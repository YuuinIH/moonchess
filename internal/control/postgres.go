package control

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct {
	pool *pgxpool.Pool
}

func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("configure postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) Migrate(ctx context.Context) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('moonchess-schema-v1'))`); err != nil {
		return fmt.Errorf("lock migration: %w", err)
	}
	_, err = tx.Exec(ctx, `
CREATE TABLE IF NOT EXISTS games (
  game_id text PRIMARY KEY,
  owner_id text,
  epoch bigint NOT NULL DEFAULT 0,
  lease_until timestamptz,
  snapshot_seq bigint NOT NULL DEFAULT 0,
  snapshot_key text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS games_lease_until_idx ON games (lease_until);
`)
	if err != nil {
		return fmt.Errorf("migrate control plane: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}

const recordColumns = `game_id, COALESCE(owner_id, ''), epoch, COALESCE(lease_until, to_timestamp(0)), snapshot_seq, snapshot_key`

type scanner interface{ Scan(...any) error }

func scanRecord(row scanner) (Record, error) {
	var r Record
	err := row.Scan(&r.GameID, &r.OwnerID, &r.Epoch, &r.LeaseUntil, &r.SnapshotSeq, &r.SnapshotKey)
	return r, err
}

func (p *Postgres) Create(ctx context.Context, gameID, snapshotKey string) (Record, error) {
	r, err := scanRecord(p.pool.QueryRow(ctx, `INSERT INTO games (game_id, snapshot_key)
VALUES ($1, $2) RETURNING `+recordColumns, gameID, snapshotKey))
	if err != nil {
		return Record{}, fmt.Errorf("create game: %w", err)
	}
	return r, nil
}

func (p *Postgres) Get(ctx context.Context, gameID string) (Record, error) {
	r, err := scanRecord(p.pool.QueryRow(ctx, `SELECT `+recordColumns+` FROM games WHERE game_id=$1`, gameID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("get game: %w", err)
	}
	return r, nil
}

func (p *Postgres) List(ctx context.Context) ([]Record, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+recordColumns+` FROM games ORDER BY game_id`)
	if err != nil {
		return nil, fmt.Errorf("list games: %w", err)
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("scan game: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

func (p *Postgres) Acquire(ctx context.Context, gameID, workerID string, ttl time.Duration) (Lease, error) {
	var lease Lease
	err := p.pool.QueryRow(ctx, `UPDATE games
SET owner_id=$2, epoch=epoch+1,
    lease_until=now()+($3::bigint * interval '1 millisecond'), updated_at=now()
WHERE game_id=$1 AND (owner_id IS NULL OR lease_until <= now())
RETURNING game_id, owner_id, epoch, lease_until`, gameID, workerID, ttl.Milliseconds()).Scan(
		&lease.GameID, &lease.WorkerID, &lease.Epoch, &lease.ValidUntil,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, ErrBusy
	}
	if err != nil {
		return Lease{}, fmt.Errorf("acquire game: %w", err)
	}
	return lease, nil
}

func (p *Postgres) Renew(ctx context.Context, lease Lease, ttl time.Duration) (Lease, error) {
	var renewed Lease
	err := p.pool.QueryRow(ctx, `UPDATE games
SET lease_until=now()+($4::bigint * interval '1 millisecond'), updated_at=now()
WHERE game_id=$1 AND owner_id=$2 AND epoch=$3 AND lease_until > now()
RETURNING game_id, owner_id, epoch, lease_until`, lease.GameID, lease.WorkerID, lease.Epoch, ttl.Milliseconds()).Scan(
		&renewed.GameID, &renewed.WorkerID, &renewed.Epoch, &renewed.ValidUntil,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, ErrFenced
	}
	if err != nil {
		return Lease{}, fmt.Errorf("renew lease: %w", err)
	}
	return renewed, nil
}

func (p *Postgres) Commit(ctx context.Context, lease Lease, previousSeq int64, snapshotKey string) (Record, error) {
	r, err := scanRecord(p.pool.QueryRow(ctx, `UPDATE games
SET snapshot_seq=snapshot_seq+1, snapshot_key=$5, updated_at=now()
WHERE game_id=$1 AND owner_id=$2 AND epoch=$3 AND lease_until > now() AND snapshot_seq=$4
RETURNING `+recordColumns, lease.GameID, lease.WorkerID, lease.Epoch, previousSeq, snapshotKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrFenced
	}
	if err != nil {
		return Record{}, fmt.Errorf("commit snapshot pointer: %w", err)
	}
	return r, nil
}
