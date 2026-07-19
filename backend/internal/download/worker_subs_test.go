package download

import (
	"context"
	"sync"
	"testing"

	"github.com/trick77/peeq/internal/ytdlp"
)

// spySummaryJobs is a SummaryEnqueuer that records every enqueued video ID,
// so a test can assert that a summary job is queued as a downstream
// consequence of a successful download.
type spySummaryJobs struct {
	mu  sync.Mutex
	ids []string
}

func (s *spySummaryJobs) Enqueue(videoID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids = append(s.ids, videoID)
	return int64(len(s.ids)), nil
}

func (s *spySummaryJobs) enqueued() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.ids))
	copy(out, s.ids)
	return out
}

// TestSucceedPersistsSubtitleAndEnqueuesSummary verifies that a successful
// download (a) resolves req.SubLang from DefaultSubLang when the video has
// no known AudioLanguage yet, (b) persists the subtitle/audio-language/
// chapters fields the Runner reports, and (c) enqueues a summary job for the
// video.
func TestSucceedPersistsSubtitleAndEnqueuesSummary(t *testing.T) {
	var gotSubLang string
	spy := &spySummaryJobs{}

	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			gotSubLang = req.SubLang
			return &ytdlp.Result{
				MediaPath:       "/media/v1/v1.mp4",
				SubtitleRelPath: "UC/v1/v1.en.vtt",
				AudioLanguage:   "en",
				ChaptersJSON:    `[{"ts":0,"title":"Intro","source":"yt-dlp"}]`,
			}, nil
		},
	}
	h := newHarness(t, runner, func(d *Deps) {
		d.SummaryJobs = spy
		d.DefaultSubLang = "en"
	})
	id := h.enqueue(t, "v1", 0)
	runWorker(t, h.worker)

	waitFor(t, "job done", func() bool { return h.jobState(t, id).State == "done" })
	waitForVideoStatus(t, h, "v1", "downloaded")

	if gotSubLang != "en" {
		t.Fatalf("req.SubLang = %q, want %q (from DefaultSubLang)", gotSubLang, "en")
	}

	v, err := h.videos.Get("v1")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.SubtitlePath != "UC/v1/v1.en.vtt" {
		t.Fatalf("SubtitlePath = %q, want %q", v.SubtitlePath, "UC/v1/v1.en.vtt")
	}
	if v.AudioLanguage != "en" {
		t.Fatalf("AudioLanguage = %q, want %q", v.AudioLanguage, "en")
	}
	if v.Chapters != `[{"ts":0,"title":"Intro","source":"yt-dlp"}]` {
		t.Fatalf("Chapters = %q, want the yt-dlp chapters JSON", v.Chapters)
	}

	ids := spy.enqueued()
	if len(ids) != 1 || ids[0] != "v1" {
		t.Fatalf("SummaryJobs.Enqueue calls = %v, want exactly [\"v1\"]", ids)
	}
}
