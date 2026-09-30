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
store_response() {
  encoded=$(jq -nr --arg key "$1" '$key | @uri')
  docker compose exec -T mooncake-store bash -c 'exec 3<>/dev/tcp/127.0.0.1/8080; printf "GET /api/get/%s HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n" "$1" >&3; cat <&3' -- "$encoded"
}
store_status() {
  store_response "$1" | awk 'NR == 1 { print $2; exit }'
}
latest=$(echo "$final" | jq -r '.control.checkpointRef')
fallback=$(echo "$final" | jq -r '.control.fallbackRef')
[ "$(store_status "$latest")" = 200 ]
[ "$(store_status "$fallback")" = 200 ]
fallback_payload=$(store_response "$fallback" | sed '1,/^\r$/d')
echo "$fallback_payload" | jq -e '(.coveredRefs | length) == 8' >/dev/null
# Reads renew native object leases. Do not poll obsolete refs while GC is
# waiting for those leases to expire.
sleep 25
echo "$fallback_payload" | jq -r '.coveredRefs[], .previousCheckpointRef' | while IFS= read -r obsolete; do
  if [ "$(store_status "$obsolete")" != 404 ]; then
    echo "GC did not remove $obsolete" >&2
    exit 1
  fi
done
echo "PASS checkpoint recovery: game=$game_id latest=16 fallback=8 moves=16"
