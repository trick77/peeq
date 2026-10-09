package captionfetch

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// changeCounter wires OnChange into a worker and counts the calls per video.
func changeCounter(h *harness, f *fetcher) (*Worker, map[string]int) {
	got := map[string]int{}
	w := NewWorker(Deps{
		Fetcher: f, Ledger: h.ledger, Videos: h.videos, Summaries: h.summary, MediaDir: h.mediaDir,
		OnChange: func(id string) { got[id]++ },
	})
	return w, got
}

// TestOnChange_firesOncePerSpentAttempt: every attempt that reached YouTube
// can change what the Inbox card says (queued for summary, last error, gave
// up), so each one tells the open Inbox — once, however many rows it wrote.
func TestOnChange_firesOncePerSpentAttempt(t *testing.T) {
	t.Run("captions arrive", func(t *testing.T) {
		h := newHarness(t)
		rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.en.vtt")
		writeCaption(t, h, rel)
		w, got := changeCounter(h, &fetcher{results: []string{rel}})
		w.pass(context.Background())
		if got["v1"] != 1 {
			t.Fatalf("OnChange calls = %v, want v1 once", got)
		}
	})
	t.Run("ladder runs out", func(t *testing.T) {
		h := newHarness(t)
		w, got := changeCounter(h, &fetcher{})
		for i := 0; i < channelvideos.CaptionMaxAttempts; i++ {
			w.pass(context.Background())
			mustBeDue(t, h)
		}
		if got["v1"] != channelvideos.CaptionMaxAttempts {
			t.Fatalf("OnChange calls = %v, want v1 %d times", got, channelvideos.CaptionMaxAttempts)
		}
	})
}

// TestOnChange_silentWhenNothingChanged: a gated fetch hands its rung back and
// writes nothing a card shows; with the cookie down that repeats every tick,
// and each event would refetch every open Inbox for nothing.
func TestOnChange_silentWhenNothingChanged(t *testing.T) {
	h := newHarness(t)
	w, got := changeCounter(h, &fetcher{errs: []error{ytdlp.ErrNoCookie}})
	w.pass(context.Background())
	if len(got) != 0 {
		t.Fatalf("gated pass: OnChange calls = %v, want none", got)
	}

	if err := h.ledger.MarkCaptionSettled("v1"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	w.pass(context.Background())
	if len(got) != 0 {
		t.Fatalf("nothing due: OnChange calls = %v, want none", got)
	}
}
