package gateway

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/yuuinih/moonchess/internal/state"
	webassets "github.com/yuuinih/moonchess/web"
)

type Gateway struct {
	Plane           control.Plane
	Store           state.Store
	WorkerEndpoints map[string]string
	Client          *http.Client
}

func (g *Gateway) Handler() http.Handler {
	if g.Client == nil {
		g.Client = &http.Client{Timeout: 5 * time.Second}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.Write(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/games", g.createGame)
	mux.HandleFunc("GET /api/games/{gameID}", g.getGame)
	mux.HandleFunc("POST /api/games/{gameID}/moves", g.move)
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
	payload, _ := json.Marshal(snapshot)
	key := fmt.Sprintf("game/%s/epoch/0/seq/0", gameID)
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
	payload, err := g.Store.Get(request.Context(), record.SnapshotKey)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	var snapshot game.State
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("decode snapshot: %w", err))
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"game": snapshot, "control": record})
}

func (g *Gateway) move(w http.ResponseWriter, request *http.Request) {
	var body struct {
		Move string `json:"move"`
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
	endpoint := g.WorkerEndpoints[record.OwnerID]
	if endpoint == "" || record.LeaseUntil.Before(time.Now()) {
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

func randomID() (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate game id: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
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
