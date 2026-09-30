#!/bin/sh
set -eu
. "$(dirname "$0")/lib.sh"
need curl
need jq

game_id=$(create_game)
before=$(wait_for_owner "$game_id")
old_owner=$(echo "$before" | jq -r '.control.ownerId')
old_epoch=$(echo "$before" | jq -r '.control.epoch')
case "$old_owner" in worker-a) target=worker-b ;; worker-b) target=worker-a ;; esac
move "$game_id" e2e4 >/dev/null
curl -fsS -X POST "$gateway_url/api/games/$game_id/migrate" -H 'Content-Type: application/json' -d "{\"target\":\"$target\"}" >/dev/null
after=$(wait_for_owner "$game_id" "$old_owner")
new_epoch=$(echo "$after" | jq -r '.control.epoch')
move "$game_id" e7e5 >/dev/null
[ "$new_epoch" -gt "$old_epoch" ]
echo "PASS explicit migrate: game=$game_id $old_owner/epoch-$old_epoch -> $target/epoch-$new_epoch"
