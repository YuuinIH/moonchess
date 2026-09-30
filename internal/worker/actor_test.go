package worker_test

import (
	"context"
	"errors"
	"strings"
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

type rejectingCommitter struct {
	called   bool
	getCalls int
}

func (c *rejectingCommitter) Commit(context.Context, control.Lease, int64, string) (control.Record, error) {
	c.called = true
	return control.Record{}, control.ErrFenced
}
func (c *rejectingCommitter) Get(context.Context, string) (control.Record, error) {
	c.getCalls++
	if c.getCalls == 1 {
		return control.Record{OwnerID: "worker-a", Epoch: 4, SnapshotSeq: 7}, nil
	}
	return control.Record{OwnerID: "worker-b", Epoch: 5}, nil
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
	if len(store.keys) != 1 || !strings.HasPrefix(store.keys[0], "game/g1/epoch/4/seq/8/attempt/") {
		t.Fatalf("put keys = %#v", store.keys)
	}
	if got := actor.State(); got.Turn != "white" || len(got.Moves) != 0 {
		t.Fatalf("actor advanced after failed CAS: %#v", got)
	}
}

type ambiguousCommitter struct {
	record       control.Record
	cancelCommit context.CancelFunc
}

func (c *ambiguousCommitter) Commit(_ context.Context, lease control.Lease, previousSeq int64, key string) (control.Record, error) {
	c.record = control.Record{GameID: lease.GameID, OwnerID: lease.WorkerID, Epoch: lease.Epoch, SnapshotSeq: previousSeq + 1, SnapshotKey: key}
	if c.cancelCommit != nil {
		c.cancelCommit()
	}
	return control.Record{}, errors.New("connection lost after commit")
}
func (c *ambiguousCommitter) Get(ctx context.Context, _ string) (control.Record, error) {
	if err := ctx.Err(); err != nil {
		return control.Record{}, err
	}
	return c.record, nil
}

type snapshotStore struct {
	values map[string][]byte
}

func (s *snapshotStore) Put(_ context.Context, key string, value []byte) error {
	s.values[key] = append([]byte(nil), value...)
	return nil
}
func (s *snapshotStore) Get(_ context.Context, key string) ([]byte, error) {
	return s.values[key], nil
}

func TestActorReconcilesCommitWhoseResponseWasLost(t *testing.T) {
	store := &snapshotStore{values: make(map[string][]byte)}
	lease := control.Lease{GameID: "g1", WorkerID: "worker-a", Epoch: 2}
	requestCtx, cancel := context.WithCancel(context.Background())
	committer := &ambiguousCommitter{
		record:       control.Record{GameID: "g1", OwnerID: "worker-a", Epoch: 2, SnapshotSeq: 0},
		cancelCommit: cancel,
	}
	actor := worker.NewActor(lease, 0, game.NewState(), store, committer)
	defer actor.Close()

	_, _, _ = actor.Move(requestCtx, "e2e4")
	if snapshot := actor.State(); snapshot.Turn != "black" || len(snapshot.Moves) != 1 {
		t.Fatalf("actor did not reconcile committed state: %#v", snapshot)
	}
}

func TestClosedActorRejectsMove(t *testing.T) {
	actor := worker.NewActor(control.Lease{GameID: "g1", WorkerID: "worker-a", Epoch: 4}, 7, game.NewState(), &recordingStore{}, &rejectingCommitter{})
	actor.Close()

	_, _, err := actor.Move(context.Background(), "e2e4")
	if !errors.Is(err, worker.ErrActorClosed) {
		t.Fatalf("move error = %v, want ErrActorClosed", err)
	}
}
