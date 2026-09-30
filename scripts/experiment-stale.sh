#!/bin/sh
set -eu
. "$(dirname "$0")/lib.sh"
need curl
need jq
need docker

game_id=$(create_game)
before=$(wait_for_owner "$game_id")
old_owner=$(echo "$before" | jq -r '.control.ownerId')
old_epoch=$(echo "$before" | jq -r '.control.epoch')
move "$game_id" e2e4 >/dev/null

container_id=$(docker compose ps -q "$old_owner")
[ -n "$container_id" ]
trap 'docker kill --signal=SIGCONT "$container_id" >/dev/null 2>&1 || true' EXIT
docker kill --signal=SIGSTOP "$container_id" >/dev/null
after=$(wait_for_owner "$game_id" "$old_owner")
new_owner=$(echo "$after" | jq -r '.control.ownerId')
new_epoch=$(echo "$after" | jq -r '.control.epoch')
docker kill --signal=SIGCONT "$container_id" >/dev/null
trap - EXIT

port=$(worker_port "$old_owner")
status=$(curl -sS -o /tmp/moonchess-stale-response.json -w '%{http_code}' \
  -X POST "http://localhost:$port/internal/games/$game_id/moves" \
  -H 'Content-Type: application/json' -d '{"move":"e7e5"}')
[ "$status" = "409" ]
move "$game_id" e7e5 >/dev/null
final=$(game_json "$game_id")
seq=$(echo "$final" | jq -r '.control.committedSeq')
[ "$seq" = "2" ]

echo "PASS stale fencing: $old_owner/epoch-$old_epoch got HTTP 409 after $new_owner/epoch-$new_epoch takeover; current owner committed seq=$seq"
