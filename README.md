# MoonChess

MoonChess studies where a chess room's **state control plane** ends and Mooncake's **transfer/data plane** begins. Its second question is whether **partial materialization and state locality aware scheduling** reduce takeover work: a warm worker replays only a missing delta suffix, while a cold worker loads a checkpoint and replays the remaining chain. Here locality means the chess state already materialized in a Go worker, not Mooncake replica placement. Chess state is ordinary FEN, legal move history, turn, and status; it is not artificially enlarged to make transfers look impressive.

## Architecture

```text
Browser -> Go gateway -> Go room actor
              |              |
              |              +-- PUT immutable delta/checkpoint -> Mooncake Store
              |              +-- fenced CAS of canonical head -> etcd
              +-- read canonical metadata -> etcd

MaterializedState = Checkpoint + DeltaSuffix
```

etcd is the sole coordination authority. Each game has a metadata record containing epoch, committed sequence, head ref, latest and fallback checkpoint refs/sequences, migration target, and phase. A separate owner key has an etcd lease. Worker progress (materialized sequence and head) is advisory; it cannot authorize commits. Mooncake holds only immutable payloads. This experiment deliberately uses one basic Store node; multi-Store replica placement and physical cross-node transfer are not research targets. A delta key includes its content SHA-256 and sequence; competing attempts at one sequence cannot overwrite one another. A move is acknowledged only after the owner, lease, epoch, metadata revision, and head have passed one etcd transaction. Failed transactions leave unreachable Mooncake objects.

Both crash failover and explicit migration use `select target -> materialize to canonical head -> transfer ownership`. The target materializes before acquiring the owner key. Graceful migration pins the chosen target in etcd, releases the prior lease, then allows that target to acquire. A stale resumed worker can still produce an orphan object, but its etcd transaction is fenced.

Workers poll all games and keep a local warm materialization. `GET /internal/games/{id}/progress` reports path (`cold`, `warm`, or `hot`), payload bytes read, replay count, materialization latency, and the observed no-owner interval at takeover. The last value starts when a worker observes an empty owner key; it is not an end-to-end client outage measurement. Periodic checkpoints are written every eight moves. The latest and fallback checkpoints are retained; recovery tries the fallback when the latest cannot be read or verified. After publishing a newer checkpoint, GC waits for in-flight readers and Mooncake object leases, then retries removal of only deltas fully covered by the retained fallback and the older checkpoint. GC retries are process-local and best-effort: a worker crash or persistent removal failure can leak unreachable objects but cannot remove canonical state.

## Run and verify

Requires Go, Docker Compose, Python 3, `curl`, and `jq`.

```sh
make up
make verify
```

Open [http://localhost:18080](http://localhost:18080). `make verify` runs Go race tests, the etcd fencing contract test, a Mooncake smoke move, killed owner recovery, SIGSTOP/SIGCONT stale owner fencing, explicit migration, and checkpoint recovery. `make down` stops the stack without removing etcd data; `make clean` removes its Compose volume.

API: `POST /api/games`, `GET /api/games/{id}`, `POST /api/games/{id}/moves` with `{"move":"e2e4"}`, and `POST /api/games/{id}/migrate` with `{"target":"worker-b"}`.

## Native Mooncake binding status

Mooncake v0.3.13.post1 has an official Go binding over `store_c.h` and `libmooncake_store`; its `Setup`, `Put`, `Get`, and `Remove` surface was checked against the pinned source. The current Compose build uses the release image's official Store REST service as the Go data-plane adapter. That service wraps the native library and is sufficient for the single-Store experiment; Python is confined to the infrastructure image. The application, control plane, gateway, and workers are Go. See [binding survey](docs/mooncake-bindings.md). Reported bytes are application payload reads, not physical network traffic; native Go linking and multi-Store placement are optional future work, not completion criteria here.

The standalone Mooncake Store process is an independent worker failure boundary, not durable storage. Loss of that process loses payloads. Kubernetes is not required.

## Two-client playable demo

The browser uses an anonymous, server-issued opaque session in an HttpOnly,
SameSite cookie. One browser profile is one player; private windows or another
browser give another identity. Client IDs are public seat identifiers, not
credentials. Nicknames may be changed. Identity reset revokes the old session
and queue ticket; it is available when there is no active game (or after it
finishes), to avoid abandoning an opponent.

1. Run `make up`, then open `http://localhost:18080` in two different browser
   profiles, or one normal window and one private window.
2. Click **Find Match** in both. One becomes white and the other black. Click a
   piece and its destination to move; promotion asks for q/r/b/n. Only the
   seated player whose turn it is can submit a move.
3. Watch the debug panel: room ID stays fixed while owner, epoch, canonical seq,
   checkpoint and each worker's materialization metrics change. Click **Migrate
   to another worker**, or use `docker compose kill -s SIGKILL worker-a` (choose
   the displayed owner). The gateway SSE connection continues and a new owner
   takes over. Restart the killed service with `docker compose up -d worker-a`.
4. For a quick finished game, play `f2f3 e7e5 g2g4 d8h4`. Both browsers show
   Finished. **Replay** scrubs the legal move history locally; **Play again**
   releases the finished-game pointer and queues that client for another game.
   There is no ELO, friends list or searchable history database.

Each gateway runs a matchmaker. A single etcd transaction compares both client
revisions and both leased tickets, deletes the tickets, sets both current-game
pointers and creates the room metadata with `white_client_id` and
`black_client_id`. Preparing a Mooncake checkpoint happens first; a lost race
can leave an unreachable checkpoint, never a duplicate matched seat. Tickets
have a 30-second lease renewed by status requests/SSE; closing all browser tabs
lets the search expire. Room identity and player identity do not depend on the
worker currently owning the actor. Workers enforce seat/turn checks inside the
serialized actor, immediately before the existing fenced commit path.

Commands remain REST. `GET /api/events` uses SSE for `matchmaking`, `game_state`,
`move_committed`, `game_finished`, `ownership` and `ownership_state`. Browser
EventSource reconnects automatically. Event IDs encode room ID, canonical move
sequence, epoch and ownership-event etcd revision. Reconnect first recovers
ownership transitions from small durable etcd control records and missing
committed moves from the full canonical move list (also retained in checkpoints
following delta GC), then sends an authoritative snapshot. Consumers should
apply move sequences idempotently. `game_state`/`game_finished` snapshots may be
repeated. A fresh page gets a snapshot plus the current game's move history.
Migration/release/acquisition events are recorded in their corresponding etcd
transactions; `ownership_state` also reports sampled no-owner intervals caused
by lease expiry. It is not a lossless timeline of every instant of an outage.
No gateway-local event buffer is needed, so reconnect may reach another gateway.
The small ownership records are retained per room for this demo; automatic room
and session retention policies are outside its scope.

REST endpoints added:

- `GET /api/me`, `PATCH /api/me` with `{"nickname":"Moon player"}`,
  `POST /api/me/reset`.
- `POST /api/matchmaking/enqueue`, `DELETE /api/matchmaking/enqueue`,
  `GET /api/matchmaking/status`.
- `GET /api/games/current`, `POST /api/games/current/play-again`.
- `GET /api/events` (session cookie; optional `Last-Event-ID`).

The original `POST /api/games` remains an unseated **debug sandbox** for the
system experiments. Player game reads/moves require a seated session; a
client-supplied `client_id` cannot impersonate another player. Worker internal
ports and migration controls are trusted local demo infrastructure, not a
public production deployment boundary. The REST Mooncake adapter, etcd
coordination and immutable checkpoint/delta pipeline are unchanged.

`make verify` additionally tests anonymous sessions, 60 competing transactions
across two etcd clients (six unique matches), revoked ticket fencing, actor
seat/turn checks, and runs `scripts/demo-e2e.py` against the real Compose stack.
The E2E uses two independent cookie clients, keeps SSE open through explicit
migration and owner SIGKILL, reconnects an offline client to recover missing
moves and ownership events, finishes by checkmate and matches both again.
It requires Python 3 and restores the killed worker in a `finally` block.
For optional actual-browser verification, install Playwright in your test
runtime and have Chrome installed (`BROWSER_CHANNEL` can select another supported
channel), then run `node scripts/browser-e2e.cjs`; it uses two isolated browser
contexts and exercises the Home / Searching / Game / Finished UI and replay.
