# MoonChess V0

MoonChess is a fault-tolerance demonstrator for a chess room actor. Game payloads are immutable snapshots in Mooncake Store; PostgreSQL contains only ownership, fencing epoch, lease, sequence, and the current snapshot pointer.

The application is pure Go. Mooncake runs as the official pinned `kvcacheai/mooncake:0.3.13.post1` infrastructure image over TCP—no Kubernetes, Redis, RDMA, GPU, or application-side Python.

## What is implemented

- Full legal chess move validation from UCI moves (`e2e4`, `e7e8q`, and so on).
- One goroutine/channel actor per owned game; commands for a room are serialized.
- Two workers competing for expiring PostgreSQL leases.
- Epoch plus owner plus sequence compare-and-swap on every commit.
- Immutable `game/<id>/epoch/<epoch>/seq/<seq>/attempt/<attempt>` Mooncake keys.
- Gateway API and a minimal browser board with live owner/epoch/lease state.
- Deterministic kill-takeover and paused-stale-worker experiments.
- `control.Plane` and `state.Store` interfaces for future backends.

See [the binding survey](docs/mooncake-bindings.md) for the Go/Rust/C++ decision and its limitations.

## Architecture

```text
Browser ──HTTP──> Gateway ──HTTP──> current Worker / room actor
                     │                         │
                     │                         ├── put immutable snapshot ──> Mooncake Store
                     │                         │
                     └──────── PostgreSQL <───┴── fenced CAS pointer commit
                               owner / epoch / lease / seq / key

                 Worker A                         Worker B
                 active or stale                  standby or takeover
```

The commit order is deliberate:

```text
validate move
  → serialize a new immutable snapshot
  → Mooncake PUT epoch/e, seq/n
  → PostgreSQL UPDATE ...
       WHERE owner = expected_owner
         AND epoch = expected_epoch
         AND lease_until > now()
         AND snapshot_seq = n-1
  → publish actor state only when that CAS succeeds
```

If the CAS fails, the new Mooncake object is an unreachable orphan. It can waste space, but it cannot become the current game state.

## Run

Requirements: Docker with Compose, `curl`, and `jq`. Both amd64 and arm64 images are available.

```bash
make up
make smoke
```

Open [http://localhost:18080](http://localhost:18080). First startup downloads the Mooncake image and may take several minutes. `make logs` follows startup logs; `make down` stops containers without deleting PostgreSQL data.

### Experiment 1: kill the owner

```bash
make experiment-kill
```

The script creates a game, commits `e2e4`, kills its owning worker, waits for the other worker to acquire a higher epoch and restore from Mooncake, then commits `e7e5`. It restarts the killed worker afterward.

### Experiment 2: fence a stale worker

```bash
make experiment-stale
```

The script pauses the owner beyond the three-second lease, waits for takeover, then resumes the old worker and sends it a move directly. The old actor still has its former epoch, so its real PostgreSQL commit returns HTTP 409. The same move succeeds through the new owner, and the committed sequence becomes 2.

### Cleanup

```bash
make clean
```

This removes the Compose containers and the PostgreSQL volume. Mooncake snapshots are memory-resident and disappear with the Store container.

## Local verification

```bash
make test
```

To run the full acceptance path—including Compose startup, the real PostgreSQL contract, Mooncake smoke test, killed-owner takeover, and stale-worker fencing—use:

```bash
make verify
```

The PostgreSQL contract test is opt-in because it needs a database:

```bash
MOONCHESS_TEST_POSTGRES_URL='postgres://moonchess:moonchess@localhost:15432/moonchess?sslmode=disable' \
  go test ./internal/control -run TestPostgresPlaneFencesExpiredOwner
```

## HTTP API

```text
POST /api/games
GET  /api/games/{gameId}
POST /api/games/{gameId}/moves   {"move":"e2e4"}
GET  /healthz
```

Worker endpoints on host ports 18081 and 18082 are exposed only so the stale-worker experiment can deliberately bypass the Gateway. They are not a public API.

## V0 limits

- The standalone Mooncake Store is a worker-failure boundary, not durable storage. Losing it loses snapshots.
- PostgreSQL is a single Compose instance; its own failover is outside this experiment.
- There is no authentication, matchmaking, clocks, draw offers, or orphan garbage collection.
- Gateway returns a transient 503 during the short interval between lease expiry and takeover; clients retry.
