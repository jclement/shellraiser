#!/usr/bin/env bash
# v2 coordinator e2e: build sr, start a --no-auth coordinator against this repo,
# run the headless coordinator checks, and clean up. Run via `mise run e2e`.
set -euo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../.. && pwd)"
PORT=7790
export SHELLRAISER_HOME="$(mktemp -d)"
export SB_NO_OPEN=1
ID="$(basename "$ROOT")"

echo ">> cross-compiling workers + building sr"
( cd "$ROOT" && for a in amd64 arm64; do
    CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags='-s -w' \
      -o cmd/sr/assets/bin/worker-linux-$a ./cmd/worker
  done && go build -o "$SHELLRAISER_HOME/sr" ./cmd/sr )
SB="$SHELLRAISER_HOME/sr"

echo ">> ensuring playwright + chromium"
[ -d node_modules/playwright ] || npm install --silent
npx playwright install chromium >/dev/null 2>&1 || true

cleanup() {
  "$SB" down >/dev/null 2>&1 || true
  docker rm -f "sr_$ID" >/dev/null 2>&1 || true
  docker volume rm "sr_${ID}_vol" >/dev/null 2>&1 || true
  docker network rm "sr_net_$ID" >/dev/null 2>&1 || true
  rm -rf "$SHELLRAISER_HOME"
}
trap cleanup EXIT

echo ">> starting coordinator (builds image on first run)"
( cd "$ROOT" && "$SB" --no-auth --port "$PORT" ) || true
for i in $(seq 1 60); do curl -sf -o /dev/null "http://127.0.0.1:$PORT/api/workers" && break; sleep 2; done

echo ">> running coordinator checks"
COORD_URL="http://127.0.0.1:$PORT" PROJECT_ID="$ID" node coordinator.mjs
