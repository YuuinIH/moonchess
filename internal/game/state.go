package game

import (
	"fmt"
	"time"

	chess "github.com/notnil/chess"
)

// State is the complete immutable payload stored in Mooncake.
type State struct {
	FEN    string   `json:"fen"`
	Turn   string   `json:"turn"`
	Moves  []string `json:"moves"`
	Status string   `json:"status"`
	Reason string   `json:"reason,omitempty"`
	Clock  *Clock   `json:"clock,omitempty"`
}

// Clock stores remaining time at the last committed transition. Only the side
// to move spends time after TurnStartedUnixMS; ownership changes never reset it.
type Clock struct {
	WhiteMS           int64 `json:"whiteMs"`
	BlackMS           int64 `json:"blackMs"`
	IncrementMS       int64 `json:"incrementMs"`
	TurnStartedUnixMS int64 `json:"turnStartedUnixMs"`
}

func NewTimedState(initial, increment time.Duration, now time.Time) State {
	s := NewState()
	s.Clock = &Clock{WhiteMS: initial.Milliseconds(), BlackMS: initial.Milliseconds(), IncrementMS: increment.Milliseconds(), TurnStartedUnixMS: now.UnixMilli()}
	return s
}

func (s State) clockAt(now time.Time) *Clock {
	if s.Clock == nil {
		return nil
	}
	c := *s.Clock
	if s.Status == "active" && c.TurnStartedUnixMS > 0 {
		elapsed := max(int64(0), now.UnixMilli()-c.TurnStartedUnixMS)
		if s.Turn == "white" {
			c.WhiteMS = max(int64(0), c.WhiteMS-elapsed)
		} else {
			c.BlackMS = max(int64(0), c.BlackMS-elapsed)
		}
	}
	return &c
}

func (s State) Expired(now time.Time) bool {
	c := s.clockAt(now)
	return c != nil && s.Status == "active" && (s.Turn == "white" && c.WhiteMS == 0 || s.Turn == "black" && c.BlackMS == 0)
}

// MoveAt charges the current player and adds increment only for a legal move.
func (s State) MoveAt(uci string, now time.Time) (State, error) {
	if s.Expired(now) {
		return State{}, fmt.Errorf("clock expired")
	}
	next, err := s.Apply(uci)
	if err != nil {
		return State{}, err
	}
	next.Clock = s.clockAt(now)
	if next.Clock != nil {
		if s.Turn == "white" {
			next.Clock.WhiteMS += next.Clock.IncrementMS
		} else {
			next.Clock.BlackMS += next.Clock.IncrementMS
		}
		next.Clock.TurnStartedUnixMS = now.UnixMilli()
		if next.Status != "active" {
			next.Clock.TurnStartedUnixMS = 0
		}
	}
	return next, nil
}

// Finish leaves the position/history intact; resignation does not require a turn.
func (s State) Finish(loser, reason string) (State, error) {
	if s.Status != "active" || loser != "white" && loser != "black" {
		return State{}, fmt.Errorf("invalid game termination")
	}
	if reason != "resignation" && reason != "abandonment" && reason != "timeout" {
		return State{}, fmt.Errorf("invalid termination reason")
	}
	s.Status = "0-1"
	if loser == "black" {
		s.Status = "1-0"
	}
	// A bare king cannot win even by a cooperative sequence of legal moves.
	option, err := chess.FEN(s.FEN)
	if err != nil {
		return State{}, err
	}
	winner := chess.Black
	if loser == "black" {
		winner = chess.White
	}
	canWin := false
	for _, piece := range chess.NewGame(option).Position().Board().SquareMap() {
		if piece.Color() == winner && piece.Type() != chess.King {
			canWin = true
		}
	}
	if !canWin {
		s.Status = "1/2-1/2"
	}
	s.Reason = reason
	if s.Clock != nil {
		c := *s.Clock
		c.TurnStartedUnixMS = 0
		s.Clock = &c
	}
	return s, nil
}

func (s State) FinishAt(loser, reason string, now time.Time) (State, error) {
	s.Clock = s.clockAt(now)
	return s.Finish(loser, reason)
}

func NewState() State {
	g := chess.NewGame()
	return fromGame(g, nil)
}

// Apply validates a UCI move against the position and returns a new value.
func (s State) Apply(uci string) (State, error) {
	if s.Status != "active" {
		return State{}, fmt.Errorf("game is finished")
	}
	option, err := chess.FEN(s.FEN)
	if err != nil {
		return State{}, fmt.Errorf("decode FEN: %w", err)
	}
	g := chess.NewGame(option)
	move, err := (chess.UCINotation{}).Decode(g.Position(), uci)
	if err != nil {
		return State{}, fmt.Errorf("decode UCI move: %w", err)
	}
	if err := g.Move(move); err != nil {
		return State{}, fmt.Errorf("illegal move: %w", err)
	}
	moves := append(append([]string(nil), s.Moves...), uci)
	next := fromGame(g, moves)
	next.Clock = s.Clock
	return next, nil
}

func fromGame(g *chess.Game, moves []string) State {
	turn := "white"
	if g.Position().Turn() == chess.Black {
		turn = "black"
	}
	status := "active"
	if g.Outcome() != chess.NoOutcome {
		status = g.Outcome().String()
	}
	reason := ""
	if status != "active" {
		reason = "board outcome"
		if g.Method() == chess.Checkmate {
			reason = "checkmate"
		}
	}
	return State{FEN: g.FEN(), Turn: turn, Moves: moves, Status: status, Reason: reason}
}
