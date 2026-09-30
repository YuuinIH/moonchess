package state_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yuuinih/moonchess/internal/game"
	"github.com/yuuinih/moonchess/internal/state"
)

type objects map[string][]byte

func (s objects) Put(_ context.Context, k string, v []byte) error { s[k] = v; return nil }
func (s objects) Get(_ context.Context, k string) ([]byte, error) { return s[k], nil }
func (s objects) Remove(_ context.Context, k string) error        { delete(s, k); return nil }
func TestMaterializeColdWarmHot(t *testing.T) {
	ctx := context.Background()
	s := objects{}
	cp, _ := json.Marshal(state.Checkpoint{Seq: 0, State: game.NewState()})
	cpRef := state.Ref("checkpoint", "g", 0, cp)
	s[cpRef] = cp
	d1, _ := json.Marshal(state.Delta{Seq: 1, Prev: cpRef, Move: "e2e4"})
	h1 := state.Ref("delta", "g", 1, d1)
	s[h1] = d1
	d2, _ := json.Marshal(state.Delta{Seq: 2, Prev: h1, Move: "e7e5"})
	h2 := state.Ref("delta", "g", 2, d2)
	s[h2] = d2
	one, metrics, err := state.Materialize(ctx, s, 1, h1, cpRef, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Path != "cold" || metrics.ReplayCount != 1 || one.State.Turn != "black" {
		t.Fatalf("cold = %#v %#v", one, metrics)
	}
	two, metrics, err := state.Materialize(ctx, s, 2, h2, cpRef, 0, &one)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Path != "warm" || metrics.ReplayCount != 1 || len(two.State.Moves) != 2 {
		t.Fatalf("warm = %#v %#v", two, metrics)
	}
	_, metrics, err = state.Materialize(ctx, s, 2, h2, cpRef, 0, &two)
	if err != nil || metrics.Path != "hot" || metrics.BytesMoved != 0 {
		t.Fatalf("hot = %#v %v", metrics, err)
	}
}
