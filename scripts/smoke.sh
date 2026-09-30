#!/bin/sh
set -eu
. "$(dirname "$0")/lib.sh"
need curl
need jq

game_id=$(create_game)
owned=$(wait_for_owner "$game_id")
owner=$(echo "$owned" | jq -r '.control.ownerId')
result=$(move "$game_id" e2e4)
seq=$(echo "$result" | jq -r '.control.snapshotSeq')
[ "$seq" = "1" ]
echo "PASS smoke: game=$game_id owner=$owner seq=$seq move=e2e4"
