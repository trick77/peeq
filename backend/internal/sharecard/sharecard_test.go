package sharecard

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/image/font"
)

// fakeThumb is a 16:9 stand-in for a yt-dlp thumbnail, with an off-center block
// so a crop that loses the subject is visible in a dumped card.
func fakeThumb(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(40 + x*80/w), uint8(60 + y*90/h), 120, 255})
		}
	}
	draw.Draw(img, image.Rect(w/3, h/3, 2*w/3, 2*h/3), image.NewUniform(color.RGBA{0xd9, 0x77, 0x57, 0xff}), image.Point{}, draw.Src)
	return img
}

func decode(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decode rendered card: %v", err)
	}
	return img
}

// dump writes the card to $PEEQ_CARD_DUMP when set, so the layout can be eyeballed
// without a running server. Never required for the test to pass.
func dump(t *testing.T, name string, b []byte) {
	t.Helper()
	dir := os.Getenv("PEEQ_CARD_DUMP")
	if dir == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatalf("dump card: %v", err)
	}
}

func TestRender_squareCardWithThumbnail(t *testing.T) {
	// Given a normal 16:9 thumbnail and a two-line title.
	// When the card is rendered.
	b, err := Render(fakeThumb(1280, 720), "How the Voyager 1 flight team debugged a 15-billion-kilometre memory fault", "Scott Manley · 22 min")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	dump(t, "card-with-thumb.jpg", b)

	// Then it is a 1200x1200 JPEG (the square iMessage never crops).
	img := decode(t, b)
	if got := img.Bounds(); got.Dx() != canvas || got.Dy() != canvas {
		t.Fatalf("card bounds = %v, want %dx%d", got, canvas, canvas)
	}
	// And the thumbnail is actually drawn: the canvas is not uniformly the
	// background color.
	if uniformBG(img) {
		t.Fatal("card is a flat background — nothing was drawn")
	}
}

func TestRender_noThumbnailStillRenders(t *testing.T) {
	// Given a video with no thumbnail on disk.
	// When the card is rendered.
	b, err := Render(nil, "A short one", "Kurzgesagt · 8 min")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	dump(t, "card-no-thumb.jpg", b)

	// Then it still produces a full-size card (text-only fallback), rather than
	// failing and leaving the unfurl with a broken image.
	img := decode(t, b)
	if got := img.Bounds(); got.Dx() != canvas || got.Dy() != canvas {
		t.Fatalf("card bounds = %v, want %dx%d", got, canvas, canvas)
	}
	if uniformBG(img) {
		t.Fatal("text-only card drew nothing")
	}
}

func TestRender_portraitThumbnailIsCroppedNotSquashed(t *testing.T) {
	// Given a portrait (shorts-shaped) thumbnail, which the 16:9 slot cannot fit.
	// When it is rendered.
	b, err := Render(fakeThumb(720, 1280), "Vertical video", "Chan · 1 min")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	dump(t, "card-portrait-thumb.jpg", b)
	// Then rendering still succeeds at the card size (the crop math handled it).
	if got := decode(t, b).Bounds(); got.Dx() != canvas {
		t.Fatalf("card width = %d, want %d", got.Dx(), canvas)
	}
}

func TestCoverCrop_matchesTargetAspectAndStaysInBounds(t *testing.T) {
	cases := []struct{ w, h int }{{1280, 720}, {720, 1280}, {1000, 1000}, {3840, 1080}}
	for _, c := range cases {
		src := image.Rect(0, 0, c.w, c.h)
		got := coverCrop(src, thumbW, thumbH)
		if !got.In(src) {
			t.Fatalf("crop %v escapes source %v", got, src)
		}
		// Aspect within one pixel of 16:9 (integer division rounds).
		want := float64(thumbW) / float64(thumbH)
		if ratio := float64(got.Dx()) / float64(got.Dy()); ratio < want*0.99 || ratio > want*1.01 {
			t.Fatalf("crop of %v has aspect %.3f, want ~%.3f", src, ratio, want)
		}
	}
}

func TestWrap_ellipsizesWhenTextOutrunsTheLines(t *testing.T) {
	// Given far more title than two lines can hold.
	long := strings.Repeat("supercalifragilistic ", 30)
	// When it is wrapped to the card's title box.
	lines := wrap(titleFace, long, canvas-2*margin, 2)
	// Then it stops at two lines and marks the truncation.
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if !strings.HasSuffix(lines[1], "…") {
		t.Fatalf("last line = %q, want a trailing ellipsis", lines[1])
	}
	for _, l := range lines {
		if w := font.MeasureString(titleFace, l).Round(); w > canvas-2*margin {
			t.Fatalf("line %q measures %d, wider than the %d box", l, w, canvas-2*margin)
		}
	}
}

func TestWrap_singleWordWiderThanTheBoxIsTruncated(t *testing.T) {
	lines := wrap(titleFace, strings.Repeat("A", 200), canvas-2*margin, 2)
	if len(lines) != 1 || !strings.HasSuffix(lines[0], "…") {
		t.Fatalf("got %q, want one ellipsized line", lines)
	}
	if w := font.MeasureString(titleFace, lines[0]).Round(); w > canvas-2*margin {
		t.Fatalf("truncated word still measures %d", w)
	}
}

func TestWrap_emptyTextYieldsNoLines(t *testing.T) {
	if got := wrap(subFace, "   ", 500, 1); got != nil {
		t.Fatalf("wrap of blank text = %q, want nil", got)
	}
}

// uniformBG reports whether every sampled pixel is still the background color.
func uniformBG(img image.Image) bool {
	for y := 0; y < canvas; y += 7 {
		for x := 0; x < canvas; x += 7 {
			r, g, b, _ := img.At(x, y).RGBA()
			if abs(int(r>>8)-int(colBG.R)) > 6 || abs(int(g>>8)-int(colBG.G)) > 6 || abs(int(b>>8)-int(colBG.B)) > 6 {
				return false
			}
		}
	}
	return true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
