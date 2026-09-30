package worker_test

import (
	"context"
	"errors"
	"testing"

	"github.com/yuuinih/moonchess/internal/control"
	"github.com/yuuinih/moonchess/internal/game"
	"github.com/yuuinih/moonchess/internal/worker"
)

type recordingStore struct {
	keys []string
}

func (s *recordingStore) Put(_ context.Context, key string, _ []byte) error {
	s.keys = append(s.keys, key)
	return nil
}
func (s *recordingStore) Get(context.Context, string) ([]byte, error) { return nil, nil }

type rejectingCommitter struct{ called bool }

func (c *rejectingCommitter) Commit(context.Context, control.Lease, int64, string) (control.Record, error) {
	c.called = true
	return control.Record{}, control.ErrFenced
}

func TestActorLeavesOrphanAndDoesNotAdvanceWhenFenced(t *testing.T) {
	store := &recordingStore{}
	committer := &rejectingCommitter{}
	lease := control.Lease{GameID: "g1", WorkerID: "worker-a", Epoch: 4}
	actor := worker.NewActor(lease, 7, game.NewState(), store, committer)
	defer actor.Close()

	_, _, err := actor.Move(context.Background(), "e2e4")
	if !errors.Is(err, control.ErrFenced) {
		t.Fatalf("move error = %v, want ErrFenced", err)
	}
	if !committer.called {
		t.Fatal("commit was not attempted after the snapshot put")
	}
	if len(store.keys) != 1 || store.keys[0] != "game/g1/epoch/4/seq/8" {
		t.Fatalf("put keys = %#v", store.keys)
	}
	if got := actor.State(); got.Turn != "white" || len(got.Moves) != 0 {
		t.Fatalf("actor advanced after failed CAS: %#v", got)
	}
}
