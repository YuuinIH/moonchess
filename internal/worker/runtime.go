package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yuuinih/moonchess/internal/control"
	"github.com/yuuinih/moonchess/internal/game"
	"github.com/yuuinih/moonchess/internal/httpjson"
	"github.com/yuuinih/moonchess/internal/state"
)

type Runtime struct {
	ID           string
	Plane        control.Plane
	Store        state.Store
	LeaseTTL     time.Duration
	PollInterval time.Duration
	Logger       *slog.Logger

	mu     sync.RWMutex
	actors map[string]*Actor
}

func NewRuntime(id string, plane control.Plane, store state.Store, logger *slog.Logger) *Runtime {
	return &Runtime{
		ID: id, Plane: plane, Store: store, LeaseTTL: 3 * time.Second,
		PollInterval: 500 * time.Millisecond, Logger: logger, actors: make(map[string]*Actor),
	}
}

func (r *Runtime) Run(ctx context.Context) error {
	if err := r.reconcile(ctx); err != nil {
		r.Logger.Warn("initial reconcile failed", "error", err)
	}
	ticker := time.NewTicker(r.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.closeActors()
			return ctx.Err()
		case <-ticker.C:
			if err := r.reconcile(ctx); err != nil {
				r.Logger.Warn("reconcile failed", "error", err)
			}
		}
	}
}

func (r *Runtime) reconcile(ctx context.Context) error {
	records, err := r.Plane.List(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, record := range records {
		actor := r.actor(record.GameID)
		if record.OwnerID == r.ID && record.LeaseUntil.After(now) {
			if actor != nil && actor.Lease().Epoch == record.Epoch {
				if _, err := r.Plane.Renew(ctx, actor.Lease(), r.LeaseTTL); err != nil && !errors.Is(err, control.ErrFenced) {
					r.Logger.Warn("lease renewal failed", "game", record.GameID, "error", err)
				}
				continue
			}
			lease := control.Lease{GameID: record.GameID, WorkerID: r.ID, Epoch: record.Epoch, ValidUntil: record.LeaseUntil}
			if err := r.restore(ctx, record, lease); err != nil {
				r.Logger.Warn("restore owned game failed", "game", record.GameID, "error", err)
			}
			continue
		}
		if record.OwnerID != "" && record.LeaseUntil.After(now) {
			continue
		}
		lease, err := r.Plane.Acquire(ctx, record.GameID, r.ID, r.LeaseTTL)
		if errors.Is(err, control.ErrBusy) {
			continue
		}
		if err != nil {
			r.Logger.Warn("acquire failed", "game", record.GameID, "error", err)
			continue
		}
		if err := r.restore(ctx, record, lease); err != nil {
			r.Logger.Error("restore after acquisition failed", "game", record.GameID, "epoch", lease.Epoch, "error", err)
			continue
		}
		r.Logger.Info("game acquired", "game", record.GameID, "epoch", lease.Epoch, "seq", record.SnapshotSeq)
	}
	return nil
}

func (r *Runtime) restore(ctx context.Context, record control.Record, lease control.Lease) error {
	payload, err := r.Store.Get(ctx, record.SnapshotKey)
	if err != nil {
		return fmt.Errorf("get %s: %w", record.SnapshotKey, err)
	}
	var snapshot game.State
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return fmt.Errorf("decode snapshot: %w", err)
	}
	actor := NewActor(lease, record.SnapshotSeq, snapshot, r.Store, r.Plane)
	r.mu.Lock()
	previous := r.actors[record.GameID]
	r.actors[record.GameID] = actor
	r.mu.Unlock()
	if previous != nil {
		previous.Close()
	}
	return nil
}

func (r *Runtime) actor(gameID string) *Actor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.actors[gameID]
}

func (r *Runtime) closeActors() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, actor := range r.actors {
		actor.Close()
	}
}

func (r *Runtime) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.Write(w, http.StatusOK, map[string]string{"status": "ok", "worker": r.ID})
	})
	mux.HandleFunc("POST /internal/games/{gameID}/moves", r.handleMove)
	return mux
}

func (r *Runtime) handleMove(w http.ResponseWriter, request *http.Request) {
	gameID := strings.TrimSpace(request.PathValue("gameID"))
	actor := r.actor(gameID)
	if actor == nil {
		httpjson.Write(w, http.StatusConflict, map[string]string{"error": "game is not loaded on this worker"})
		return
	}
	var body struct {
		Move string `json:"move"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, request.Body, 4096))
	if err := decoder.Decode(&body); err != nil || body.Move == "" {
		httpjson.Write(w, http.StatusBadRequest, map[string]string{"error": "move must be a UCI string such as e2e4"})
		return
	}
	snapshot, record, err := actor.Move(request.Context(), body.Move)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, control.ErrFenced) {
			status = http.StatusConflict
		}
		httpjson.Write(w, status, map[string]string{"error": err.Error()})
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"game": snapshot, "control": record})
}
