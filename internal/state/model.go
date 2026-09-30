package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/yuuinih/moonchess/internal/game"
)

type Delta struct {
	Seq          int64  `json:"seq"`
	Prev         string `json:"prev"`
	Move         string `json:"move"`
	AttemptID    string `json:"attemptId"`
	WhiteClockMS int64  `json:"whiteClockMs,omitempty"`
	BlackClockMS int64  `json:"blackClockMs,omitempty"`
}

type Checkpoint struct {
	Seq                   int64      `json:"seq"`
	Head                  string     `json:"head"`
	State                 game.State `json:"state"`
	PreviousCheckpointRef string     `json:"previousCheckpointRef,omitempty"`
	CoveredRefs           []string   `json:"coveredRefs,omitempty"`
}

func Ref(kind, gameID string, seq int64, payload []byte) string {
	hash := sha256.Sum256(payload)
	return fmt.Sprintf("%s/%s/%d/%s", kind, gameID, seq, hex.EncodeToString(hash[:]))
}

func verifyRef(ref string, payload []byte) error {
	sum := sha256.Sum256(payload)
	if !strings.HasSuffix(ref, "/"+hex.EncodeToString(sum[:])) {
		return errors.New("Mooncake object digest mismatch")
	}
	return nil
}

type Materialized struct {
	Seq   int64
	Head  string
	State game.State
}
type Metrics struct {
	BytesMoved        int64  `json:"bytesMoved"`
	ReplayCount       int    `json:"replayCount"`
	Path              string `json:"path"`
	RecoveryLatencyMS int64  `json:"recoveryLatencyMs"`
	ObservedPauseMS   int64  `json:"observedPauseMs,omitempty"`
}

// Materialize walks immutable prev links back to a trusted local head or a checkpoint.
// The caller supplies only the canonical head and checkpoint from etcd.
func Materialize(ctx context.Context, store Store, seq int64, head string, checkpointRef string, checkpointSeq int64, local *Materialized) (Materialized, Metrics, error) {
	if local != nil && local.Seq == seq && local.Head == head {
		return *local, Metrics{Path: "hot"}, nil
	}
	var suffix []Delta
	ref := head
	metrics := Metrics{Path: "cold"}
	var base Materialized
	var checkpoint Checkpoint
	checkpointLoaded := false
	for {
		if local != nil && ref == local.Head && seq-int64(len(suffix)) == local.Seq {
			base = *local
			metrics.Path = "warm"
			break
		}
		if seq-int64(len(suffix)) == checkpointSeq {
			if !checkpointLoaded {
				bytes, err := store.Get(ctx, checkpointRef)
				if err != nil {
					return Materialized{}, metrics, err
				}
				metrics.BytesMoved += int64(len(bytes))
				if err := verifyRef(checkpointRef, bytes); err != nil {
					return Materialized{}, metrics, err
				}
				if err := json.Unmarshal(bytes, &checkpoint); err != nil {
					return Materialized{}, metrics, err
				}
				checkpointLoaded = true
			}
			if checkpoint.Seq != checkpointSeq || ref != checkpoint.Head && !(checkpointSeq == 0 && ref == checkpointRef) {
				return Materialized{}, metrics, errors.New("checkpoint head mismatch")
			}
			base = Materialized{Seq: checkpoint.Seq, Head: ref, State: checkpoint.State}
			break
		}
		if int64(len(suffix)) >= seq-checkpointSeq {
			return Materialized{}, metrics, errors.New("delta chain does not reach checkpoint")
		}
		bytes, err := store.Get(ctx, ref)
		if err != nil {
			return Materialized{}, metrics, err
		}
		metrics.BytesMoved += int64(len(bytes))
		if err := verifyRef(ref, bytes); err != nil {
			return Materialized{}, metrics, err
		}
		var delta Delta
		if err := json.Unmarshal(bytes, &delta); err != nil {
			return Materialized{}, metrics, err
		}
		if delta.Seq != seq-int64(len(suffix)) || delta.Prev == "" {
			return Materialized{}, metrics, errors.New("invalid delta chain")
		}
		suffix = append(suffix, delta)
		ref = delta.Prev
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		next, err := base.State.Apply(suffix[i].Move)
		if err != nil {
			return Materialized{}, metrics, fmt.Errorf("replay delta %d: %w", suffix[i].Seq, err)
		}
		base.State = next
		base.Seq = suffix[i].Seq
		metrics.ReplayCount++
	}
	base.Head = head
	return base, metrics, nil
}

// MaterializeWithFallback uses the previous complete checkpoint if the latest
// checkpoint cannot be read or verified. The canonical head is unchanged.
func MaterializeWithFallback(ctx context.Context, store Store, seq int64, head, checkpointRef string, checkpointSeq int64, fallbackRef string, fallbackSeq int64, local *Materialized) (Materialized, Metrics, error) {
	m, metrics, err := Materialize(ctx, store, seq, head, checkpointRef, checkpointSeq, local)
	if err == nil || fallbackRef == "" {
		return m, metrics, err
	}
	fallback, other, fallbackErr := Materialize(ctx, store, seq, head, fallbackRef, fallbackSeq, local)
	other.BytesMoved += metrics.BytesMoved
	if fallbackErr != nil {
		return Materialized{}, other, fmt.Errorf("latest checkpoint: %v; fallback: %w", err, fallbackErr)
	}
	return fallback, other, nil
}
