package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yuuinih/moonchess/internal/control"
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
	mu           sync.RWMutex
	actors       map[string]*Actor
	local        map[string]state.Materialized
	metrics      map[string]state.Metrics
	history      map[string][]state.Metrics
	hotCount     map[string]int64
	takeover     map[string]state.Metrics
}

func NewRuntime(id string, plane control.Plane, store state.Store, logger *slog.Logger) *Runtime {
	return &Runtime{ID: id, Plane: plane, Store: store, LeaseTTL: 3 * time.Second, PollInterval: 400 * time.Millisecond, Logger: logger, actors: map[string]*Actor{}, local: map[string]state.Materialized{}, metrics: map[string]state.Metrics{}, history: map[string][]state.Metrics{}, hotCount: map[string]int64{}, takeover: map[string]state.Metrics{}}
}
func (r *Runtime) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.PollInterval)
	defer ticker.Stop()
	for {
		if err := r.reconcile(ctx); err != nil && ctx.Err() == nil {
			r.Logger.Warn("reconcile failed", "error", err)
		}
		select {
		case <-ctx.Done():
			r.closeActors()
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (r *Runtime) reconcile(ctx context.Context) error {
	records, err := r.Plane.List(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		observedNoOwnerAt := time.Now()
		var materializeMetrics state.Metrics
		actor := r.actor(record.GameID)
		if record.OwnerID == r.ID && actor != nil && actor.Lease().Epoch == record.Epoch {
			continue
		}
		if record.OwnerID != "" && record.OwnerID != r.ID || record.OwnerID == "" {
			if metrics, err := r.materialize(ctx, record); err != nil {
				r.Logger.Warn("materialize failed", "game", record.GameID, "error", err)
				continue
			} else {
				materializeMetrics = metrics
			}
		}
		if record.OwnerID != "" {
			continue
		}
		if record.TargetID != "" && record.TargetID != r.ID && time.Now().UnixMilli() < record.TargetUntilUnixMS {
			continue
		}
		lease, err := r.Plane.Acquire(ctx, record, r.ID, int64(r.LeaseTTL.Seconds()+0.5))
		if errors.Is(err, control.ErrBusy) {
			continue
		}
		if err != nil {
			r.Logger.Warn("acquire failed", "game", record.GameID, "error", err)
			continue
		}
		fresh, err := r.Plane.Get(ctx, record.GameID)
		if err != nil {
			r.Logger.Error("get acquired game failed", "error", err)
			releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = r.Plane.Release(releaseCtx, lease)
			cancel()
			continue
		}
		r.mu.Lock()
		materializeMetrics.ObservedPauseMS = time.Since(observedNoOwnerAt).Milliseconds()
		r.takeover[record.GameID] = materializeMetrics
		m := r.local[record.GameID]
		previous := r.actors[record.GameID]
		newActor := NewActor(lease, fresh, m, r.Store, r.Plane)
		r.actors[record.GameID] = newActor
		r.mu.Unlock()
		go r.keepLease(ctx, newActor)
		if previous != nil {
			previous.Close()
		}
		r.Logger.Info("game acquired", "game", record.GameID, "epoch", lease.Epoch, "seq", fresh.CommittedSeq)
	}
	return nil
}
func (r *Runtime) keepLease(ctx context.Context, a *Actor) {
	interval := r.LeaseTTL / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.Done():
			return
		case <-ticker.C:
			if err := r.Plane.Renew(ctx, a.Lease()); err != nil {
				if errors.Is(err, control.ErrFenced) {
					return
				}
				r.Logger.Warn("lease renewal failed", "game", a.Lease().GameID, "error", err)
			}
		}
	}
}
func (r *Runtime) materialize(ctx context.Context, record control.Record) (state.Metrics, error) {
	start := time.Now()
	r.mu.RLock()
	cached, ok := r.local[record.GameID]
	r.mu.RUnlock()
	var local *state.Materialized
	if ok {
		local = &cached
	}
	var m state.Materialized
	var metrics state.Metrics
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		m, metrics, err = state.MaterializeWithFallback(ctx, r.Store, record.CommittedSeq, record.HeadRef, record.CheckpointRef, record.CheckpointSeq, record.FallbackRef, record.FallbackSeq, local)
		if err == nil {
			break
		}
		fresh, readErr := r.Plane.Get(ctx, record.GameID)
		if readErr != nil || fresh.Revision == record.Revision {
			break
		}
		record = fresh
	}
	if err != nil {
		return metrics, err
	}
	metrics.RecoveryLatencyMS = time.Since(start).Milliseconds()
	r.mu.Lock()
	r.local[record.GameID] = m
	if metrics.Path == "hot" {
		r.hotCount[record.GameID]++
	} else {
		r.metrics[record.GameID] = metrics
		r.history[record.GameID] = append(r.history[record.GameID], metrics)
		if len(r.history[record.GameID]) > 32 {
			r.history[record.GameID] = r.history[record.GameID][len(r.history[record.GameID])-32:]
		}
	}
	r.mu.Unlock()
	if err := r.Plane.SetProgress(ctx, record.GameID, r.ID, control.Progress{Seq: m.Seq, HeadRef: m.Head}); err != nil {
		return metrics, err
	}
	return metrics, nil
}
func (r *Runtime) actor(id string) *Actor { r.mu.RLock(); defer r.mu.RUnlock(); return r.actors[id] }
func (r *Runtime) closeActors() {
	r.mu.Lock()
	actors := make([]*Actor, 0, len(r.actors))
	for _, a := range r.actors {
		actors = append(actors, a)
	}
	r.mu.Unlock()
	for _, a := range actors {
		a.Close()
	}
}
func (r *Runtime) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.Write(w, 200, map[string]string{"status": "ok", "worker": r.ID})
	})
	mux.HandleFunc("POST /internal/games/{gameID}/moves", r.handleMove)
	mux.HandleFunc("POST /internal/games/{gameID}/migrate", r.handleMigrate)
	mux.HandleFunc("POST /internal/games/{gameID}/prepare", r.handlePrepare)
	mux.HandleFunc("GET /internal/games/{gameID}/progress", r.handleProgress)
	return mux
}
func (r *Runtime) handleMove(w http.ResponseWriter, req *http.Request) {
	a := r.actor(strings.TrimSpace(req.PathValue("gameID")))
	if a == nil {
		httpjson.Write(w, 409, map[string]string{"error": "game is not loaded on this worker"})
		return
	}
	var body struct {
		Move string `json:"move"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 4096)).Decode(&body); err != nil || body.Move == "" {
		httpjson.Write(w, 400, map[string]string{"error": "move must be a UCI string such as e2e4"})
		return
	}
	snapshot, record, err := a.Move(req.Context(), body.Move)
	if err != nil {
		status := 422
		if errors.Is(err, control.ErrFenced) {
			status = 409
		}
		httpjson.Write(w, status, map[string]string{"error": err.Error()})
		return
	}
	r.mu.Lock()
	r.local[record.GameID] = state.Materialized{Seq: record.CommittedSeq, Head: record.HeadRef, State: snapshot}
	r.mu.Unlock()
	httpjson.Write(w, 200, map[string]any{"game": snapshot, "control": record})
}
func (r *Runtime) handleMigrate(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("gameID")
	a := r.actor(id)
	if a == nil {
		httpjson.Write(w, 409, map[string]string{"error": "game is not owned here"})
		return
	}
	var body struct {
		Target        string `json:"target"`
		ExpectedHead  string `json:"expectedHead"`
		ExpectedSeq   int64  `json:"expectedSeq"`
		ExpectedEpoch int64  `json:"expectedEpoch"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil || body.Target == "" {
		httpjson.Write(w, 400, map[string]string{"error": "target is required"})
		return
	}
	record, err := r.Plane.Get(req.Context(), id)
	if err != nil {
		httpjson.Write(w, 503, map[string]string{"error": err.Error()})
		return
	}
	if record.OwnerID != r.ID || record.Epoch != a.Lease().Epoch || record.HeadRef != body.ExpectedHead || record.CommittedSeq != body.ExpectedSeq || record.Epoch != body.ExpectedEpoch {
		httpjson.Write(w, 409, map[string]string{"error": control.ErrFenced.Error()})
		return
	}
	start := time.Now()
	migrating, err := r.Plane.BeginMigration(req.Context(), a.Lease(), record, body.Target)
	if err != nil {
		httpjson.Write(w, 409, map[string]string{"error": err.Error()})
		return
	}
	// The target already materialized this head. A later move would have failed
	// the metadata CAS above, so the lease can now be transferred.
	if err := r.Plane.Release(req.Context(), a.Lease()); err != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, rollbackErr := r.Plane.CancelMigration(rollbackCtx, a.Lease(), migrating)
		cancel()
		if rollbackErr != nil {
			a.Close()
		}
		httpjson.Write(w, 503, map[string]string{"error": err.Error()})
		return
	}
	a.Close()
	httpjson.Write(w, 200, map[string]any{"control": migrating, "pauseMs": time.Since(start).Milliseconds()})
}
func (r *Runtime) handlePrepare(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("gameID")
	record, err := r.Plane.Get(req.Context(), id)
	if err != nil {
		httpjson.Write(w, 503, map[string]string{"error": err.Error()})
		return
	}
	if record.OwnerID == r.ID {
		httpjson.Write(w, 409, map[string]string{"error": "target is current owner"})
		return
	}
	metrics, err := r.materialize(req.Context(), record)
	if err != nil {
		httpjson.Write(w, 503, map[string]string{"error": err.Error()})
		return
	}
	httpjson.Write(w, 200, map[string]any{"seq": record.CommittedSeq, "headRef": record.HeadRef, "metrics": metrics})
}
func (r *Runtime) handleProgress(w http.ResponseWriter, req *http.Request) {
	id := req.PathValue("gameID")
	r.mu.RLock()
	m, ok := r.local[id]
	metrics := r.metrics[id]
	history := append([]state.Metrics(nil), r.history[id]...)
	hotCount := r.hotCount[id]
	takeover := r.takeover[id]
	r.mu.RUnlock()
	if !ok {
		httpjson.Write(w, 404, map[string]string{"error": "not materialized"})
		return
	}
	httpjson.Write(w, 200, map[string]any{"seq": m.Seq, "headRef": m.Head, "metrics": metrics, "history": history, "hotCount": hotCount, "takeover": takeover})
}
