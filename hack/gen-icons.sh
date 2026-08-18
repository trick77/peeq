#!/usr/bin/env bash
# hack/gen-icons.sh
#
# Renders every favicon raster from the three SVG sources, plus the Companion
# extension's four icons from the same master. Run it by hand after editing any
# of them and commit what it writes.
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
MASTER="$SRC/icon.svg"          # the mark: ships as the tab icon AND renders the PWA rasters

for tool in rsvg-convert magick; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "gen-icons: $tool not found — brew install librsvg imagemagick" >&2
		exit 1
	fi
done
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --- icon.svg: served directly as the modern tab icon ------------------------
# The master itself, unmodified. There used to be a separate icon-favicon.svg
# here — the master with the frame's rect filled #33322f — because Safari plates
# favicons on a light rgb(192,192,192) bar and a hollow mark landed there as an
# orange scribble on a white card. The mark is a filled chip now, which is a
# solid object on that plate on its own, so the second source bought nothing and
# is gone.
cp "$MASTER" "$OUT/icon.svg"

# --- transparent, from the master --------------------------------------------
# -b none, and these still come out transparent: the master paints a chip, not a
# canvas, so the four corners outside its rx-4 radius stay clear and the mark
# lands as a chip rather than a square tile. The renderer must not supply a
# ground of its own here or the check below, which is what catches a master that
# grew a background rect back, could never fail.
rsvg-convert -b none -w 192 -h 192 "$MASTER" -o "$OUT/icon-192.png"
rsvg-convert -b none -w 512 -h 512 "$MASTER" -o "$OUT/icon-512.png"

# --- opaque, from the tiled sources -----------------------------------------
# No -b none here, and that is the point: iOS flattens alpha onto black and
# Android fills it with the launcher's own colour, so these two carry peeq's
# #1f1f1e edge to edge instead of letting the OS choose.
rsvg-convert -w 180 -h 180 "$SRC/icon-tile.svg" -o "$OUT/apple-touch-icon.png"
rsvg-convert -w 512 -h 512 "$SRC/icon-maskable.svg" -o "$OUT/icon-maskable-512.png"

# --- the Companion extension's icons, also from the master -------------------
# Chrome draws a generated letter tile — a grey "p" — for an extension whose
# manifest declares no icons, which is what the toolbar showed until these
# existed. The four sizes are the ones Chrome asks for: 16 in the toolbar and
# favicon slots, 32 on Windows, 48 in chrome://extensions, 128 on the Web Store
# and at install time. Chrome downscales when a size is missing, but its
# downscale of the 128 into a 16 is muddier than rsvg's own render, and 16 is
# the size the user actually looks at all day.
#
# Transparent, from the master, exactly like the tab icon: Chrome's toolbar
# follows the browser theme, so a mark with its own opaque ground would carry a
# square of peeq's dark panel onto a light toolbar. The chip is opaque ink and
# only its corners are clear, so it reads as a chip on either theme.
EXT="$ROOT/extension/icons"
mkdir -p "$EXT"
for size in 16 32 48 128; do
	rsvg-convert -b none -w "$size" -h "$size" "$MASTER" -o "$EXT/icon-$size.png"
done

# --- verify the grounds survived --------------------------------------------
# Rendering can succeed and still produce the wrong thing — a tiled source that
# lost its background rect yields a touch icon iOS flattens onto black; a master
# that GREW one stamps a square of peeq's ground onto every tab bar it lands in.
# Either fails silently in a viewer and only shows up on a real device, so
# assert every ground here instead.
#
# The four split two and two, and the halves are not the same decision:
#
#   icon-192/512          transparent — rendered from ui/icons/icon.svg, whose
#                         chip leaves the canvas corners clear. These were opaque
#                         for a while, when the master carried a #1f1f1e canvas to
#                         stop Safari's light tab bar showing through the corners
#                         of a rounded silhouette; the chip is opaque ink now, so
#                         there is no interior for a light bar to show through and
#                         that artifact has nothing left to appear in.
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
# The extension's four are transparent for the same reason icon-192/512 are, and
# for one more: a light Chrome toolbar shows through anything they leave opaque.
for size in 16 32 48 128; do
	check_alpha "$EXT/icon-$size.png" false
done

# icon.svg ships as SVG, so there is no raster to read — render one here just to
# assert it. Two samples, because it has to be a chip and not a tile, and it has
# to be filled: the canvas corner transparent, and a point inside the chip clear
# of the wedge fully opaque. Losing the fill is invisible until someone opens
# Safari's favourites bar.
#
# The interior is asserted on ALPHA, not on a hex value: the chip is painted with
# a gradient, so any single sample sits at one arbitrary point on the ramp and a
# hex equality would pin this check to that point and break on every retune.
#
# -alpha on before both reads. Without it a fully opaque image carries no alpha
# channel, and then %[fx:...a] does not report 1 — the comparisons would be
# measuring ImageMagick's channel bookkeeping rather than the icon.
rsvg-convert -b none -w 512 -h 512 "$OUT/icon.svg" -o "$TMP/icon-favicon.png"
fav_corner="$(magick "$TMP/icon-favicon.png" -alpha on -format '%[fx:p{0,0}.a]' info:)"
fav_ground="$(magick "$TMP/icon-favicon.png" -alpha on -format '%[fx:p{256,96}.a]' info:)"
if [[ "$fav_corner" != "0" ]]; then
	echo "gen-icons: icon.svg's canvas corner has alpha $fav_corner, expected 0" >&2
	fail=1
fi
if [[ "$fav_ground" != "1" ]]; then
	echo "gen-icons: icon.svg's chip has alpha $fav_ground, expected 1" >&2
	fail=1
fi

[[ "$fail" == 0 ]] || exit 1
echo "gen-icons: wrote $(cd "$OUT" && ls icon-*.png apple-touch-icon.png | tr '\n' ' ')"
echo "gen-icons: wrote $(cd "$EXT" && ls icon-*.png | sed 's|^|extension/icons/|' | tr '\n' ' ')"
