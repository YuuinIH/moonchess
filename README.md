# MoonChess

MoonChess studies where a chess room's **state control plane** ends and Mooncake's **transfer/data plane** begins. Its second question is whether **partial materialization and state locality aware scheduling** reduce takeover work: a warm worker replays only a missing delta suffix, while a cold worker loads a checkpoint and replays the remaining chain. Chess state is ordinary FEN, legal move history, turn, and status; it is not artificially enlarged to make transfers look impressive.

## Architecture

```text
Browser -> Go gateway -> Go room actor
              |              |
              |              +-- PUT immutable delta/checkpoint -> Mooncake Store
              |              +-- fenced CAS of canonical head -> etcd
              +-- read canonical metadata -> etcd

MaterializedState = Checkpoint + DeltaSuffix
```

etcd is the sole coordination authority. Each game has a metadata record containing epoch, committed sequence, head ref, latest and fallback checkpoint refs/sequences, migration target, and phase. A separate owner key has an etcd lease. Worker progress (materialized sequence and head) is advisory; it cannot authorize commits. Mooncake holds only immutable payloads and handles their placement, replication, and transfer. A delta key includes its content SHA-256 and sequence; competing attempts at one sequence cannot overwrite one another. A move is acknowledged only after the owner, lease, epoch, metadata revision, and head have passed one etcd transaction. Failed transactions leave unreachable Mooncake objects.

Both crash failover and explicit migration use `select target -> materialize to canonical head -> transfer ownership`. The target materializes before acquiring the owner key. Graceful migration pins the chosen target in etcd, releases the prior lease, then allows that target to acquire. A stale resumed worker can still produce an orphan object, but its etcd transaction is fenced.

Workers poll all games and keep a local warm materialization. `GET /internal/games/{id}/progress` reports path (`cold`, `warm`, or `hot`), payload bytes read, replay count, materialization latency, and the observed no-owner interval at takeover. The last value starts when a worker observes an empty owner key; it is not an end-to-end client outage measurement. Periodic checkpoints are written every eight moves. The latest and fallback checkpoints are retained; recovery tries the fallback when the latest cannot be read or verified. After publishing a newer checkpoint, GC waits for in-flight readers and Mooncake object leases, then retries removal of only deltas fully covered by the retained fallback and the older checkpoint. GC retries are process-local and best-effort: a worker crash or persistent removal failure can leak unreachable objects but cannot remove canonical state.

## Run and verify

Requires Docker Compose, `curl`, and `jq`.

```sh
make up
make verify
```

Open [http://localhost:18080](http://localhost:18080). `make verify` runs Go race tests, the etcd fencing contract test, a Mooncake smoke move, killed owner recovery, SIGSTOP/SIGCONT stale owner fencing, explicit migration, and checkpoint recovery. `make down` stops the stack without removing etcd data; `make clean` removes its Compose volume.

API: `POST /api/games`, `GET /api/games/{id}`, `POST /api/games/{id}/moves` with `{"move":"e2e4"}`, and `POST /api/games/{id}/migrate` with `{"target":"worker-b"}`.

## Native Mooncake binding status

Mooncake v0.3.13.post1 has an official Go binding over `store_c.h` and `libmooncake_store`; its `Setup`, `Put`, `Get`, and `Remove` surface was checked against the pinned source. The current Compose build still uses the release image's official Store REST service as the Go data-plane adapter. That service itself wraps the native library, but includes Python in the infrastructure image. The application, control plane, gateway, and workers are Go. See [binding survey](docs/mooncake-bindings.md). Native Go linking and direct per-worker Mooncake segment placement remain unverified; the measured bytes are application payload reads, not physical network traffic.

The standalone Mooncake Store process is an independent worker failure boundary, not durable storage. Loss of that process loses payloads. Kubernetes is not required.
