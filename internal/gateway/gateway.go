package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yuuinih/moonchess/internal/control"
	"github.com/yuuinih/moonchess/internal/game"
	"github.com/yuuinih/moonchess/internal/httpjson"
	"github.com/yuuinih/moonchess/internal/id"
	"github.com/yuuinih/moonchess/internal/state"
	webassets "github.com/yuuinih/moonchess/web"
)

type Gateway struct {
	Lobby           control.Lobby
	Plane           control.Plane
	Store           state.Store
	WorkerEndpoints map[string]string
	Client          *http.Client
}

func (g *Gateway) Handler() http.Handler {
	if g.Client == nil {
		g.Client = &http.Client{Timeout: 5 * time.Second}
	}
	if g.Lobby == nil {
		g.Lobby, _ = g.Plane.(control.Lobby)
	}
	mux := http.NewServeMux()
	g.lobbyRoutes(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.Write(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/games", g.createGame)
	mux.HandleFunc("GET /api/games/{gameID}", g.getGame)
	mux.HandleFunc("POST /api/games/{gameID}/moves", g.move)
	mux.HandleFunc("POST /api/games/{gameID}/migrate", g.migrate)
	mux.Handle("/", http.FileServer(http.FS(webassets.Assets)))
	return mux
}

func (g *Gateway) createGame(w http.ResponseWriter, request *http.Request) {
	gameID, err := randomID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	snapshot := game.NewState()
	payload, _ := json.Marshal(state.Checkpoint{Seq: 0, State: snapshot})
	key := state.Ref("checkpoint", gameID, 0, payload)
	if err := g.Store.Put(request.Context(), key, payload); err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	record, err := g.Plane.Create(request.Context(), gameID, key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, map[string]any{"game": snapshot, "control": record})
}

func (g *Gateway) getGame(w http.ResponseWriter, request *http.Request) {
	record, err := g.Plane.Get(request.Context(), request.PathValue("gameID"))
	if errors.Is(err, control.ErrNotFound) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if record.WhiteClientID != "" {
		c, authErr := g.session(request)
		if authErr != nil {
			writeError(w, 401, authErr)
			return
		}
		if c.ID != record.WhiteClientID && c.ID != record.BlackClientID {
			writeError(w, 403, control.ErrUnauthorized)
			return
		}
	}
	var materialized state.Materialized
	for attempt := 0; attempt < 2; attempt++ {
		materialized, _, err = state.MaterializeWithFallback(request.Context(), g.Store, record.CommittedSeq, record.HeadRef, record.CheckpointRef, record.CheckpointSeq, record.FallbackRef, record.FallbackSeq, nil)
		if err == nil {
			break
		}
		fresh, readErr := g.Plane.Get(request.Context(), record.GameID)
		if readErr != nil || fresh.Revision == record.Revision {
			break
		}
		record = fresh
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"game": materialized.State, "control": record})
}

func (g *Gateway) move(w http.ResponseWriter, request *http.Request) {
	var body struct {
		Move     string `json:"move"`
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, request.Body, 4096)).Decode(&body); err != nil || body.Move == "" {
		writeError(w, http.StatusBadRequest, errors.New("move must be a UCI string such as e2e4"))
		return
	}
	record, err := g.Plane.Get(request.Context(), request.PathValue("gameID"))
	if err != nil {
		if errors.Is(err, control.ErrNotFound) {
			writeError(w, http.StatusNotFound, err)
		} else {
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	if record.WhiteClientID != "" {
		c, authErr := g.session(request)
		if authErr != nil {
			writeError(w, 401, authErr)
			return
		}
		if c.ID != record.WhiteClientID && c.ID != record.BlackClientID {
			writeError(w, 403, control.ErrUnauthorized)
			return
		}
		body.ClientID = c.ID
	} else {
		body.ClientID = ""
	}
	endpoint := g.WorkerEndpoints[record.OwnerID]
	if endpoint == "" {
		writeError(w, http.StatusServiceUnavailable, errors.New("room is between owners; retry shortly"))
		return
	}
	payload, _ := json.Marshal(body)
	url := strings.TrimRight(endpoint, "/") + "/internal/games/" + record.GameID + "/moves"
	req, err := http.NewRequestWithContext(request.Context(), http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.Client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("owner %s unavailable: %w", record.OwnerID, err))
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

func (g *Gateway) migrate(w http.ResponseWriter, request *http.Request) {
	id := request.PathValue("gameID")
	record, err := g.Plane.Get(request.Context(), id)
	if err != nil {
		writeError(w, 404, err)
		return
	}
	var body struct {
		Target        string `json:"target"`
		ExpectedHead  string `json:"expectedHead"`
		ExpectedSeq   int64  `json:"expectedSeq"`
		ExpectedEpoch int64  `json:"expectedEpoch"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, request.Body, 4096)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, 400, errors.New("invalid migration body"))
		return
	}
	if body.Target == "" {
		body.Target, err = g.selectTarget(request.Context(), record)
		if err != nil {
			writeError(w, 503, err)
			return
		}
	}
	if body.Target == record.OwnerID || g.WorkerEndpoints[body.Target] == "" {
		writeError(w, 400, errors.New("invalid target"))
		return
	}
	endpoint := g.WorkerEndpoints[record.OwnerID]
	if endpoint == "" {
		writeError(w, 503, errors.New("game has no owner"))
		return
	}
	prepare, err := http.NewRequestWithContext(request.Context(), http.MethodPost, strings.TrimRight(g.WorkerEndpoints[body.Target], "/")+"/internal/games/"+id+"/prepare", nil)
	if err != nil {
		writeError(w, 500, err)
		return
	}
	prepared, err := g.Client.Do(prepare)
	if err != nil {
		writeError(w, 502, fmt.Errorf("prepare target: %w", err))
		return
	}
	var ready struct {
		Seq     int64  `json:"seq"`
		HeadRef string `json:"headRef"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(prepared.Body, 1<<20)).Decode(&ready)
	_ = prepared.Body.Close()
	if prepared.StatusCode != 200 || decodeErr != nil || ready.Seq != record.CommittedSeq || ready.HeadRef != record.HeadRef {
		writeError(w, 409, errors.New("target did not materialize the observed canonical head; retry migration"))
		return
	}
	body.ExpectedHead = record.HeadRef
	body.ExpectedSeq = record.CommittedSeq
	body.ExpectedEpoch = record.Epoch
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(request.Context(), http.MethodPost, strings.TrimRight(endpoint, "/")+"/internal/games/"+id+"/migrate", bytes.NewReader(payload))
	if err != nil {
		writeError(w, 500, err)
		return
	}
	resp, err := g.Client.Do(req)
	if err != nil {
		writeError(w, 502, err)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

func (g *Gateway) selectTarget(ctx context.Context, record control.Record) (string, error) {
	chosen := ""
	bestSeq := int64(-1)
	bestExact := false
	for worker, endpoint := range g.WorkerEndpoints {
		if worker == record.OwnerID {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/healthz", nil)
		if err != nil {
			continue
		}
		resp, err := g.Client.Do(req)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			continue
		}
		progress, err := g.Plane.GetProgress(ctx, record.GameID, worker)
		seq := int64(-1)
		exact := false
		if err == nil {
			seq = progress.Seq
			exact = progress.Seq == record.CommittedSeq && progress.HeadRef == record.HeadRef
		}
		if chosen == "" || exact && !bestExact || exact == bestExact && (seq > bestSeq || seq == bestSeq && worker < chosen) {
			chosen = worker
			bestSeq = seq
			bestExact = exact
		}
	}
	if chosen == "" {
		return "", errors.New("no live migration target")
	}
	return chosen, nil
}

func randomID() (string, error) {
	value, err := id.RandomHex(12)
	if err != nil {
		return "", fmt.Errorf("generate game id: %w", err)
	}
	return value, nil
}

func ParseWorkerEndpoints(value string) (map[string]string, error) {
	result := make(map[string]string)
	for _, item := range strings.Split(value, ",") {
		if strings.TrimSpace(item) == "" {
			continue
		}
		name, endpoint, ok := strings.Cut(item, "=")
		if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(endpoint) == "" {
			return nil, fmt.Errorf("invalid worker endpoint %q", item)
		}
		result[strings.TrimSpace(name)] = strings.TrimSpace(endpoint)
	}
	return result, nil
}

func writeError(w http.ResponseWriter, status int, err error) {
	httpjson.Write(w, status, map[string]string{"error": err.Error()})
}
