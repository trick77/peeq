package download

import (
	"context"
	"sync"
	"testing"

	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// A video whose transcript is already stored (the inbox caption read, or an
// earlier download) must not ask YouTube for captions again: the summary,
// search and player all read the stored row, and a refused caption request
// used to fail the whole download.
func TestDownloadSkipsSubtitlesWhenTranscriptStored(t *testing.T) {
	var gotSkip bool
	runner := &fakeRunner{
		fn: func(_ context.Context, _ int, req ytdlp.DownloadReq, _ func(ytdlp.Progress)) (*ytdlp.Result, error) {
			gotSkip = req.SkipSubtitles
			return &ytdlp.Result{MediaPath: "/media/v1/v1.mp4"}, nil
		},
	}
	h := newHarness(t, runner, nil)
	id := h.enqueue(t, "v1", 0)
	if err := h.videos.SetTranscript("v1", videos.TranscriptSourceCaption, "WEBVTT\n"); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	runWorker(t, h.worker)
	waitFor(t, "job done", func() bool { return h.jobState(t, id).State == "done" })

	if !gotSkip {
		t.Fatal("req.SkipSubtitles = false for a video with a stored transcript")
	}
	tr, err := h.videos.GetTranscript("v1")
	if err != nil || tr == nil || tr.VTT != "WEBVTT\n" {
		t.Fatalf("stored transcript = (%+v, %v), want the caption read kept", tr, err)
	}
}

// No stored transcript: the download is where the captions come from.
func TestDownloadRequestsSubtitlesWithoutTranscript(t *testing.T) {
	gotSkip := true
	runner := &fakeRunner{
		fn: func(_ context.Context, _ int, req ytdlp.DownloadReq, _ func(ytdlp.Progress)) (*ytdlp.Result, error) {
			gotSkip = req.SkipSubtitles
			return &ytdlp.Result{MediaPath: "/media/v1/v1.mp4"}, nil
		},
	}
	h := newHarness(t, runner, nil)
	id := h.enqueue(t, "v1", 0)
	runWorker(t, h.worker)
	waitFor(t, "job done", func() bool { return h.jobState(t, id).State == "done" })

	if gotSkip {
		t.Fatal("req.SkipSubtitles = true for a video with no transcript")
	}
}

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
		fn: func(_ context.Context, _ int, req ytdlp.DownloadReq, _ func(ytdlp.Progress)) (*ytdlp.Result, error) {
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
	// succeed() enqueues the summary job *after* SetDownloaded, so the video
	// status is not a sufficient sync point: wait on the last side effect.
	waitFor(t, "summary enqueued", func() bool { return len(spy.enqueued()) == 1 })

	if gotSubLang != "en" {
		t.Fatalf("req.SubLang = %q, want %q (from DefaultSubLang)", gotSubLang, "en")
	}

	v, err := h.videos.Get("v1")
	if err != nil {
		t.Fatalf("get video: %v", err)
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
