package game

import (
	"fmt"

	chess "github.com/notnil/chess"
)

// State is the complete immutable payload stored in Mooncake.
type State struct {
	FEN    string   `json:"fen"`
	Turn   string   `json:"turn"`
	Moves  []string `json:"moves"`
	Status string   `json:"status"`
}

func NewState() State {
	g := chess.NewGame()
	return fromGame(g, nil)
}

// Apply validates a UCI move against the position and returns a new value.
func (s State) Apply(uci string) (State, error) {
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
	return fromGame(g, moves), nil
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
	return State{FEN: g.FEN(), Turn: turn, Moves: moves, Status: status}
}
