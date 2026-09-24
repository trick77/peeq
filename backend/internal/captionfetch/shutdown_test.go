package captionfetch

import (
	"context"
	"testing"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/videos"
)

// TestShutdownMidFetchGivesTheAttemptBack: a process shutdown that lands during
// a caption fetch is not this video's captions failing to exist. The rung burned
// before the fetch is handed back, and on the last rung the video is NOT settled
// as no_transcript — which nothing would ever revisit.
func TestShutdownMidFetchGivesTheAttemptBack(t *testing.T) {
	h := newHarness(t)
	last := channelvideos.CaptionMaxAttempts - 1
	if _, err := h.db.Exec(`UPDATE channel_videos SET caption_attempts = ? WHERE video_id = 'v1'`, last); err != nil {
		t.Fatalf("seed attempts: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fetcher{onCall: func(context.Context) { cancel() }, errs: []error{context.Canceled}}

	h.worker(f).pass(ctx)

	var attempts int
	if err := h.db.QueryRow(`SELECT caption_attempts FROM channel_videos WHERE video_id = 'v1'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != last {
		t.Fatalf("caption_attempts = %d, want %d — the rung must be given back on shutdown", attempts, last)
	}
	v, err := h.videos.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if v != nil && v.SummaryStatus == videos.SummaryNoTranscript {
		t.Fatal("video settled as no_transcript by a shutdown on the last rung")
	}
}
