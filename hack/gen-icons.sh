#!/usr/bin/env bash
# hack/gen-icons.sh
#
# Renders every favicon raster from the three SVG sources. Run it by hand after
# editing any of them and commit what it writes.
#
# The outputs are COMMITTED rather than generated during the build, so neither
# `npm run build` nor CI needs an image toolchain. That is the whole reason this
# script is not a package.json script.
#
# librsvg does the rasterising, not ImageMagick's own SVG support: the marks are
# gradient-filled paths, and IM's internal MSVG delegate renders those poorly.
# ImageMagick is used only to assemble the .ico container from finished PNGs.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$ROOT/ui/icons"
OUT="$ROOT/ui/public"
MASTER="$OUT/icon.svg" # the transparent master ships as-is; it is not a copy

for tool in rsvg-convert magick; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "gen-icons: $tool not found — brew install librsvg imagemagick" >&2
		exit 1
	fi
done

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --- transparent, from the master -------------------------------------------
# Every .ico member is rendered at its NATIVE size rather than downsampled from
# one large render: 16px is where that difference is visible, and 16px is the
# size the .ico actually exists for.
for s in 16 32 48; do
	rsvg-convert -b none -w "$s" -h "$s" "$MASTER" -o "$TMP/f$s.png"
done
# -background none and an explicit RGBA colour type because IM's ICO writer is
# where alpha silently dies if an intermediate gets colormapped. `make verify`
# below re-reads the result to prove it did not.
magick "$TMP/f16.png" "$TMP/f32.png" "$TMP/f48.png" \
	-background none -define png:color-type=6 "$OUT/favicon.ico"

rsvg-convert -b none -w 192 -h 192 "$MASTER" -o "$OUT/icon-192.png"
rsvg-convert -b none -w 512 -h 512 "$MASTER" -o "$OUT/icon-512.png"

# --- opaque, from the tiled sources -----------------------------------------
# No -b none here, and that is the point: iOS flattens alpha onto black and
# Android fills it with the launcher's own colour, so these two carry peeq's
# #1f1f1e edge to edge instead of letting the OS choose.
rsvg-convert -w 180 -h 180 "$SRC/icon-tile.svg" -o "$OUT/apple-touch-icon.png"
rsvg-convert -w 512 -h 512 "$SRC/icon-maskable.svg" -o "$OUT/icon-maskable-512.png"

# --- verify the grounds survived --------------------------------------------
# Rendering can succeed and still produce the wrong thing: an .ico that lost its
# alpha, or a touch icon that kept it. Both fail silently in a viewer and only
# show up on a real device, so assert them here.
fail=0
check_alpha() { # <file> <expected true|false>
	local got
	# ImageMagick 7 prints "True"/"False"; 6 printed "true"/"false". Fold the
	# case so this script is not pinned to one major version.
	got="$(magick identify -format '%[opaque]' "$1[0]" | tr '[:upper:]' '[:lower:]')"
	if [[ "$got" != "$2" ]]; then
		echo "gen-icons: $(basename "$1") is opaque=$got, expected $2" >&2
		fail=1
	fi
}
check_alpha "$OUT/favicon.ico" false
check_alpha "$OUT/icon-192.png" false
check_alpha "$OUT/icon-512.png" false
check_alpha "$OUT/apple-touch-icon.png" true
check_alpha "$OUT/icon-maskable-512.png" true

frames="$(magick identify "$OUT/favicon.ico" | wc -l | tr -d ' ')"
if [[ "$frames" != "3" ]]; then
	echo "gen-icons: favicon.ico has $frames frames, expected 3 (16/32/48)" >&2
	fail=1
fi

[[ "$fail" == 0 ]] || exit 1
echo "gen-icons: wrote $(cd "$OUT" && ls favicon.ico icon-*.png apple-touch-icon.png | tr '\n' ' ')"
