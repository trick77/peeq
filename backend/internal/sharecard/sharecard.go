// Package sharecard renders the 1200x1200 social preview card used as the
// og:image of a shared video link (video thumbnail + title + channel on the dark
// app surface). Square is the safe format — iOS 17+ crops link images toward a
// square, so a 1200x1200 card survives every iMessage context without clipping,
// and the same card renders identically in WhatsApp, Slack, and Twitter.
//
// Unlike music's square album art, a video thumbnail is 16:9: it is placed at its
// native aspect inside the square rather than center-cropped, because cropping a
// frame to square reliably chops whatever the frame is actually of.
package sharecard

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"strings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

const (
	canvas     = 1200
	margin     = 80
	thumbW     = canvas - 2*margin // 1040
	thumbH     = thumbW * 9 / 16   // 585 — the 16:9 slot the thumbnail is fitted into
	titlePx    = 64
	subtitlePx = 42
	titleLead  = 78 // line height for wrapped title lines
	jpegQ      = 88
)

// The palette mirrors peeq's CSS tokens (ui/src/index.css, Warm Editorial dark)
// so the card reads as the same product as the page it links to.
var (
	colBG    = color.RGBA{0x1f, 0x1f, 0x1e, 0xff} // --color-bg
	colInk   = color.RGBA{0xfa, 0xf9, 0xf5, 0xff} // --color-ink
	colMuted = color.RGBA{0x9c, 0x9a, 0x92, 0xff} // --color-muted
	colWell  = color.RGBA{0x2c, 0x2c, 0x2a, 0xff} // --color-active, the empty thumbnail slot

	titleFace = mustFace(gobold.TTF, titlePx)
	subFace   = mustFace(goregular.TTF, subtitlePx)
)

// mustFace parses one of the bundled Go font faces. peeq's own typefaces ship as
// woff2 only (ui/src/fonts), which opentype.Parse cannot read — so the card
// carries the brand through its colors and layout, not its typeface.
func mustFace(ttf []byte, px float64) font.Face {
	f, err := opentype.Parse(ttf)
	if err != nil {
		panic(err) // bundled font bytes are constant — a parse failure is a build bug
	}
	// DPI 72 makes 1 point == 1 pixel, so px is the on-screen size.
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic(err)
	}
	return face
}

// Render composes the card and returns JPEG bytes. thumb may be nil (no
// thumbnail on disk, or an undecodable one), in which case the text block is
// centered on the empty canvas rather than sitting under a hole.
func Render(thumb image.Image, title, subtitle string) ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, canvas, canvas))
	draw.Draw(img, img.Bounds(), image.NewUniform(colBG), image.Point{}, draw.Src)

	titleLines := wrap(titleFace, strings.TrimSpace(title), canvas-2*margin, 2)
	subLines := wrap(subFace, strings.TrimSpace(subtitle), canvas-2*margin, 1)

	// Lay the thumbnail + text out as one block, vertically centered.
	const gapThumbText = 66
	const gapTitleSub = 28
	blockH := thumbH + gapThumbText + len(titleLines)*titleLead
	if len(subLines) > 0 {
		blockH += gapTitleSub + subtitlePx
	}
	y := (canvas - blockH) / 2

	// The 16:9 slot is drawn either way. With no thumbnail it stays an empty well
	// carrying the wordmark, because a title alone floating in a 1200px square
	// reads as a broken image rather than as a deliberate text card.
	slot := image.Rect(margin, y, margin+thumbW, y+thumbH)
	draw.Draw(img, slot, image.NewUniform(colWell), image.Point{}, draw.Src)
	if thumb != nil {
		// Fill the slot completely: a thumbnail that is not itself 16:9 gets
		// center-cropped along its long axis only, which trims edges rather than
		// letterboxing the card with bars of a slightly different dark.
		xdraw.CatmullRom.Scale(img, slot, thumb, coverCrop(thumb.Bounds(), thumbW, thumbH), xdraw.Over, nil)
	} else {
		drawCentered(img, titleFace, colMuted, "peeq", y+(thumbH+titlePx)/2)
	}
	y += thumbH + gapThumbText

	for _, line := range titleLines {
		y += titlePx // advance to this line's baseline
		drawCentered(img, titleFace, colInk, line, y)
		y += titleLead - titlePx
	}
	if len(subLines) > 0 {
		y += gapTitleSub + subtitlePx - (titleLead - titlePx)
		drawCentered(img, subFace, colMuted, subLines[0], y)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQ}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// coverCrop returns the largest centered sub-rectangle of b with the dstW:dstH
// aspect, so the source fills the slot (never stretched, never letterboxed). A
// source that already matches the target aspect is returned whole.
func coverCrop(b image.Rectangle, dstW, dstH int) image.Rectangle {
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return b
	}
	cropW, cropH := w, h
	if w*dstH > h*dstW { // source is wider than the slot: trim its sides
		cropW = h * dstW / dstH
	} else { // source is taller: trim its top and bottom
		cropH = w * dstH / dstW
	}
	cx, cy := (b.Min.X+b.Max.X)/2, (b.Min.Y+b.Max.Y)/2
	return image.Rect(cx-cropW/2, cy-cropH/2, cx-cropW/2+cropW, cy-cropH/2+cropH)
}

func drawCentered(dst draw.Image, face font.Face, col color.Color, s string, baseline int) {
	w := font.MeasureString(face, s).Round()
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(col),
		Face: face,
		Dot:  fixed.Point26_6{X: fixed.I((canvas - w) / 2), Y: fixed.I(baseline)},
	}
	d.DrawString(s)
}

// wrap greedily packs s into at most maxLines lines that each fit maxW pixels.
// If words remain after the last allowed line, that line is ellipsized to signal
// the truncation; a single word wider than maxW is hard-truncated with an ellipsis.
func wrap(face font.Face, s string, maxW, maxLines int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	fits := func(str string) bool { return font.MeasureString(face, str).Round() <= maxW }

	var lines []string
	i := 0
	for i < len(words) && len(lines) < maxLines {
		cur := words[i]
		i++
		for i < len(words) && fits(cur+" "+words[i]) {
			cur += " " + words[i]
			i++
		}
		if !fits(cur) { // a lone word too wide for the line
			cur = ellipsize(face, cur, maxW)
		}
		lines = append(lines, cur)
	}
	if i < len(words) && len(lines) > 0 { // ran out of lines with text remaining
		last := len(lines) - 1
		lines[last] = ellipsize(face, lines[last], maxW)
	}
	return lines
}

// ellipsize returns s shortened so that s+"…" fits maxW (used to mark truncation).
func ellipsize(face font.Face, s string, maxW int) string {
	r := []rune(s)
	for len(r) > 0 {
		cand := strings.TrimRight(string(r), " ") + "…"
		if font.MeasureString(face, cand).Round() <= maxW {
			return cand
		}
		r = r[:len(r)-1]
	}
	return "…"
}
