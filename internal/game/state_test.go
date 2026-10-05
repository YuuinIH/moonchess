package game_test

import (
	"testing"
	"time"

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

func TestClockChargesOnlyMovingSideAndAddsIncrement(t *testing.T) {
	now := time.Unix(1000, 0)
	s := game.NewTimedState(5*time.Minute, 3*time.Second, now)
	white, err := s.MoveAt("e2e4", now.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if white.Clock.WhiteMS != 293000 || white.Clock.BlackMS != 300000 || white.Clock.TurnStartedUnixMS != now.Add(10*time.Second).UnixMilli() {
		t.Fatal(white.Clock)
	}
	black, err := white.MoveAt("e7e5", now.Add(17*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if black.Clock.WhiteMS != 293000 || black.Clock.BlackMS != 296000 {
		t.Fatal(black.Clock)
	}
	if s.Clock.WhiteMS != 300000 || white.Clock.BlackMS != 300000 {
		t.Fatal("input clocks were mutated")
	}
	if _, err = black.MoveAt("e4e7", now.Add(18*time.Second)); err == nil {
		t.Fatal("illegal move accepted")
	}
	if black.Clock.WhiteMS != 293000 {
		t.Fatal("illegal move changed clock")
	}
	if black.Expired(now.Add(310*time.Second)) == false {
		t.Fatal("white clock did not expire")
	}
	if _, err = black.MoveAt("g1f3", now.Add(310*time.Second)); err == nil {
		t.Fatal("late move accepted")
	}
}

func TestFinishPreservesBoardHistoryAndStopsClock(t *testing.T) {
	now := time.Unix(1000, 0)
	s := game.NewTimedState(5*time.Minute, 3*time.Second, now)
	s, _ = s.MoveAt("e2e4", now.Add(time.Second))
	for _, reason := range []string{"resignation", "abandonment", "timeout"} {
		finished, err := s.FinishAt("white", reason, now.Add(20*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if finished.Status != "0-1" || finished.Reason != reason || finished.FEN != s.FEN || len(finished.Moves) != 1 || finished.Clock.TurnStartedUnixMS != 0 || finished.Clock.BlackMS != 281000 {
			t.Fatal(finished)
		}
		if finished.Expired(now.Add(time.Hour)) {
			t.Fatal("finished clock is running")
		}
		if _, err = finished.Apply("e7e5"); err == nil {
			t.Fatal("finished game accepted a move")
		}
	}
	if s.Clock.BlackMS != 300000 {
		t.Fatal("finish mutated source clock")
	}
}

func TestBareKingCannotWinOnTimeout(t *testing.T) {
	s := game.State{FEN: "7k/8/8/8/8/8/P7/K7 w - - 0 1", Turn: "white", Status: "active"}
	finished, err := s.Finish("white", "timeout")
	if err != nil || finished.Status != "1/2-1/2" {
		t.Fatalf("%+v %v", finished, err)
	}
}
