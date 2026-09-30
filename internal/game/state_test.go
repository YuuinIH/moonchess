package game_test

import (
	"testing"

	"github.com/yuuinih/moonchess/internal/game"
)

func TestStateAppliesLegalUCIMove(t *testing.T) {
	state := game.NewState()

	next, err := state.Apply("e2e4")
	if err != nil {
		t.Fatalf("apply e2e4: %v", err)
	}
	if next.Turn != "black" {
		t.Fatalf("turn = %q, want black", next.Turn)
	}
	if len(next.Moves) != 1 || next.Moves[0] != "e2e4" {
		t.Fatalf("moves = %#v, want [e2e4]", next.Moves)
	}
}

func TestStateRejectsIllegalMove(t *testing.T) {
	state := game.NewState()

	if _, err := state.Apply("e2e5"); err == nil {
		t.Fatal("e2e5 unexpectedly accepted")
	}
}
