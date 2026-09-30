package control

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("game not found")
	ErrBusy     = errors.New("game is owned by a live worker")
	ErrFenced   = errors.New("worker lease is stale")
)

type Record struct {
	GameID      string    `json:"gameId"`
	OwnerID     string    `json:"ownerId"`
	Epoch       int64     `json:"epoch"`
	LeaseUntil  time.Time `json:"leaseUntil"`
	SnapshotSeq int64     `json:"snapshotSeq"`
	SnapshotKey string    `json:"snapshotKey"`
}

type Lease struct {
	GameID     string
	WorkerID   string
	Epoch      int64
	ValidUntil time.Time
}

type Plane interface {
	Create(context.Context, string, string) (Record, error)
	Get(context.Context, string) (Record, error)
	List(context.Context) ([]Record, error)
	Acquire(context.Context, string, string, time.Duration) (Lease, error)
	Renew(context.Context, Lease, time.Duration) (Lease, error)
	Commit(context.Context, Lease, int64, string) (Record, error)
}
