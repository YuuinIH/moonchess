#!/bin/sh
set -eu
. "$(dirname "$0")/lib.sh"
need curl
need jq

game_id=$(create_game)
before=$(wait_for_owner "$game_id")
owner=$(echo "$before" | jq -r '.control.ownerId')
case "$owner" in worker-a) replica=worker-b ;; worker-b) replica=worker-a ;; esac
port=$(worker_port "$replica")
progress_url="http://localhost:$port/internal/games/$game_id/progress"
wait_progress() {
  wanted=$1
  attempts=0
  while [ "$attempts" -lt 40 ]; do
    current=$(curl -fsS "$progress_url" 2>/dev/null || true)
    seq=$(echo "$current" | jq -r '.seq // -1' 2>/dev/null || true)
    [ "$seq" = "$wanted" ] && return 0
    attempts=$((attempts + 1))
    sleep 0.5
  done
  echo "replica did not materialize seq $wanted" >&2
  return 1
}
wait_progress 0
move "$game_id" e2e4 >/dev/null
wait_progress 1
move "$game_id" e7e5 >/dev/null
wait_progress 2
sleep 1
progress=$(curl -fsS "$progress_url")
echo "$progress" | jq -e '(.history | any(.path == "cold")) and (.history | any(.path == "warm")) and (.hotCount > 0) and (.metrics.bytesMoved > 0) and (.metrics.replayCount > 0)' >/dev/null
echo "PASS locality: game=$game_id replica=$replica cold/warm/hot observed with transfer/replay metrics"
