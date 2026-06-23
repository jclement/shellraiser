#!/usr/bin/env bash
# Build shellraiser, start a --no-auth and an auth instance, run the Playwright
# e2e suite against them, and clean up. Run via `mise run e2e`.
set -euo pipefail
cd "$(dirname "$0")"
ROOT="$(cd ../.. && pwd)"
BIN=/tmp/shellraiser-e2e

echo ">> building shellraiser"
( cd "$ROOT" && go build -o "$BIN" ./cmd/worker )

echo ">> ensuring playwright + chromium"
[ -d node_modules/playwright ] || npm install --silent
npx playwright install chromium >/dev/null 2>&1 || true

AUTHHOME="$(mktemp -d)"
SHELLRAISER_POSTGRES=0 SHELLRAISER_CODE_SERVER=0 "$BIN" --no-auth --addr :7950 --repo "$ROOT" >/tmp/e2e-noauth.log 2>&1 & NA=$!
HOME="$AUTHHOME" SHELLRAISER_POSTGRES=0 SHELLRAISER_CODE_SERVER=0 "$BIN" --addr :7951 --repo "$ROOT" >/tmp/e2e-auth.log 2>&1 & AU=$!
trap 'kill $NA $AU 2>/dev/null || true; rm -rf "$AUTHHOME"' EXIT
sleep 2

BOOTSTRAP="$(python3 -c "import json;print(json.load(open('$AUTHHOME/.local/share/shellraiser/auth.json'))['bootstrap'])")"
echo ">> running e2e suite"
NOAUTH_URL=http://localhost:7950/ AUTH_URL=http://localhost:7951/ BOOTSTRAP="$BOOTSTRAP" node e2e.mjs
