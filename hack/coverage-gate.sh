#!/usr/bin/env bash
# hack/coverage-gate.sh <backend|ui>
#
# Fails when statement coverage falls below the committed floor in
# hack/coverage-floors. The floor only ever rises, and stops at 80.
#
# Backend coverage is recomputed from the raw coverprofile rather than scraped
# from `go tool cover -func`: that prints a total but gives no way to exclude a
# package, and cmd/peeq (main() wiring) is deliberately not counted.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FLOORS="${COVERAGE_FLOORS:-$ROOT/hack/coverage-floors}"
CAP=80.0
SIDE="${1:-}"

die() { echo "coverage-gate: $*" >&2; exit 2; }

case "$SIDE" in
  backend|ui) ;;
  *) die "usage: $0 <backend|ui>" ;;
esac

[ -f "$FLOORS" ] || die "floors file not found: $FLOORS"
FLOOR="$(awk -F= -v k="$SIDE" '$1==k {print $2; exit}' "$FLOORS")"
[ -n "$FLOOR" ] || die "no floor for '$SIDE' in $FLOORS"

if [ "$SIDE" = backend ]; then
  FILE="${COVERAGE_FILE:-$ROOT/coverage/backend.out}"
  [ -f "$FILE" ] || die "no coverprofile at $FILE — run the tests first"
  # Fields: <path>:<range> <numStatements> <executionCount>. Line 1 is "mode:".
  PCT="$(awk 'NR>1 && $1 !~ /\/cmd\/peeq\// { t += $2; if ($3 > 0) c += $2 }
              END { if (t == 0) { print "0.0"; exit } printf "%.1f", 100 * c / t }' "$FILE")"
else
  FILE="${COVERAGE_FILE:-$ROOT/coverage/ui/coverage-summary.json}"
  [ -f "$FILE" ] || die "no coverage summary at $FILE — run the UI tests first"
  PCT="$(node -e 'const s=require(process.argv[1]);
                  process.stdout.write(s.total.statements.pct.toFixed(1))' "$FILE")"
fi

# Compare with a 0.05 grace so float formatting alone can never fail a build.
if awk -v p="$PCT" -v f="$FLOOR" 'BEGIN { exit !(p < f - 0.05) }'; then
  echo "coverage-gate: $SIDE FAILED — ${PCT}% of statements, floor is ${FLOOR}%" >&2
  echo "  Add tests, or lower the floor in $FLOORS and say why in the commit." >&2
  exit 1
fi

echo "coverage-gate: $SIDE ok — ${PCT}% of statements (floor ${FLOOR}%)"

# Ratchet: suggest raising the floor, but never commit it automatically.
# release.yaml triggers on every push to master, so a bot commit here would
# cut a spurious release.
if awk -v p="$PCT" -v f="$FLOOR" -v cap="$CAP" \
       'BEGIN { exit !(p >= f + 0.5 && f < cap) }'; then
  NEW="$(awk -v p="$PCT" -v cap="$CAP" 'BEGIN { printf "%.1f", (p > cap ? cap : p) }')"
  echo "coverage-gate: floor can rise ${FLOOR} -> ${NEW}."
  echo "  Set '${SIDE}=${NEW}' in hack/coverage-floors in this PR."
fi
