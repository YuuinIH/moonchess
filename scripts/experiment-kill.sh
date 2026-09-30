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

docker compose kill "$old_owner" >/dev/null
after=$(wait_for_owner "$game_id" "$old_owner")
new_owner=$(echo "$after" | jq -r '.control.ownerId')
new_epoch=$(echo "$after" | jq -r '.control.epoch')
move "$game_id" e7e5 >/dev/null
docker compose up -d "$old_owner" >/dev/null

[ "$new_epoch" -gt "$old_epoch" ]
echo "PASS kill takeover: game=$game_id $old_owner/epoch-$old_epoch -> $new_owner/epoch-$new_epoch; play resumed"
