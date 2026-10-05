package worker_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuuinih/moonchess/internal/control"
	"github.com/yuuinih/moonchess/internal/game"
	"github.com/yuuinih/moonchess/internal/state"
	"github.com/yuuinih/moonchess/internal/worker"
)

type memoryStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (s *memoryStore) Put(_ context.Context, k string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[k] = append([]byte(nil), v...)
	return nil
}
func (s *memoryStore) Get(_ context.Context, k string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[k]
	if !ok {
		return nil, state.ErrNotFound
	}
	return append([]byte(nil), v...), nil
}
func (s *memoryStore) Remove(_ context.Context, k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, k)
	return nil
}

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
	s.mu.Lock()
	objectCount := len(s.data)
	s.mu.Unlock()
	if p.commits != 1 || objectCount != 1 {
		t.Fatalf("commits=%d objects=%d", p.commits, objectCount)
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
	var cpCount, deltaCount int
	deadline := time.Now().Add(20 * time.Second)
	for {
		cpCount, deltaCount = 0, 0
		s.mu.Lock()
		_, initialRetained := s.data[initial]
		for ref := range s.data {
			if strings.HasPrefix(ref, "checkpoint/") {
				cpCount++
			}
			if strings.HasPrefix(ref, "delta/") {
				deltaCount++
			}
		}
		s.mu.Unlock()
		if !initialRetained && cpCount == 2 && deltaCount == 8 {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if cpCount != 2 || deltaCount != 8 {
		t.Fatalf("objects: checkpoints=%d deltas=%d", cpCount, deltaCount)
	}
	materialized, _, err := state.Materialize(ctx, s, p.record.CommittedSeq, p.record.HeadRef, p.record.CheckpointRef, p.record.CheckpointSeq, nil)
	if err != nil || len(materialized.State.Moves) != 16 {
		t.Fatalf("latest checkpoint recovery: moves=%d error=%v", len(materialized.State.Moves), err)
	}
	_ = s.Remove(ctx, p.record.CheckpointRef)
	materialized, _, err = state.MaterializeWithFallback(ctx, s, p.record.CommittedSeq, p.record.HeadRef, p.record.CheckpointRef, p.record.CheckpointSeq, p.record.FallbackRef, p.record.FallbackSeq, nil)
	if err != nil || len(materialized.State.Moves) != 16 {
		t.Fatalf("fallback recovery: moves=%d error=%v", len(materialized.State.Moves), err)
	}
}

func TestPlayerAndTurnAuthorization(t *testing.T) {
	ctx := context.Background()
	s := &memoryStore{data: map[string][]byte{}}
	p := &acceptingPlane{record: control.Record{GameID: "players", OwnerID: "worker-a", Epoch: 1, HeadRef: "cp", WhiteClientID: "white-client", BlackClientID: "black-client"}}
	a := worker.NewActor(control.Lease{GameID: "players", WorkerID: "worker-a", Epoch: 1}, p.record, state.Materialized{Head: "cp", State: game.NewState()}, s, p)
	for _, client := range []string{"", "outsider", "black-client"} {
		if _, _, err := a.Move(ctx, "e2e4", client); !errors.Is(err, control.ErrUnauthorized) {
			t.Fatalf("client %q: %v", client, err)
		}
	}
	if len(s.data) != 0 {
		t.Fatal("unauthorized commands wrote payloads")
	}
	if _, _, err := a.Move(ctx, "e2e4", "white-client"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Move(ctx, "e7e5", "white-client"); !errors.Is(err, control.ErrUnauthorized) {
		t.Fatal(err)
	}
	if _, _, err := a.Move(ctx, "e7e5", "black-client"); err != nil {
		t.Fatal(err)
	}
	if p.record.CommittedSeq != 2 {
		t.Fatal(p.record)
	}
}

func TestConcurrentWhiteCommandsCannotPlayBothTurns(t *testing.T) {
	ctx := context.Background()
	s := &memoryStore{data: map[string][]byte{}}
	p := &acceptingPlane{record: control.Record{GameID: "race", OwnerID: "worker-a", Epoch: 1, HeadRef: "cp", WhiteClientID: "white", BlackClientID: "black"}}
	a := worker.NewActor(control.Lease{GameID: "race", WorkerID: "worker-a", Epoch: 1}, p.record, state.Materialized{Head: "cp", State: game.NewState()}, s, p)
	results := make(chan error, 2)
	for _, move := range []string{"e2e4", "d2d4"} {
		go func(move string) { _, _, err := a.Move(ctx, move, "white"); results <- err }(move)
	}
	won, denied := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			won++
		} else if errors.Is(err, control.ErrUnauthorized) {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if won != 1 || denied != 1 || p.record.CommittedSeq != 1 || len(s.data) != 1 {
		t.Fatalf("won=%d denied=%d record=%+v payloads=%d", won, denied, p.record, len(s.data))
	}
}

func timedActor(t *testing.T, initial game.State) (*worker.Actor, *acceptingPlane, *memoryStore) {
	t.Helper()
	s := &memoryStore{data: map[string][]byte{}}
	cp, _ := json.Marshal(state.Checkpoint{State: initial})
	ref := state.Ref("checkpoint", "timed", 0, cp)
	s.data[ref] = cp
	p := &acceptingPlane{record: control.Record{GameID: "timed", OwnerID: "a", Epoch: 1, HeadRef: ref, CheckpointRef: ref, WhiteClientID: "white", BlackClientID: "black", Revision: 1}}
	a := worker.NewActor(control.Lease{GameID: "timed", WorkerID: "a", Epoch: 1}, p.record, state.Materialized{Head: ref, State: initial}, s, p)
	return a, p, s
}

func TestFinishEitherTurnAuthorizedAndRecoverable(t *testing.T) {
	for _, reason := range []string{"resignation", "abandonment"} {
		t.Run(reason, func(t *testing.T) {
			ctx := context.Background()
			a, p, s := timedActor(t, game.NewTimedState(time.Minute, 3*time.Second, time.Now()))
			if _, _, err := a.Finish(ctx, reason, "outsider"); !errors.Is(err, control.ErrUnauthorized) {
				t.Fatal(err)
			}
			if _, _, err := a.Finish(ctx, "timeout", "white"); err == nil {
				t.Fatal("client can force timeout")
			}
			if len(s.data) != 1 {
				t.Fatal("unauthorized command wrote payload")
			}
			finished, record, err := a.Finish(ctx, reason, "black") // black resigns during white's turn
			if err != nil || finished.Status != "1-0" || finished.Reason != reason || record.CommittedSeq != 1 || len(finished.Moves) != 0 {
				t.Fatalf("%+v %+v %v", finished, record, err)
			}
			if _, _, err = a.Finish(ctx, reason, "black"); err != nil {
				t.Fatal(err)
			}
			if p.record.CommittedSeq != 1 {
				t.Fatal("retry advanced seq")
			}
			m, _, err := state.Materialize(ctx, s, record.CommittedSeq, record.HeadRef, record.CheckpointRef, record.CheckpointSeq, nil)
			if err != nil || m.State.Status != finished.Status || m.State.Reason != reason || m.State.Clock.TurnStartedUnixMS != 0 {
				t.Fatalf("recovery %+v %v", m, err)
			}
			if _, _, err = a.Move(ctx, "e2e4", "white"); err == nil {
				t.Fatal("finished room accepted a move")
			}
		})
	}
}

func TestTimeoutSurvivesTakeoverAndPreventsLateMove(t *testing.T) {
	ctx := context.Background()
	for _, command := range []string{"expire", "move", "resign"} {
		t.Run(command, func(t *testing.T) {
			a, p, s := timedActor(t, game.NewTimedState(time.Second, 3*time.Second, time.Now().Add(-2*time.Second)))
			// A restarted/takeover actor materializes the persisted anchor; no clock reset.
			m, _, err := state.Materialize(ctx, s, 0, p.record.HeadRef, p.record.CheckpointRef, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			p.record.OwnerID, p.record.Epoch = "b", 2
			if _, _, err = a.Expire(ctx); !errors.Is(err, control.ErrFenced) {
				t.Fatal(err)
			}
			if len(s.data) != 1 {
				t.Fatal("stale actor wrote timeout")
			}
			a = worker.NewActor(control.Lease{GameID: "timed", WorkerID: "b", Epoch: 2}, p.record, m, s, p)
			var finished game.State
			var record control.Record
			switch command {
			case "expire":
				finished, record, err = a.Expire(ctx)
			case "move":
				finished, record, err = a.Move(ctx, "e2e4", "white")
			case "resign":
				finished, record, err = a.Finish(ctx, "resignation", "black")
			}
			if err != nil || finished.Status != "0-1" || finished.Reason != "timeout" || record.CommittedSeq != 1 || len(finished.Moves) != 0 || finished.Clock.WhiteMS != 0 {
				t.Fatalf("%+v %+v %v", finished, record, err)
			}
			if _, _, err = a.Expire(ctx); err != nil {
				t.Fatal(err)
			}
			if p.record.CommittedSeq != 1 {
				t.Fatal("duplicate timeout")
			}
		})
	}
}

func TestConcurrentMoveAndResignationProduceOneFinalResult(t *testing.T) {
	ctx := context.Background()
	a, p, s := timedActor(t, game.NewTimedState(time.Minute, 0, time.Now()))
	results := make(chan error, 2)
	go func() { _, _, err := a.Move(ctx, "e2e4", "white"); results <- err }()
	go func() { _, _, err := a.Finish(ctx, "resignation", "white"); results <- err }()
	<-results
	<-results
	m, _, err := state.Materialize(ctx, s, p.record.CommittedSeq, p.record.HeadRef, p.record.CheckpointRef, 0, nil)
	if err != nil || m.State.Status != "0-1" || m.State.Reason != "resignation" || p.record.CommittedSeq != int64(len(m.State.Moves))+1 {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestFinishOnCheckpointBoundaryRecoversFromFallback(t *testing.T) {
	ctx := context.Background()
	a, p, s := timedActor(t, game.NewTimedState(time.Minute, 0, time.Now()))
	for i, move := range []string{"e2e4", "e7e5", "g1f3", "b8c6", "f1c4", "g8f6", "d2d3"} {
		client := "white"
		if i%2 == 1 {
			client = "black"
		}
		if _, _, err := a.Move(ctx, move, client); err != nil {
			t.Fatal(err)
		}
	}
	finished, record, err := a.Finish(ctx, "abandonment", "black")
	if err != nil || record.CheckpointSeq != 8 || record.FallbackSeq != 0 {
		t.Fatalf("%+v %v", record, err)
	}
	for _, missingCheckpoint := range []bool{false, true} {
		if missingCheckpoint {
			s.Remove(ctx, p.record.CheckpointRef)
		}
		m, _, err := state.MaterializeWithFallback(ctx, s, record.CommittedSeq, record.HeadRef, record.CheckpointRef, record.CheckpointSeq, record.FallbackRef, record.FallbackSeq, nil)
		if err != nil || m.State.Status != finished.Status || m.State.Reason != "abandonment" || len(m.State.Moves) != 7 || m.State.Clock.TurnStartedUnixMS != 0 {
			t.Fatalf("missing=%v %+v %v", missingCheckpoint, m, err)
		}
	}
}

func TestFencedEndingCannotPublishResult(t *testing.T) {
	for _, reason := range []string{"resignation", "timeout"} {
		t.Run(reason, func(t *testing.T) {
			s := &memoryStore{data: map[string][]byte{}}
			r := control.Record{GameID: "ending-fenced", OwnerID: "a", Epoch: 1, HeadRef: "cp", WhiteClientID: "white", BlackClientID: "black"}
			p := &rejectedPlane{record: r}
			initial := game.NewTimedState(time.Second, 0, time.Now().Add(-2*time.Second))
			a := worker.NewActor(control.Lease{GameID: r.GameID, WorkerID: "a", Epoch: 1}, r, state.Materialized{Head: "cp", State: initial}, s, p)
			var err error
			if reason == "timeout" {
				_, _, err = a.Expire(context.Background())
			} else {
				_, _, err = a.Finish(context.Background(), reason, "white")
			}
			if !errors.Is(err, control.ErrFenced) || a.State().Status != "active" || p.commits != 1 || len(s.data) != 1 {
				t.Fatalf("err=%v state=%+v commits=%d", err, a.State(), p.commits)
			}
		})
	}
}
