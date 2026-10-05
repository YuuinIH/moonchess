# Local browser recovery smoke test — 2026-10-05

Tested the playable demo on macOS using the local Compose stack (two Go
workers, etcd, and one Mooncake Store with the REST adapter). Two independent
clients in Codex's in-app browser and Edge played through the visible UI.

The same game underwent six ownership changes while both pages stayed open:

| Transition | Action | New owner | Epoch | Preserved canonical sequence |
| --- | --- | --- | --- | --- |
| Initial | Match two clients, play e4/e5 | worker-b | 1 | 2 |
| 1 | Explicit migration | worker-a | 2 | 2 |
| 2 | SIGKILL worker-a (exit 137) | worker-b | 3 | 3 |
| 3 | Restart A, migrate back to A | worker-a | 4 | 4 |
| 4 | Explicit migration | worker-b | 5 | 5 |
| 5 | SIGKILL worker-b (exit 137) | worker-a | 6 | 6 |
| 6 | Restart B, migrate back to B | worker-b | 7 | 6 |

Restarting either worker left the current owner in place; transferring ownership
back to it succeeded. Both recovered workers subsequently committed new moves.
The clients needed no reload, preserved the room and moves, and finished with
Qxf7# at canonical sequence 7, result 1-0. Captured SSE snapshots from both
clients contained acquired epochs 1–7 and move sequences 1–7. Both workers were
running again when the test ended.

This is a bounded smoke test: commands were issued after takeover completed,
not during the ownerless interval. It used the initial checkpoint and did not
measure client outage latency, cross-node Store transfer, or long-run stability.
The automated checkpoint and stale-owner experiments cover those separate
recovery/fencing paths. The narrow-window board/debug overlap observed during
browser play is corrected with the game-ending UI change.

## Ending/clock follow-up

The game-ending change passed `make verify`, including Go race tests/vet,
etcd integration, the original smoke/fencing/migration/checkpoint/locality
experiments, and `scripts/ending-e2e.py`. The new real-backend E2E verifies
clock persistence through explicit migration and SIGKILL, off-turn resignation,
authorized abandonment, finish retries, identity reset after ending, and SSE
reconnection. A separate 12-second/zero-increment match confirms that an offline
player times out after owner takeover without issuing any move or browser timer
command. The gateway settings and killed workers are restored in cleanup.

Fresh independent browser clients also verified cancellation of each ending
confirmation, resignation on the opponent's turn, replay, play-again,
refresh without clock reset, and automatic timeout with both pages displaying
0-1/timeout and stopped clocks. Clicking h2/h3 in the in-app browser succeeded,
confirming the board/debug overlap fix. Default gateway settings were restored
to 5+3 after the short timeout test. Legacy untimed rooms were left intact.
