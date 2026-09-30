package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/yuuinih/moonchess/internal/control"
	"github.com/yuuinih/moonchess/internal/game"
	"github.com/yuuinih/moonchess/internal/state"
	"github.com/yuuinih/moonchess/internal/worker"
)

type memoryStore struct{ data map[string][]byte }

func (s *memoryStore) Put(_ context.Context, k string, v []byte) error {
	s.data[k] = append([]byte(nil), v...)
	return nil
}
func (s *memoryStore) Get(_ context.Context, k string) ([]byte, error) {
	v, ok := s.data[k]
	if !ok {
		return nil, state.ErrNotFound
	}
	return append([]byte(nil), v...), nil
}
func (s *memoryStore) Remove(_ context.Context, k string) error { delete(s.data, k); return nil }

type rejectedPlane struct {
	control.Plane
	record  control.Record
	commits int
}

func (p *rejectedPlane) Get(context.Context, string) (control.Record, error) { return p.record, nil }
func (p *rejectedPlane) Commit(_ context.Context, _ control.Lease, _ control.Record, _ string) (control.Record, error) {
	p.commits++
	return control.Record{}, control.ErrFenced
}

func TestFencedMoveLeavesOnlyAnUncommittedDelta(t *testing.T) {
	s := &memoryStore{data: map[string][]byte{}}
	r := control.Record{GameID: "g1", OwnerID: "a", Epoch: 2, HeadRef: "cp", CheckpointRef: "cp"}
	p := &rejectedPlane{record: r}
	a := worker.NewActor(control.Lease{GameID: "g1", WorkerID: "a", Epoch: 2}, r, state.Materialized{Head: "cp", State: game.NewState()}, s, p)
	_, _, err := a.Move(context.Background(), "e2e4")
	if !errors.Is(err, control.ErrFenced) {
		t.Fatalf("move error = %v", err)
	}
	if p.commits != 1 || len(s.data) != 1 {
		t.Fatalf("commits=%d objects=%d", p.commits, len(s.data))
	}
	if got := a.State(); got.Turn != "white" || len(got.Moves) != 0 {
		t.Fatalf("actor advanced: %#v", got)
	}
}

type acceptingPlane struct {
	control.Plane
	record control.Record
}

func (p *acceptingPlane) Get(context.Context, string) (control.Record, error) { return p.record, nil }
func (p *acceptingPlane) Commit(_ context.Context, _ control.Lease, observed control.Record, ref string) (control.Record, error) {
	if observed.HeadRef != p.record.HeadRef {
		return control.Record{}, control.ErrFenced
	}
	p.record.CommittedSeq++
	p.record.HeadRef = ref
	p.record.Revision++
	return p.record, nil
}
func (p *acceptingPlane) Checkpoint(_ context.Context, _ control.Lease, observed control.Record, ref string) (control.Record, error) {
	if observed.Revision != p.record.Revision {
		return control.Record{}, control.ErrFenced
	}
	p.record.FallbackRef = p.record.CheckpointRef
	p.record.FallbackSeq = p.record.CheckpointSeq
	p.record.CheckpointRef = ref
	p.record.CheckpointSeq = p.record.CommittedSeq
	p.record.Revision++
	return p.record, nil
}
func (p *acceptingPlane) SetProgress(context.Context, string, string, control.Progress) error {
	return nil
}

func TestCheckpointKeepsLatestFallbackAndCollectsCoveredDeltas(t *testing.T) {
	ctx := context.Background()
	s := &memoryStore{data: map[string][]byte{}}
	cp, _ := json.Marshal(state.Checkpoint{Seq: 0, State: game.NewState()})
	initial := state.Ref("checkpoint", "g2", 0, cp)
	s.data[initial] = cp
	p := &acceptingPlane{record: control.Record{GameID: "g2", OwnerID: "a", Epoch: 1, HeadRef: initial, CheckpointRef: initial, Revision: 1}}
	a := worker.NewActor(control.Lease{GameID: "g2", WorkerID: "a", Epoch: 1}, p.record, state.Materialized{Head: initial, State: game.NewState()}, s, p)
	moves := []string{"e2e4", "e7e5", "g1f3", "b8c6", "f1c4", "g8f6", "d2d3", "f8c5", "c2c3", "d7d6", "b1d2", "c8g4", "h2h3", "g4h5", "a2a4", "a7a6"}
	for _, move := range moves {
		if _, _, err := a.Move(ctx, move); err != nil {
			t.Fatalf("move %s: %v", move, err)
		}
	}
	if p.record.CheckpointSeq != 16 || p.record.FallbackSeq != 8 {
		t.Fatalf("checkpoints: %#v", p.record)
	}
	if _, ok := s.data[initial]; ok {
		t.Fatal("obsolete initial checkpoint retained")
	}
	var cpCount, deltaCount int
	for ref := range s.data {
		if strings.HasPrefix(ref, "checkpoint/") {
			cpCount++
		}
		if strings.HasPrefix(ref, "delta/") {
			deltaCount++
		}
	}
	if cpCount != 2 || deltaCount != 8 {
		t.Fatalf("objects: checkpoints=%d deltas=%d", cpCount, deltaCount)
	}
	materialized, _, err := state.Materialize(ctx, s, p.record.CommittedSeq, p.record.HeadRef, p.record.CheckpointRef, p.record.CheckpointSeq, nil)
	if err != nil || len(materialized.State.Moves) != 16 {
		t.Fatalf("latest checkpoint recovery: moves=%d error=%v", len(materialized.State.Moves), err)
	}
	delete(s.data, p.record.CheckpointRef)
	materialized, _, err = state.MaterializeWithFallback(ctx, s, p.record.CommittedSeq, p.record.HeadRef, p.record.CheckpointRef, p.record.CheckpointSeq, p.record.FallbackRef, p.record.FallbackSeq, nil)
	if err != nil || len(materialized.State.Moves) != 16 {
		t.Fatalf("fallback recovery: moves=%d error=%v", len(materialized.State.Moves), err)
	}
}
