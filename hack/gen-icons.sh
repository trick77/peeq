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
# ImageMagick is used only to re-read the results and assert their grounds.
#
# There is no favicon.ico here. peeq declares an SVG icon in <head>, and the
# clients that go looking for a bare /favicon.ico are RSS readers, Windows
# bookmark thumbnails and old IE — none of which peeq targets. backend/web
# answers that path with a 404 instead.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="$ROOT/ui/icons"
OUT="$ROOT/ui/public"
MASTER="$SRC/icon.svg"          # renders the PWA rasters; never ships itself
FAVICON="$SRC/icon-favicon.svg" # the master with a filled frame; ships as-is
TAB='#33322f'                   # the tab icon's ground, see icon-favicon.svg

for tool in rsvg-convert magick; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "gen-icons: $tool not found — brew install librsvg imagemagick" >&2
		exit 1
	fi
done
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --- icon.svg: served directly as the modern tab icon ------------------------
# Its own source, not the master: Safari plates favicons on its favourites bar,
# so this one carries a ground where the master does not.
cp "$FAVICON" "$OUT/icon.svg"

# --- transparent, from the master --------------------------------------------
# -b none, and now these genuinely come out transparent: the master paints only
# its glyph — no canvas rect, no fill inside the frame — so the mark composites
# onto whatever is behind it instead of stamping peeq's ground onto it. The
# renderer must not supply a ground of its own here or the check below, which
# is what catches a master that grew a background rect back, could never fail.
rsvg-convert -b none -w 192 -h 192 "$MASTER" -o "$OUT/icon-192.png"
rsvg-convert -b none -w 512 -h 512 "$MASTER" -o "$OUT/icon-512.png"

# --- opaque, from the tiled sources -----------------------------------------
# No -b none here, and that is the point: iOS flattens alpha onto black and
# Android fills it with the launcher's own colour, so these two carry peeq's
# #1f1f1e edge to edge instead of letting the OS choose.
rsvg-convert -w 180 -h 180 "$SRC/icon-tile.svg" -o "$OUT/apple-touch-icon.png"
rsvg-convert -w 512 -h 512 "$SRC/icon-maskable.svg" -o "$OUT/icon-maskable-512.png"

# --- verify the grounds survived --------------------------------------------
# Rendering can succeed and still produce the wrong thing — a tiled source that
# lost its background rect yields a touch icon iOS flattens onto black; a master
# that GREW one stamps a square of peeq's ground onto every tab bar it lands in.
# Either fails silently in a viewer and only shows up on a real device, so
# assert every ground here instead.
#
# The four split two and two, and the halves are not the same decision:
#
#   icon-192/512          transparent — rendered from ui/public/icon.svg, which
#                         paints only its glyph so the mark can take the colour
#                         behind it. These were opaque for a while, when the
#                         master carried a #1f1f1e canvas to stop Safari's light
#                         tab bar showing through the corners of a rounded
#                         silhouette. It no longer fills anything at all, which
#                         is why that artifact has nothing left to appear in.
#   apple-touch/maskable  opaque — from ui/icons/, and deliberately NOT following
#                         the master: iOS flattens alpha onto black and Android
#                         fills it with the launcher's colour, so these two must
#                         carry their own ground whatever the favicon does.
#
# A mismatch here means a source changed, not that the check needs relaxing.
fail=0
check_alpha() { # <file> <expected true|false>
	local got
	# ImageMagick 7 prints "True"/"False"; 6 printed "true"/"false". Fold the
	# case so this script is not pinned to one major version.
	got="$(magick identify -format '%[opaque]' "$1" | tr '[:upper:]' '[:lower:]')"
	if [[ "$got" != "$2" ]]; then
		echo "gen-icons: $(basename "$1") is opaque=$got, expected $2" >&2
		fail=1
	fi
}
check_alpha "$OUT/icon-192.png" false
check_alpha "$OUT/icon-512.png" false
check_alpha "$OUT/apple-touch-icon.png" true
check_alpha "$OUT/icon-maskable-512.png" true

# icon.svg ships as SVG, so there is no raster to read — render one here just to
# assert it. Two samples, because it has to be a chip and not a tile: the canvas
# corner transparent, and a point inside the frame clear of the wedge filled
# with $TAB. Losing the fill is invisible until someone opens Safari.
#
# -alpha on before both reads. Without it a fully opaque image carries no alpha
# channel, and then %[hex:...] returns six digits instead of eight and
# %[fx:...a] does not report 1 — the comparisons would be measuring
# ImageMagick's channel bookkeeping rather than the icon.
rsvg-convert -b none -w 512 -h 512 "$OUT/icon.svg" -o "$TMP/icon-favicon.png"
fav_corner="$(magick "$TMP/icon-favicon.png" -alpha on -format '%[fx:p{0,0}.a]' info:)"
fav_ground="$(magick "$TMP/icon-favicon.png" -alpha on -format '%[hex:p{256,96}]' info: | tr '[:upper:]' '[:lower:]')"
if [[ "$fav_corner" != "0" ]]; then
	echo "gen-icons: icon.svg's canvas corner has alpha $fav_corner, expected 0" >&2
	fail=1
fi
if [[ "$fav_ground" != "${TAB#\#}ff" ]]; then
	echo "gen-icons: icon.svg's frame is #$fav_ground, expected ${TAB}ff" >&2
	fail=1
fi

[[ "$fail" == 0 ]] || exit 1
echo "gen-icons: wrote $(cd "$OUT" && ls icon-*.png apple-touch-icon.png | tr '\n' ' ')"
