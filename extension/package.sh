#!/usr/bin/env bash
# Package the peeq Companion extension into a loadable/uploadable zip.
#
#   ./package.sh <version> <output.zip>
#
# Used by both CI (per-PR download artifact) and the release workflow (asset on
# the GitHub Release). Keeping it here rather than inline in the workflows means
# the packaging can be run and checked locally, exactly as CI runs it.
set -euo pipefail

VERSION="${1:?usage: package.sh <version> <output.zip>}"
OUTPUT="${2:?usage: package.sh <version> <output.zip>}"

SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

cp -R "$SRC/." "$STAGE/"

# Dev-only files are removed by DENYLIST, deliberately. An allowlist would
# silently omit a newly added runtime file — the extension would then load with
# a missing module and fail at runtime, which is far worse than shipping an
# extra file. Anything not named here is packaged.
rm -rf "$STAGE/testdata" "$STAGE/node_modules" "$STAGE/.git" "$STAGE/e2e"
rm -f "$STAGE/package.json" "$STAGE/package-lock.json" "$STAGE/package.sh"
find "$STAGE" -name '*.test.js' -delete
# Belt and braces: node_modules can appear at any depth, and shipping one into
# a Chrome Web Store package would be both enormous and a review red flag.
find "$STAGE" -name 'node_modules' -type d -prune -exec rm -rf {} +

# Chrome Web Store refuses an upload whose manifest version did not increase,
# and an unpacked install shows this string in chrome://extensions. Stamp the
# release version in rather than hand-maintaining it in the repo, so the
# packaged version can never disagree with the release that carried it.
node -e '
  const fs = require("fs");
  const path = process.argv[1];
  const version = process.argv[2];
  if (!/^\d+\.\d+\.\d+$/.test(version)) {
    console.error(`refusing to stamp a non-numeric version: ${version}`);
    process.exit(1);
  }
  const manifest = JSON.parse(fs.readFileSync(path, "utf8"));
  manifest.version = version;
  fs.writeFileSync(path, JSON.stringify(manifest, null, 2) + "\n");
' "$STAGE/manifest.json" "$VERSION"

# Fail loudly if packaging dropped something the extension needs to load. Every
# entry point the manifest names, plus the modules they import.
for required in manifest.json background.js send.js shared.js config.js \
                popup.html popup.js options.html options.js styles.css \
                icons/icon-16.png icons/icon-32.png icons/icon-48.png \
                icons/icon-128.png; do
  if [ ! -f "$STAGE/$required" ]; then
    echo "::error::packaged extension is missing $required" >&2
    exit 1
  fi
done

# No test file, dependency tree, or browser-test harness may reach the package.
# This guard caught a real leak: adding extension/e2e/ silently grew the zip
# from 10 files to 239 (17 MB of Playwright) until e2e/ joined the denylist.
if find "$STAGE" \( -name '*.test.js' -o -name 'node_modules' -o -name 'e2e' \) | grep -q .; then
  echo "::error::packaged extension contains test or dependency files:" >&2
  find "$STAGE" \( -name '*.test.js' -o -name 'node_modules' -o -name 'e2e' \) >&2
  exit 1
fi

# A runtime extension is a handful of small text files. A package far larger
# than that means something was swept in that does not belong.
FILE_COUNT="$(find "$STAGE" -type f | wc -l | tr -d ' ')"
if [ "$FILE_COUNT" -gt 25 ]; then
  echo "::error::packaged extension has $FILE_COUNT files, expected ~14 — something leaked in" >&2
  find "$STAGE" -type f >&2
  exit 1
fi

OUTPUT_ABS="$(cd "$(dirname "$OUTPUT")" && pwd)/$(basename "$OUTPUT")"
rm -f "$OUTPUT_ABS"
(cd "$STAGE" && zip -qr "$OUTPUT_ABS" .)

echo "packaged peeq Companion $VERSION -> $OUTPUT_ABS"
unzip -l "$OUTPUT_ABS"
