#!/usr/bin/env bash
# hack/coverage-gate.test.sh — self-test for coverage-gate.sh
set -uo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail=0

check() { # check <label> <expected-exit> <actual-exit>
  if [ "$2" = "$3" ]; then
    echo "  ok   $1"
  else
    echo "  FAIL $1 (expected exit $2, got $3)"; fail=1
  fi
}

# 80 of 100 statements covered outside cmd/peeq -> 80.0%.
# cmd/peeq contributes 0 of 100 and must be ignored entirely.
cat > "$TMP/backend.out" <<'PROFILE'
mode: atomic
github.com/trick77/peeq/internal/auth/auth.go:1.1,2.2 80 1
github.com/trick77/peeq/internal/auth/auth.go:3.1,4.2 20 0
github.com/trick77/peeq/cmd/peeq/main.go:1.1,2.2 100 0
PROFILE

printf 'backend=79.0\nui=50.0\n' > "$TMP/floors-under"
printf 'backend=81.0\nui=50.0\n' > "$TMP/floors-over"

COVERAGE_FLOORS="$TMP/floors-under" COVERAGE_FILE="$TMP/backend.out" \
  ./hack/coverage-gate.sh backend >/dev/null 2>&1
check "passes when above floor" 0 $?

COVERAGE_FLOORS="$TMP/floors-over" COVERAGE_FILE="$TMP/backend.out" \
  ./hack/coverage-gate.sh backend >/dev/null 2>&1
check "fails when below floor" 1 $?

out=$(COVERAGE_FLOORS="$TMP/floors-under" COVERAGE_FILE="$TMP/backend.out" \
  ./hack/coverage-gate.sh backend 2>&1)
case "$out" in
  *80.0*) echo "  ok   excludes cmd/peeq (reports 80.0%)" ;;
  *)      echo "  FAIL excludes cmd/peeq — got: $out"; fail=1 ;;
esac

./hack/coverage-gate.sh bogus >/dev/null 2>&1
check "rejects unknown side" 2 $?

printf 'backend=79.0\nui=abc\n' > "$TMP/floors-nonnumeric"
COVERAGE_FLOORS="$TMP/floors-nonnumeric" COVERAGE_FILE="$TMP/backend.out" \
  ./hack/coverage-gate.sh ui >/dev/null 2>&1
check "rejects non-numeric floor" 2 $?

# --- ui branch ---

cat > "$TMP/ui-summary-good.json" <<'JSON'
{"total": {"statements": {"pct": 90.0}}}
JSON

cat > "$TMP/ui-summary-malformed.json" <<'JSON'
{not valid json
JSON

cat > "$TMP/ui-summary-missing-field.json" <<'JSON'
{"total": {"statements": {}}}
JSON

printf 'backend=79.0\nui=50.0\n' > "$TMP/floors-ui-under"

COVERAGE_FLOORS="$TMP/floors-ui-under" COVERAGE_FILE="$TMP/ui-summary-good.json" \
  ./hack/coverage-gate.sh ui >/dev/null 2>&1
check "ui passes when above floor" 0 $?

COVERAGE_FLOORS="$TMP/floors-ui-under" COVERAGE_FILE="$TMP/ui-summary-malformed.json" \
  ./hack/coverage-gate.sh ui >/dev/null 2>&1
check "ui rejects malformed JSON" 2 $?

COVERAGE_FLOORS="$TMP/floors-ui-under" COVERAGE_FILE="$TMP/ui-summary-missing-field.json" \
  ./hack/coverage-gate.sh ui >/dev/null 2>&1
check "ui rejects missing total.statements.pct" 2 $?

[ "$fail" = 0 ] && echo "coverage-gate: all checks passed"
exit "$fail"
