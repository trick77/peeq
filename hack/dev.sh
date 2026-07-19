#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DB_PATH=${PEEQ_DB_PATH:-/tmp/peeq-dev.db}

cleanup() {
  if [ -n "${BACKEND_PID:-}" ]; then
    kill "$BACKEND_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

(
  cd "$ROOT/backend"
  PEEQ_SESSION_SECRET=${PEEQ_SESSION_SECRET:-dev-secret} \
  PEEQ_AUTH_MODE=dev \
  PEEQ_ADDR=127.0.0.1:8080 \
  PEEQ_PUBLIC_URL=http://127.0.0.1:8080 \
  PEEQ_DB_PATH="$DB_PATH" \
  go run ./cmd/peeq
) &
BACKEND_PID=$!

cd "$ROOT/ui"
npm run dev -- --host 127.0.0.1
