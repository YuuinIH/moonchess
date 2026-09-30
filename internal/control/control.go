package control

import (
	"context"
	"errors"
)

var (
	ErrNotFound = errors.New("game not found")
	ErrBusy     = errors.New("game is owned by a live worker")
	ErrFenced   = errors.New("worker lease or canonical head changed")
)

type Record struct {
	GameID            string `json:"gameId"`
	OwnerID           string `json:"ownerId"`
	Epoch             int64  `json:"epoch"`
	CommittedSeq      int64  `json:"committedSeq"`
	HeadRef           string `json:"headRef"`
	CheckpointRef     string `json:"checkpointRef"`
	CheckpointSeq     int64  `json:"checkpointSeq"`
	FallbackRef       string `json:"fallbackRef,omitempty"`
	FallbackSeq       int64  `json:"fallbackSeq,omitempty"`
	TargetID          string `json:"targetId,omitempty"`
	TargetUntilUnixMS int64  `json:"targetUntilUnixMs,omitempty"`
	Phase             string `json:"phase"`
	Revision          int64  `json:"-"`
}

type Lease struct {
	GameID   string
	WorkerID string
	Epoch    int64
	ID       int64
}

type Progress struct {
	Seq     int64  `json:"seq"`
	HeadRef string `json:"headRef"`
}

type Plane interface {
	Create(context.Context, string, string) (Record, error)
	Get(context.Context, string) (Record, error)
	List(context.Context) ([]Record, error)
	Acquire(context.Context, Record, string, int64) (Lease, error)
	Renew(context.Context, Lease) error
	Commit(context.Context, Lease, Record, string) (Record, error)
	Checkpoint(context.Context, Lease, Record, string) (Record, error)
	SetProgress(context.Context, string, string, Progress) error
	GetProgress(context.Context, string, string) (Progress, error)
	BeginMigration(context.Context, Lease, Record, string) (Record, error)
	CancelMigration(context.Context, Lease, Record) (Record, error)
	Release(context.Context, Lease) error
}
