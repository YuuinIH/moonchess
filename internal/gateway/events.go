package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yuuinih/moonchess/internal/control"
)

type eventCursor struct {
	Game     string
	Seq      int64
	Epoch    int64
	Revision int64
}

func (c eventCursor) ID() string {
	return fmt.Sprintf("%s:%d:%d:%d", c.Game, c.Seq, c.Epoch, c.Revision)
}
func parseCursor(value string) eventCursor {
	p := strings.Split(value, ":")
	if len(p) != 4 {
		return eventCursor{}
	}
	seq, e1 := strconv.ParseInt(p[1], 10, 64)
	epoch, e2 := strconv.ParseInt(p[2], 10, 64)
	rev, e3 := strconv.ParseInt(p[3], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || seq < 0 || epoch < 0 || rev < 0 {
		return eventCursor{}
	}
	return eventCursor{p[0], seq, epoch, rev}
}

func (g *Gateway) events(w http.ResponseWriter, r *http.Request) {
	c, err := g.session(r)
	if err != nil {
		writeError(w, 401, err)
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		writeError(w, 500, fmt.Errorf("streaming unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	send := func(kind, id string, data any) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
		b, _ := json.Marshal(data)
		if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", id, kind, b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	_, _ = fmt.Fprint(w, "retry: 1000\n\n")
	_ = rc.Flush()
	cursor := parseCursor(r.Header.Get("Last-Event-ID"))
	var previous control.Record
	lastStatus := ""
	lastClient := ""
	first := true
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		fresh, err := g.session(r)
		if err != nil {
			return
		}
		c = fresh
		queued, err := g.Lobby.Queued(r.Context(), c)
		if err == nil {
			snapshot := matchmakingSnapshot(c, queued)
			clientJSON, _ := json.Marshal(c)
			if snapshot.Status != lastStatus || string(clientJSON) != lastClient {
				if !send("matchmaking", cursor.ID(), snapshot) {
					return
				}
				lastStatus = snapshot.Status
				lastClient = string(clientJSON)
			}
		}
		if c.GameID != "" {
			record, m, err := g.materialize(r.Context(), c.GameID)
			if err == nil {
				if cursor.Game != c.GameID || cursor.Seq > record.CommittedSeq {
					cursor = eventCursor{Game: c.GameID}
				}
				if timeline, ok := g.Plane.(control.Timeline); ok {
					events, eventErr := timeline.OwnershipEvents(r.Context(), c.GameID, cursor.Revision)
					if eventErr != nil {
						return
					}
					for _, event := range events {
						cursor.Revision = event.Revision
						if !send("ownership", cursor.ID(), event) {
							return
						}
					}
				}
				// The canonical checkpoint includes the entire legal move list, even after
				// delta GC. Recover every missed committed move without a gateway-local log.
				finishedSent := false
				for seq := cursor.Seq + 1; seq <= record.CommittedSeq; seq++ {
					id := eventCursor{c.GameID, seq, record.Epoch, cursor.Revision}.ID()
					if seq <= int64(len(m.State.Moves)) {
						if !send("move_committed", id, map[string]any{"game_id": c.GameID, "seq": seq, "move": m.State.Moves[seq-1]}) {
							return
						}
					} else {
						// A finish command advances the canonical sequence but is not a
						// chess move. It remains recoverable from the final checkpoint.
						if seq != record.CommittedSeq || seq != int64(len(m.State.Moves))+1 || m.State.Status == "active" {
							return
						}
						if !send("game_finished", id, map[string]any{"game": m.State, "control": record, "serverTimeUnixMs": time.Now().UnixMilli()}) {
							return
						}
						finishedSent = true
					}
					cursor.Seq = seq
				}
				id := eventCursor{c.GameID, record.CommittedSeq, record.Epoch, cursor.Revision}.ID()
				if first || previous.Revision != record.Revision || previous.OwnerID != record.OwnerID || previous.GameID != record.GameID {
					if !send("game_state", id, map[string]any{"game": m.State, "control": record, "serverTimeUnixMs": time.Now().UnixMilli(), "debug": g.debug(r, record)}) {
						return
					}
					if first || previous.OwnerID != record.OwnerID || previous.Epoch != record.Epoch || previous.Phase != record.Phase {
						if !send("ownership_state", id, map[string]any{"control": record, "recovered": cursor.Epoch != 0 && cursor.Epoch != record.Epoch}) {
							return
						}
					}
					if m.State.Status != "active" && !finishedSent {
						if !send("game_finished", id, map[string]any{"game": m.State, "control": record, "serverTimeUnixMs": time.Now().UnixMilli()}) {
							return
						}
					}
					previous = record
					first = false
				}
				cursor.Epoch = record.Epoch
			}
		}
		_ = rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
			return
		}
		if rc.Flush() != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
func (g *Gateway) debug(r *http.Request, record control.Record) map[string]any {
	out := map[string]any{}
	for worker, endpoint := range g.WorkerEndpoints {
		req, err := http.NewRequestWithContext(r.Context(), "GET", strings.TrimRight(endpoint, "/")+"/internal/games/"+record.GameID+"/progress", nil)
		if err != nil {
			continue
		}
		// Debug is advisory and must not hold up canonical state on a dead worker.
		client := &http.Client{Timeout: 300 * time.Millisecond}
		resp, err := client.Do(req)
		if err != nil {
			out[worker] = map[string]string{"status": "unavailable"}
			continue
		}
		var p map[string]any
		if resp.StatusCode == 200 && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&p) == nil {
			out[worker] = p
		}
		_ = resp.Body.Close()
	}
	return out
}
