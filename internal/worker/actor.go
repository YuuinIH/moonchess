package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/yuuinih/moonchess/internal/control"
	"github.com/yuuinih/moonchess/internal/game"
	"github.com/yuuinih/moonchess/internal/id"
	"github.com/yuuinih/moonchess/internal/state"
)

var ErrActorClosed = errors.New("room actor is closed")

type CommitControl interface {
	Commit(context.Context, control.Lease, int64, string) (control.Record, error)
	Get(context.Context, string) (control.Record, error)
}

type moveRequest struct {
	ctx   context.Context
	move  string
	reply chan moveResult
}

type moveResult struct {
	state  game.State
	record control.Record
	err    error
}

// Actor serializes every command for one room. It deliberately writes the
// immutable payload before attempting the fenced control-plane commit.
type Actor struct {
	lease   control.Lease
	store   state.Store
	control CommitControl
	moves   chan moveRequest
	states  chan chan game.State
	stop    chan struct{}
}

func NewActor(lease control.Lease, seq int64, initial game.State, store state.Store, commitControl CommitControl) *Actor {
	a := &Actor{
		lease: lease, store: store, control: commitControl,
		moves: make(chan moveRequest), states: make(chan chan game.State), stop: make(chan struct{}),
	}
	go a.run(seq, initial)
	return a
}

func (a *Actor) run(seq int64, current game.State) {
	for {
		select {
		case request := <-a.moves:
			if _, _, err := a.syncFromControl(request.ctx, &seq, &current); err != nil {
				request.reply <- moveResult{err: fmt.Errorf("synchronize actor: %w", err)}
				continue
			}
			next, err := current.Apply(request.move)
			if err != nil {
				request.reply <- moveResult{err: err}
				continue
			}
			payload, err := json.Marshal(next)
			if err != nil {
				request.reply <- moveResult{err: fmt.Errorf("encode snapshot: %w", err)}
				continue
			}
			attemptID, err := newAttemptID()
			if err != nil {
				request.reply <- moveResult{err: err}
				continue
			}
			key := fmt.Sprintf("game/%s/epoch/%d/seq/%d/attempt/%s", a.lease.GameID, a.lease.Epoch, seq+1, attemptID)
			if err := a.store.Put(request.ctx, key, payload); err != nil {
				request.reply <- moveResult{err: fmt.Errorf("put snapshot: %w", err)}
				continue
			}
			record, err := a.control.Commit(request.ctx, a.lease, seq, key)
			if err != nil {
				reconcileCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				reconciled, advanced, reconcileErr := a.syncFromControl(reconcileCtx, &seq, &current)
				cancel()
				if reconcileErr == nil && advanced {
					if reconciled.SnapshotKey == key {
						request.reply <- moveResult{state: current, record: reconciled}
						continue
					}
					request.reply <- moveResult{err: fmt.Errorf("actor reconciled to committed seq %d; retry move: %w", seq, err)}
					continue
				}
				request.reply <- moveResult{err: fmt.Errorf("commit move: %w", err)}
				continue
			}
			seq = record.SnapshotSeq
			current = next
			request.reply <- moveResult{state: current, record: record}
		case reply := <-a.states:
			reply <- current
		case <-a.stop:
			return
		}
	}
}

func (a *Actor) syncFromControl(ctx context.Context, seq *int64, current *game.State) (control.Record, bool, error) {
	record, err := a.control.Get(ctx, a.lease.GameID)
	if err != nil {
		return control.Record{}, false, err
	}
	if record.OwnerID != a.lease.WorkerID || record.Epoch != a.lease.Epoch {
		return record, false, control.ErrFenced
	}
	if record.SnapshotSeq <= *seq {
		return record, false, nil
	}
	payload, err := a.store.Get(ctx, record.SnapshotKey)
	if err != nil {
		return record, false, fmt.Errorf("get committed snapshot: %w", err)
	}
	var restored game.State
	if err := json.Unmarshal(payload, &restored); err != nil {
		return record, false, fmt.Errorf("decode committed snapshot: %w", err)
	}
	*seq = record.SnapshotSeq
	*current = restored
	return record, true, nil
}

func newAttemptID() (string, error) {
	value, err := id.RandomHex(12)
	if err != nil {
		return "", fmt.Errorf("generate snapshot attempt id: %w", err)
	}
	return value, nil
}

func (a *Actor) Move(ctx context.Context, uci string) (game.State, control.Record, error) {
	reply := make(chan moveResult, 1)
	select {
	case a.moves <- moveRequest{ctx: ctx, move: uci, reply: reply}:
	case <-a.stop:
		return game.State{}, control.Record{}, ErrActorClosed
	case <-ctx.Done():
		return game.State{}, control.Record{}, ctx.Err()
	}
	select {
	case result := <-reply:
		return result.state, result.record, result.err
	case <-a.stop:
		return game.State{}, control.Record{}, ErrActorClosed
	case <-ctx.Done():
		return game.State{}, control.Record{}, ctx.Err()
	}
}

func (a *Actor) State() game.State {
	reply := make(chan game.State, 1)
	a.states <- reply
	return <-reply
}

func (a *Actor) Lease() control.Lease { return a.lease }

func (a *Actor) Close() {
	select {
	case <-a.stop:
	default:
		close(a.stop)
	}
}
