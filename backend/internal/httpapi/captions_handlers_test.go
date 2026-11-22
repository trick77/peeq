package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/videos"
)

// fakeCaptionRefetcher records Start calls; busy names videos whose fetch is
// already in flight.
type fakeCaptionRefetcher struct {
	mu      sync.Mutex
	busy    map[string]bool
	started []string
	urls    []string
}

func (f *fakeCaptionRefetcher) Start(videoID, rawURL string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.busy[videoID] {
		return false
	}
	f.started = append(f.started, videoID)
	f.urls = append(f.urls, rawURL)
	return true
}

// fetchCaptionsDeps is the state the endpoint exists for: a downloaded video
// whose caption call found nothing, with YouTube reachable (valid cookie, not
// paused).
func fetchCaptionsDeps(t *testing.T) (Deps, *fakeCaptionRefetcher) {
	t.Helper()
	deps := searchTestDeps(t)
	vs := deps.Videos
	if err := vs.Upsert(videos.Video{ID: "v1", URL: "https://youtu.be/v1", Title: "A video"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := vs.SetDownloaded("v1", videos.DownloadedResult{MediaPath: "UC1/v1/v1.mp4"}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
	}
	if err := vs.SetSummaryStatus("v1", videos.SummaryNoTranscript, ""); err != nil {
		t.Fatalf("seed no_transcript: %v", err)
	}
	if err := deps.Settings.SetCookie(context.Background(), "", settings.CookieValid); err != nil {
		t.Fatalf("seed cookie: %v", err)
	}
	f := &fakeCaptionRefetcher{}
	deps.Captions = f
	return deps, f
}

func postFetchCaptions(t *testing.T, deps Deps, id string) *httptest.ResponseRecorder {
	t.Helper()
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	req := httptest.NewRequest(http.MethodPost, "/api/videos/"+id+"/fetch-captions", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestFetchCaptions_accepted(t *testing.T) {
	deps, f := fetchCaptionsDeps(t)

	rec := postFetchCaptions(t, deps, "v1")

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	if len(f.started) != 1 || f.started[0] != "v1" || f.urls[0] != "https://youtu.be/v1" {
		t.Fatalf("started = %v %v, want one fetch for v1 with its url", f.started, f.urls)
	}
}

// A swept video keeps its row, and captions are text: it can be repaired.
func TestFetchCaptions_tombstonedAccepted(t *testing.T) {
	deps, _ := fetchCaptionsDeps(t)
	if err := deps.Videos.SetStatus("v1", videos.StatusTombstoned, ""); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	if rec := postFetchCaptions(t, deps, "v1"); rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
}

func TestFetchCaptions_unknownVideo404(t *testing.T) {
	deps, f := fetchCaptionsDeps(t)

	if rec := postFetchCaptions(t, deps, "missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if len(f.started) != 0 {
		t.Fatalf("started = %v, want none", f.started)
	}
}

func TestFetchCaptions_refusals409(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(t *testing.T, d Deps, f *fakeCaptionRefetcher)
		// want is a fragment of the reason, which the Player toasts as is.
		want string
	}{
		{"transcript present", func(t *testing.T, d Deps, _ *fakeCaptionRefetcher) {
			if err := d.Videos.SetTranscript("v1", videos.TranscriptSourceDownload, "WEBVTT\n"); err != nil {
				t.Fatalf("seed transcript: %v", err)
			}
		}, "already has a transcript"},
		{"download queued", func(t *testing.T, d Deps, _ *fakeCaptionRefetcher) {
			if err := d.Videos.SetStatus("v1", videos.StatusQueued, ""); err != nil {
				t.Fatalf("queue: %v", err)
			}
		}, "download in progress"},
		{"downloading", func(t *testing.T, d Deps, _ *fakeCaptionRefetcher) {
			if err := d.Videos.SetStatus("v1", videos.StatusDownloading, ""); err != nil {
				t.Fatalf("downloading: %v", err)
			}
		}, "download in progress"},
		// summary_jobs has no unique active-job index: a second job would run
		// the analysis twice.
		{"summary pending", func(t *testing.T, d Deps, _ *fakeCaptionRefetcher) {
			if err := d.Videos.SetSummaryStatus("v1", videos.SummaryPending, ""); err != nil {
				t.Fatalf("pending: %v", err)
			}
		}, "summary is already queued"},
		{"summary running", func(t *testing.T, d Deps, _ *fakeCaptionRefetcher) {
			if err := d.Videos.SetSummaryStatus("v1", videos.SummaryRunning, ""); err != nil {
				t.Fatalf("running: %v", err)
			}
		}, "summary is already queued"},
		// The fetch would only be refused later, in Activity; say so now.
		{"youtube paused", func(t *testing.T, d Deps, _ *fakeCaptionRefetcher) {
			if err := d.Settings.SetYoutubePaused(ctx, true, "too many failures"); err != nil {
				t.Fatalf("pause: %v", err)
			}
		}, "paused"},
		{"cookie stale", func(t *testing.T, d Deps, _ *fakeCaptionRefetcher) {
			if err := d.Settings.SetCookie(ctx, "", settings.CookieStale); err != nil {
				t.Fatalf("stale cookie: %v", err)
			}
		}, "cookie"},
		{"already in flight", func(_ *testing.T, _ Deps, f *fakeCaptionRefetcher) {
			f.busy = map[string]bool{"v1": true}
		}, "already being fetched"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, f := fetchCaptionsDeps(t)
			tc.setup(t, deps, f)

			rec := postFetchCaptions(t, deps, "v1")

			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body %s", rec.Code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body = %s, want it to say %q", rec.Body, tc.want)
			}
			if len(f.started) != 0 {
				t.Fatalf("started = %v, want none", f.started)
			}
		})
	}
}

// An inbox video (never downloaded) belongs to the inbox caption fetcher,
// which stores its captions as a truncated 'caption' read.
func TestFetchCaptions_neverDownloaded409(t *testing.T) {
	deps, f := fetchCaptionsDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v2", URL: "https://youtu.be/v2"}); err != nil {
		t.Fatalf("seed v2: %v", err)
	}

	if rec := postFetchCaptions(t, deps, "v2"); rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if len(f.started) != 0 {
		t.Fatalf("started = %v, want none", f.started)
	}
}

func TestFetchCaptions_notConfigured503(t *testing.T) {
	deps, _ := fetchCaptionsDeps(t)
	deps.Captions = nil

	if rec := postFetchCaptions(t, deps, "v1"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
