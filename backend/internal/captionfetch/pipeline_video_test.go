package captionfetch

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// seedQueuedByURL puts the harness's inbox video into the download pipeline
// the way the add-by-URL path does: a videos row in 'queued' with the
// download's own transcript, and no change to the ledger.
func seedQueuedByURL(t *testing.T, h *harness) {
	t.Helper()
	if _, err := h.db.Exec(`UPDATE channels SET auto_summary = 1 WHERE id = 'UC1'`); err != nil {
		t.Fatal(err)
	}
	if err := h.videos.Upsert(videos.Video{ID: "v1", URL: "https://youtu.be/v1", Title: "A video"}); err != nil {
		t.Fatal(err)
	}
	if err := h.videos.SetStatus("v1", videos.StatusQueued, ""); err != nil {
		t.Fatal(err)
	}
	vtt := "WEBVTT\n\n00:00:00.000 " + "--" + "> 00:00:01.000\nfrom the download\n"
	if err := h.videos.SetTranscript("v1", videos.TranscriptSourceDownload, vtt); err != nil {
		t.Fatal(err)
	}
}

func assertLeftToTheDownload(t *testing.T, h *harness) {
	t.Helper()
	v, err := h.videos.Get("v1")
	if err != nil || v == nil {
		t.Fatal(err)
	}
	if v.Status != videos.StatusQueued {
		t.Fatalf("status = %q, want queued left untouched", v.Status)
	}
	tr, err := h.videos.GetTranscript("v1")
	if err != nil || tr == nil {
		t.Fatalf("transcript gone: %v", err)
	}
	if tr.Source != videos.TranscriptSourceDownload {
		t.Fatalf("transcript source = %q, want the download's kept", tr.Source)
	}
	active, err := h.summary.ListActive()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("%d summary jobs queued for a video the download pipeline owns", len(active))
	}
}

// TestVideoQueuedByURLIsNeverRead: an inbox video the user then adds by URL is
// in the download pipeline. The caption fetcher must leave it alone — not fetch,
// not spend a rung, not reset its status, not replace the transcript the
// download stored, not queue a second analysis.
func TestVideoQueuedByURLIsNeverRead(t *testing.T) {
	h := newHarness(t)
	seedQueuedByURL(t, h)
	f := &fetcher{results: []string{"never used"}}

	h.worker(f).pass(context.Background())

	if f.calls != 0 {
		t.Fatalf("fetched %d times for a video already in the pipeline, want 0", f.calls)
	}
	var attempts int
	if err := h.db.QueryRow(`SELECT caption_attempts FROM channel_videos WHERE video_id = 'v1'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("caption_attempts = %d, want 0 — no rung may be spent on a video that was never read", attempts)
	}
	assertLeftToTheDownload(t, h)
}

// TestVideoQueuedDuringTheFetchKeepsTheDownload: the fetch waits on the shared
// pacer, and the user can queue the video meanwhile. The candidate check ran
// before the wait, so the row is checked again before anything is written.
func TestVideoQueuedDuringTheFetchKeepsTheDownload(t *testing.T) {
	h := newHarness(t)
	rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.en.vtt")
	writeCaption(t, h, rel)
	f := &fetcher{results: []string{rel}, onCall: func(context.Context) { seedQueuedByURL(t, h) }}

	h.worker(f).pass(context.Background())

	if f.calls != 1 {
		t.Fatalf("fetched %d times, want 1", f.calls)
	}
	assertLeftToTheDownload(t, h)
}
