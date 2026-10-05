package gateway_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yuuinih/moonchess/internal/control"
	"github.com/yuuinih/moonchess/internal/game"
	"github.com/yuuinih/moonchess/internal/gateway"
	"github.com/yuuinih/moonchess/internal/state"
)

type eventLobby struct{ control.Lobby }

func (eventLobby) Authenticate(_ context.Context, token string) (control.Client, error) {
	if token != "valid" {
		return control.Client{}, control.ErrUnauthorized
	}
	return control.Client{ID: "white", Nickname: "Moon", GameID: "game"}, nil
}
func (eventLobby) Queued(context.Context, control.Client) (bool, error) { return false, nil }

type eventPlane struct {
	control.Plane
	record control.Record
}

func (p eventPlane) Get(context.Context, string) (control.Record, error) { return p.record, nil }
func (p eventPlane) OwnershipEvents(_ context.Context, _ string, after int64) ([]control.OwnershipEvent, error) {
	if after < 12 {
		return []control.OwnershipEvent{{Kind: "acquired", Revision: 12, Control: p.record}}, nil
	}
	return nil, nil
}

type eventStore struct {
	state.Store
	payload []byte
}

func (s eventStore) Get(context.Context, string) ([]byte, error) { return s.payload, nil }
func TestSSEReconnectFromCanonicalCheckpointOnNewGateway(t *testing.T) {
	snapshot := game.NewState()
	for _, move := range []string{"f2f3", "e7e5", "g2g4", "d8h4"} {
		var err error
		snapshot, err = snapshot.Apply(move)
		if err != nil {
			t.Fatal(err)
		}
	}
	cp := state.Checkpoint{Seq: 4, Head: "head", State: snapshot}
	payload, _ := json.Marshal(cp)
	ref := state.Ref("checkpoint", "game", 4, payload)
	plane := eventPlane{record: control.Record{GameID: "game", OwnerID: "worker-b", Epoch: 3, CommittedSeq: 4, HeadRef: "head", CheckpointSeq: 4, CheckpointRef: ref, WhiteClientID: "white", BlackClientID: "black", Revision: 12}}
	// A completely new Gateway has no cached events, and all old deltas are absent.
	server := httptest.NewServer((&gateway.Gateway{Plane: plane, Lobby: eventLobby{}, Store: eventStore{payload: payload}}).Handler())
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL+"/api/events", nil)
	req.Header.Set("Cookie", "moonchess_session=valid")
	req.Header.Set("Last-Event-ID", "game:1:1:10")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(resp.Header)
	}
	scanner := bufio.NewScanner(resp.Body)
	event := ""
	var seqs []int64
	ownership := false
	finished := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var body struct {
			Seq     int64          `json:"seq"`
			Kind    string         `json:"kind"`
			Game    game.State     `json:"game"`
			Control control.Record `json:"control"`
		}
		if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &body); err != nil {
			t.Fatal(err)
		}
		switch event {
		case "ownership":
			ownership = body.Kind == "acquired"
		case "move_committed":
			seqs = append(seqs, body.Seq)
		case "game_state":
			if body.Control.OwnerID != "worker-b" || body.Game.Status != "0-1" {
				t.Fatal(body)
			}
		case "game_finished":
			finished = true
		}
		if finished {
			break
		}
	}
	if !ownership || !finished || len(seqs) != 3 || seqs[0] != 2 || seqs[1] != 3 || seqs[2] != 4 {
		t.Fatalf("ownership=%v finished=%v recovered=%v scan=%v", ownership, finished, seqs, scanner.Err())
	}
}

func TestSSEReconnectRecoversFinishAfterCheckpointAndDeltaGC(t *testing.T) {
	for _, reason := range []string{"resignation", "abandonment", "timeout"} {
		t.Run(reason, func(t *testing.T) {
			snapshot := game.NewState()
			for _, move := range []string{"e2e4", "e7e5", "g1f3", "b8c6", "f1c4", "g8f6", "d2d3"} {
				var err error
				snapshot, err = snapshot.Apply(move)
				if err != nil {
					t.Fatal(err)
				}
			}
			snapshot, _ = snapshot.Finish("black", reason)
			payload, _ := json.Marshal(state.Checkpoint{Seq: 8, Head: "final-head", State: snapshot})
			ref := state.Ref("checkpoint", "game", 8, payload)
			plane := eventPlane{record: control.Record{GameID: "game", OwnerID: "worker-b", Epoch: 3, CommittedSeq: 8, HeadRef: "final-head", CheckpointSeq: 8, CheckpointRef: ref, WhiteClientID: "white", BlackClientID: "black", Revision: 12}}
			server := httptest.NewServer((&gateway.Gateway{Plane: plane, Lobby: eventLobby{}, Store: eventStore{payload: payload}}).Handler())
			defer server.Close()
			for _, cursor := range []string{"game:6:1:10", "game:8:3:12"} {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/events", nil)
				req.Header.Set("Cookie", "moonchess_session=valid")
				req.Header.Set("Last-Event-ID", cursor)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				scanner := bufio.NewScanner(resp.Body)
				event := ""
				var seqs []int64
				finished := false
				for scanner.Scan() {
					line := scanner.Text()
					if strings.HasPrefix(line, "event: ") {
						event = strings.TrimPrefix(line, "event: ")
					}
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var body struct {
						Seq     int64          `json:"seq"`
						Game    game.State     `json:"game"`
						Control control.Record `json:"control"`
					}
					if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &body); err != nil {
						t.Fatal(err)
					}
					if event == "move_committed" {
						seqs = append(seqs, body.Seq)
					}
					if event == "game_finished" {
						if body.Game.Reason != reason || body.Game.Status != "1-0" || body.Control.CommittedSeq != 8 {
							t.Fatal(body)
						}
						finished = true
						break
					}
				}
				resp.Body.Close()
				cancel()
				if !finished || cursor == "game:6:1:10" && (len(seqs) != 1 || seqs[0] != 7) || cursor == "game:8:3:12" && len(seqs) != 0 {
					t.Fatalf("cursor=%s finished=%v seqs=%v error=%v", cursor, finished, seqs, scanner.Err())
				}
			}
		})
	}
}
