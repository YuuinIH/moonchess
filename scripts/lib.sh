#!/bin/sh
set -eu

gateway_url=${GATEWAY_URL:-http://localhost:18080}

need() {
  command -v "$1" >/dev/null 2>&1 || { echo "$1 is required" >&2; exit 1; }
}

create_game() {
  response=$(curl -fsS -X POST "$gateway_url/api/games")
  echo "$response" | jq -r '.control.gameId'
}

game_json() {
  curl -fsS "$gateway_url/api/games/$1"
}

wait_for_owner() {
  game_id=$1
  excluded=${2:-}
  attempts=0
  while [ "$attempts" -lt 40 ]; do
    response=$(game_json "$game_id" 2>/dev/null || true)
    owner=$(echo "$response" | jq -r '.control.ownerId // empty' 2>/dev/null || true)
    if [ -n "$owner" ] && [ "$owner" != "$excluded" ]; then
      echo "$response"
      return 0
    fi
    attempts=$((attempts + 1))
    sleep 0.5
  done
  echo "timed out waiting for owner (excluded=$excluded)" >&2
  return 1
}

move() {
  game_id=$1
  uci=$2
  curl -fsS -X POST "$gateway_url/api/games/$game_id/moves" \
    -H 'Content-Type: application/json' -d "{\"move\":\"$uci\"}"
}

worker_port() {
  case "$1" in
    worker-a) echo 18081 ;;
    worker-b) echo 18082 ;;
    *) echo "unknown worker $1" >&2; return 1 ;;
  esac
}
