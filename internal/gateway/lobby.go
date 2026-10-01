package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yuuinih/moonchess/internal/control"
	"github.com/yuuinih/moonchess/internal/game"
	"github.com/yuuinih/moonchess/internal/httpjson"
	"github.com/yuuinih/moonchess/internal/state"
)

func (g *Gateway) session(r *http.Request) (control.Client, error) {
	if g.Lobby == nil {
		return control.Client{}, errors.New("lobby unavailable")
	}
	cookie, err := r.Cookie("moonchess_session")
	if err != nil {
		return control.Client{}, control.ErrUnauthorized
	}
	return g.Lobby.Authenticate(r.Context(), cookie.Value)
}
func (g *Gateway) lobbyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me", g.me)
	mux.HandleFunc("PATCH /api/me", g.rename)
	mux.HandleFunc("POST /api/me/reset", g.reset)
	mux.HandleFunc("POST /api/matchmaking/enqueue", g.enqueue)
	mux.HandleFunc("DELETE /api/matchmaking/enqueue", g.cancelQueue)
	mux.HandleFunc("GET /api/matchmaking/status", g.matchStatus)
	mux.HandleFunc("GET /api/games/current", g.currentGame)
	mux.HandleFunc("POST /api/games/current/play-again", g.playAgain)
	mux.HandleFunc("GET /api/events", g.events)
}
func (g *Gateway) me(w http.ResponseWriter, r *http.Request) {
	if g.Lobby == nil {
		writeError(w, 503, errors.New("lobby unavailable"))
		return
	}
	c, err := g.session(r)
	if errors.Is(err, control.ErrUnauthorized) {
		var token string
		c, token, err = g.Lobby.NewClient(r.Context())
		if err == nil {
			http.SetCookie(w, &http.Cookie{Name: "moonchess_session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 365 * 24 * 60 * 60})
		}
	}
	if err != nil {
		writeError(w, 503, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpjson.Write(w, 200, c)
}
func (g *Gateway) rename(w http.ResponseWriter, r *http.Request) {
	c, err := g.session(r)
	if err != nil {
		writeError(w, 401, err)
		return
	}
	var body struct {
		Nickname string `json:"nickname"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body) != nil {
		writeError(w, 400, errors.New("invalid nickname"))
		return
	}
	body.Nickname = strings.TrimSpace(body.Nickname)
	if utf8.RuneCountInString(body.Nickname) < 1 || utf8.RuneCountInString(body.Nickname) > 32 {
		writeError(w, 400, errors.New("nickname must have 1–32 characters"))
		return
	}
	if err = g.Lobby.Rename(r.Context(), c, body.Nickname); err != nil {
		writeError(w, 409, err)
		return
	}
	c.Nickname = body.Nickname
	httpjson.Write(w, 200, c)
}
func (g *Gateway) reset(w http.ResponseWriter, r *http.Request) {
	c, err := g.session(r)
	if err != nil {
		writeError(w, 401, err)
		return
	}
	// Do not strand the other player by discarding an active seat.
	if c.GameID != "" {
		_, m, err := g.materialize(r.Context(), c.GameID)
		if err != nil {
			writeError(w, 503, err)
			return
		}
		if m.State.Status == "active" {
			writeError(w, 409, errors.New("finish the current game before resetting identity"))
			return
		}
	}
	if err = g.Lobby.Reset(r.Context(), c); err != nil {
		writeError(w, 409, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "moonchess_session", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	g.me(w, r.WithContext(r.Context()))
	// me must not reuse the revoked cookie; Authenticate returns ErrUnauthorized.
}
func (g *Gateway) enqueue(w http.ResponseWriter, r *http.Request) {
	c, err := g.session(r)
	if err != nil {
		writeError(w, 401, err)
		return
	}
	queued, err := g.Lobby.Queued(r.Context(), c)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	if !queued {
		if err = g.Lobby.Enqueue(r.Context(), c); err != nil {
			writeError(w, 409, err)
			return
		}
	}
	g.matchStatus(w, r)
}
func (g *Gateway) cancelQueue(w http.ResponseWriter, r *http.Request) {
	c, err := g.session(r)
	if err != nil {
		writeError(w, 401, err)
		return
	}
	if err = g.Lobby.Cancel(r.Context(), c); err != nil {
		writeError(w, 409, err)
		return
	}
	g.matchStatus(w, r)
}
func (g *Gateway) matchStatus(w http.ResponseWriter, r *http.Request) {
	c, err := g.session(r)
	if err != nil {
		writeError(w, 401, err)
		return
	}
	queued, err := g.Lobby.Queued(r.Context(), c)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	status := "home"
	if queued {
		status = "searching"
	}
	if c.GameID != "" {
		status = "matched"
	}
	httpjson.Write(w, 200, map[string]any{"status": status, "client": c})
}
func (g *Gateway) materialize(ctx context.Context, id string) (control.Record, state.Materialized, error) {
	record, err := g.Plane.Get(ctx, id)
	if err != nil {
		return record, state.Materialized{}, err
	}
	m, _, err := state.MaterializeWithFallback(ctx, g.Store, record.CommittedSeq, record.HeadRef, record.CheckpointRef, record.CheckpointSeq, record.FallbackRef, record.FallbackSeq, nil)
	if err != nil {
		fresh, readErr := g.Plane.Get(ctx, id)
		if readErr == nil && fresh.Revision != record.Revision {
			record = fresh
			m, _, err = state.MaterializeWithFallback(ctx, g.Store, record.CommittedSeq, record.HeadRef, record.CheckpointRef, record.CheckpointSeq, record.FallbackRef, record.FallbackSeq, nil)
		}
	}
	return record, m, err
}
func (g *Gateway) currentGame(w http.ResponseWriter, r *http.Request) {
	c, err := g.session(r)
	if err != nil {
		writeError(w, 401, err)
		return
	}
	if c.GameID == "" {
		httpjson.Write(w, 200, map[string]any{"game": nil})
		return
	}
	record, m, err := g.materialize(r.Context(), c.GameID)
	if err != nil {
		writeError(w, 503, err)
		return
	}
	httpjson.Write(w, 200, map[string]any{"game": m.State, "control": record})
}
func (g *Gateway) playAgain(w http.ResponseWriter, r *http.Request) {
	c, err := g.session(r)
	if err != nil {
		writeError(w, 401, err)
		return
	}
	if c.GameID != "" {
		_, m, err := g.materialize(r.Context(), c.GameID)
		if err != nil {
			writeError(w, 503, err)
			return
		}
		if m.State.Status == "active" {
			writeError(w, 409, errors.New("game is still active"))
			return
		}
		if err = g.Lobby.Leave(r.Context(), c); err != nil {
			writeError(w, 409, err)
			return
		}
	}
	g.enqueue(w, r)
}

// Every gateway may run this loop. The match transaction is the only authority.
func (g *Gateway) RunMatchmaker(ctx context.Context) error {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := g.matchOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("matchmaking retry", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (g *Gateway) matchOnce(ctx context.Context) error {
	if g.Lobby == nil {
		return errors.New("lobby unavailable")
	}
	tickets, err := g.Lobby.Tickets(ctx)
	if err != nil {
		return err
	}
	for i := 0; i+1 < len(tickets); i += 2 {
		gameID, err := randomID()
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(state.Checkpoint{Seq: 0, State: game.NewState()})
		ref := state.Ref("checkpoint", gameID, 0, payload)
		if err = g.Store.Put(ctx, ref, payload); err != nil {
			return err
		}
		_, err = g.Lobby.Match(ctx, tickets[i], tickets[i+1], gameID, ref)
		if err != nil && !errors.Is(err, control.ErrUnauthorized) {
			return fmt.Errorf("match: %w", err)
		}
	}
	return nil
}
