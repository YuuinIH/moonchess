# Playable demo requirements

Implementation scope requested for this change, based on current main:

1. Anonymous browser identity with server-issued client ID/default nickname,
   nickname editing and identity reset, without account/password/email systems.
2. Enqueue/cancel/status matchmaking using etcd transactions and leased tickets;
   multiple matchmaker instances must never match a client into two games.
3. Automatic room creation with white/black client IDs, independent room and
   worker identities, actor enforcement of seat membership and current turn.
4. Current game query and simple replay/play-again, without ELO, friends or a
   complex historical database.
5. REST commands and server-to-browser SSE matchmaking, game-state, committed
   move, finished-game and ownership/migration debug events; reconnection should
   recover missed events using an event cursor/canonical sequence.
6. Home / Searching / Game / Finished UI states and geek debug information:
   owner, epoch, canonical sequence, checkpoint, local materialization and
   migration/takeover events.
7. Two independent browser profiles can Find Match and play alternating turns.
   Explicit migrate and killed-owner takeover preserve the frontend stream and
   room, without exposing worker changes as game identity changes.
8. Preserve etcd control plane, immutable Mooncake checkpoint/delta data plane
   and REST Mooncake adapter. No native binding, PostgreSQL or Redis.
9. Cover identity, concurrent match uniqueness, SSE reconnect, seat/turn checks
   and SSE continuity through migration/failover with tests and E2E.
10. Update README/verify with a minimum reproducible demo.

The original unseated game API remains the local system-experiment sandbox.
Reset is gated while playing to avoid stranding an active opponent. Replay
uses the canonical legal move history already present in state checkpoints.

## Game endings and time controls

New matches use a 5+3 chess clock stored in immutable canonical state. The
current turn consumes time across browser disconnects and worker replacement;
only a committed legal move adds increment and switches the running side.
A seated player may resign or explicitly abandon on either turn. The UI must
confirm either action and allow cancellation. Closing a tab leaves time running
and allows reconnection. The owner commits timeout even without a live browser.
All endings use the existing fenced commit, preserve the legal move history,
stop clocks, and remain visible through current-game queries and SSE reconnect.
Reset and play-again are available after an ending. Legacy untimed rooms remain
compatible. Verify seat authorization, stale fencing, concurrent commands,
checkpoint/fallback recovery, and real migration/kill-owner behavior.
