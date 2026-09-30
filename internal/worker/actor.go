package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/yuuinih/moonchess/internal/control"
	"github.com/yuuinih/moonchess/internal/game"
	"github.com/yuuinih/moonchess/internal/state"
)

type Committer interface {
	Commit(context.Context, control.Lease, int64, string) (control.Record, error)
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
	lease     control.Lease
	store     state.Store
	committer Committer
	moves     chan moveRequest
	states    chan chan game.State
	stop      chan struct{}
}

func NewActor(lease control.Lease, seq int64, initial game.State, store state.Store, committer Committer) *Actor {
	a := &Actor{
		lease: lease, store: store, committer: committer,
		moves: make(chan moveRequest), states: make(chan chan game.State), stop: make(chan struct{}),
	}
	go a.run(seq, initial)
	return a
}

func (a *Actor) run(seq int64, current game.State) {
	for {
		select {
		case request := <-a.moves:
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
			key := fmt.Sprintf("game/%s/epoch/%d/seq/%d", a.lease.GameID, a.lease.Epoch, seq+1)
			if err := a.store.Put(request.ctx, key, payload); err != nil {
				request.reply <- moveResult{err: fmt.Errorf("put snapshot: %w", err)}
				continue
			}
			record, err := a.committer.Commit(request.ctx, a.lease, seq, key)
			if err != nil {
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

func (a *Actor) Move(ctx context.Context, uci string) (game.State, control.Record, error) {
	reply := make(chan moveResult, 1)
	select {
	case a.moves <- moveRequest{ctx: ctx, move: uci, reply: reply}:
	case <-ctx.Done():
		return game.State{}, control.Record{}, ctx.Err()
	}
	select {
	case result := <-reply:
		return result.state, result.record, result.err
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
