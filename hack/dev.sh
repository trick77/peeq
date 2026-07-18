#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DB_PATH=${VARK_DB_PATH:-/tmp/vark-dev.db}

cleanup() {
  if [ -n "${BACKEND_PID:-}" ]; then
    kill "$BACKEND_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

(
  cd "$ROOT/backend"
  VARK_SESSION_SECRET=${VARK_SESSION_SECRET:-dev-secret} \
  VARK_AUTH_MODE=dev \
  VARK_ADDR=127.0.0.1:8080 \
  VARK_PUBLIC_URL=http://127.0.0.1:8080 \
  VARK_DB_PATH="$DB_PATH" \
  go run ./cmd/vark
) &
BACKEND_PID=$!

cd "$ROOT/ui"
npm run dev -- --host 127.0.0.1
