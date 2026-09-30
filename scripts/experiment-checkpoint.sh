#!/bin/sh
set -eu
. "$(dirname "$0")/lib.sh"
need curl
need jq

game_id=$(create_game)
wait_for_owner "$game_id" >/dev/null
for uci in e2e4 e7e5 g1f3 b8c6 f1c4 g8f6 d2d3 f8c5 c2c3 d7d6 b1d2 c8g4 h2h3 g4h5 a2a4 a7a6; do
  move "$game_id" "$uci" >/dev/null
done
final=$(game_json "$game_id")
echo "$final" | jq -e '.control.committedSeq == 16 and .control.checkpointSeq == 16 and .control.fallbackSeq == 8 and (.game.moves | length) == 16' >/dev/null
echo "PASS checkpoint recovery: game=$game_id latest=16 fallback=8 moves=16"
