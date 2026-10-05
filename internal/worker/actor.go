package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/yuuinih/moonchess/internal/control"
	"github.com/yuuinih/moonchess/internal/game"
	"github.com/yuuinih/moonchess/internal/id"
	"github.com/yuuinih/moonchess/internal/state"
)

var ErrActorClosed = errors.New("room actor is closed")

// Actor serializes commands and never publishes a state before the fenced CAS.
type Actor struct {
	lease           control.Lease
	store           state.Store
	plane           control.Plane
	mu              sync.Mutex
	closed          bool
	stop            chan struct{}
	record          control.Record
	materialized    state.Materialized
	checkpointEvery int64
}

func NewActor(lease control.Lease, record control.Record, materialized state.Materialized, store state.Store, plane control.Plane) *Actor {
	return &Actor{lease: lease, record: record, materialized: materialized, store: store, plane: plane, checkpointEvery: 8, stop: make(chan struct{})}
}
func (a *Actor) Lease() control.Lease { return a.lease }
func (a *Actor) State() game.State    { a.mu.Lock(); defer a.mu.Unlock(); return a.materialized.State }
func (a *Actor) Close() {
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.stop)
	}
	a.mu.Unlock()
}
func (a *Actor) Done() <-chan struct{} { return a.stop }

func (a *Actor) Move(ctx context.Context, move string, clients ...string) (game.State, control.Record, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return game.State{}, control.Record{}, ErrActorClosed
	}
	observed, err := a.plane.Get(ctx, a.lease.GameID)
	if err != nil {
		return game.State{}, control.Record{}, err
	}
	if observed.OwnerID != a.lease.WorkerID || observed.Epoch != a.lease.Epoch {
		return game.State{}, control.Record{}, control.ErrFenced
	}
	if observed.HeadRef != a.materialized.Head {
		m, _, err := state.MaterializeWithFallback(ctx, a.store, observed.CommittedSeq, observed.HeadRef, observed.CheckpointRef, observed.CheckpointSeq, observed.FallbackRef, observed.FallbackSeq, &a.materialized)
		if err != nil {
			return game.State{}, control.Record{}, err
		}
		a.materialized = m
	}
	if observed.WhiteClientID != "" {
		client := ""
		if len(clients) > 0 {
			client = clients[0]
		}
		expected := observed.WhiteClientID
		if a.materialized.State.Turn == "black" {
			expected = observed.BlackClientID
		}
		if client == "" || client != expected {
			return game.State{}, control.Record{}, control.ErrUnauthorized
		}
	}
	if a.materialized.State.Status != "active" {
		return game.State{}, control.Record{}, errors.New("game is finished")
	}
	next, err := a.materialized.State.Apply(move)
	if err != nil {
		return game.State{}, control.Record{}, err
	}
	attemptID, err := id.RandomHex(12)
	if err != nil {
		return game.State{}, control.Record{}, err
	}
	delta := state.Delta{Seq: observed.CommittedSeq + 1, Prev: observed.HeadRef, Move: move, AttemptID: attemptID}
	payload, err := json.Marshal(delta)
	if err != nil {
		return game.State{}, control.Record{}, err
	}
	ref := state.Ref("delta", a.lease.GameID, delta.Seq, payload)
	if err := a.store.Put(ctx, ref, payload); err != nil {
		return game.State{}, control.Record{}, fmt.Errorf("put delta: %w", err)
	}
	committed, err := a.plane.Commit(ctx, a.lease, observed, ref)
	if err != nil {
		// A lost transaction response is ambiguous. Read the canonical head before replying.
		reconcileCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		current, readErr := a.plane.Get(reconcileCtx, a.lease.GameID)
		if readErr == nil && current.HeadRef == ref && current.CommittedSeq == delta.Seq {
			committed = current
		} else {
			return game.State{}, control.Record{}, fmt.Errorf("commit move: %w", err)
		}
	}
	a.materialized = state.Materialized{Seq: committed.CommittedSeq, Head: committed.HeadRef, State: next}
	a.record = committed
	_ = a.plane.SetProgress(ctx, a.lease.GameID, a.lease.WorkerID, control.Progress{Seq: a.materialized.Seq, HeadRef: a.materialized.Head})
	if a.checkpointEvery > 0 && committed.CommittedSeq%a.checkpointEvery == 0 {
		a.writeCheckpoint(ctx)
	}
	return next, a.record, nil
}
func (a *Actor) writeCheckpoint(ctx context.Context) {
	previousBytes, err := a.store.Get(ctx, a.record.CheckpointRef)
	if err != nil {
		return
	}
	var previous state.Checkpoint
	if err := json.Unmarshal(previousBytes, &previous); err != nil {
		return
	}
	anchor := previous.Head
	if previous.Seq == 0 {
		anchor = a.record.CheckpointRef
	}
	var covered []string
	for ref, seq := a.materialized.Head, a.materialized.Seq; seq > previous.Seq; seq-- {
		bytes, err := a.store.Get(ctx, ref)
		if err != nil {
			return
		}
		var d state.Delta
		if err := json.Unmarshal(bytes, &d); err != nil || d.Seq != seq {
			return
		}
		covered = append(covered, ref)
		ref = d.Prev
		if seq == previous.Seq+1 && ref != anchor {
			return
		}
	}
	cp := state.Checkpoint{Seq: a.materialized.Seq, Head: a.materialized.Head, State: a.materialized.State, PreviousCheckpointRef: a.record.CheckpointRef, CoveredRefs: covered}
	payload, err := json.Marshal(cp)
	if err != nil {
		return
	}
	ref := state.Ref("checkpoint", a.lease.GameID, cp.Seq, payload)
	if err = a.store.Put(ctx, ref, payload); err != nil {
		return
	}
	if record, err := a.plane.Checkpoint(ctx, a.lease, a.record, ref); err == nil {
		// Keep a grace period for readers of the old metadata and Mooncake's
		// object leases. The old latest is now fallback, so only its covered
		// deltas and the older checkpoint are eligible for deletion.
		obsolete := append([]string(nil), previous.CoveredRefs...)
		if previous.PreviousCheckpointRef != "" {
			obsolete = append(obsolete, previous.PreviousCheckpointRef)
		}
		a.record = record
		if len(obsolete) > 0 {
			go a.collectObsolete(obsolete)
		}
	}
}

func (a *Actor) collectObsolete(refs []string) {
	// A failed or interrupted GC may leak unreachable objects, never canonical
	// state. Retry transient Store lease failures without extending move ACK time.
	time.Sleep(12 * time.Second)
	for attempt := 0; attempt < 24 && len(refs) > 0; attempt++ {
		remaining := refs[:0]
		for _, ref := range refs {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := a.store.Remove(ctx, ref)
			cancel()
			if err != nil && !errors.Is(err, state.ErrNotFound) {
				remaining = append(remaining, ref)
			}
		}
		refs = remaining
		if len(refs) > 0 {
			time.Sleep(5 * time.Second)
		}
	}
	if len(refs) > 0 {
		slog.Warn("checkpoint GC incomplete", "game", a.lease.GameID, "remaining", len(refs))
	}
}
