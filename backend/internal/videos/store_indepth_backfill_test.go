package videos

import (
	"testing"
	"time"
)

// BACKFILL(in-depth): one-off fill of videos analysed before 0039. Remove
// (store_indepth_backfill.go, this file, summarize/backfill_indepth*.go and the
// call in summarize.Worker.Run) once in_depth_retry has drained to 0.

func TestQueueMissingInDepth_queuesOnlyUnwatchedSummarizedVideosWithoutOne(t *testing.T) {
	s := newTestStore(t)
	seed := func(id, status string, watched bool, summary string) {
		t.Helper()
		seedThumbVideo(t, s, id)
		if _, err := s.db.Exec(
			`UPDATE videos SET status = ?, watched = ?, summary = ?, summary_status = ? WHERE id = ?`,
			status, watched, summary, map[bool]string{true: SummaryDone, false: SummaryPending}[summary != ""], id); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
		if err := s.SetTranscript(id, TranscriptSourceDownload, "WEBVTT\n"); err != nil {
			t.Fatalf("transcript %s: %v", id, err)
		}
	}
	seed("inbox", StatusNew, false, "s")
	seed("library", StatusDownloaded, false, "s")
	seed("partial", StatusDownloaded, false, "s")
	if _, err := s.db.Exec(`UPDATE videos SET resume_position_seconds = 120 WHERE id = 'partial'`); err != nil {
		t.Fatalf("resume: %v", err)
	}
	seed("watched", StatusDownloaded, true, "s")
	seed("tombstoned", StatusTombstoned, false, "s")
	seed("errored", StatusError, false, "s")
	seed("unsummarized", StatusDownloaded, false, "")
	seed("has-one", StatusDownloaded, false, "s")
	if err := s.SetInDepth("has-one", "text"); err != nil {
		t.Fatalf("in-depth: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO channels (id) VALUES ('UCx')`); err != nil {
		t.Fatalf("channel: %v", err)
	}
	seed("ignored-kept-read", StatusNew, false, "s")
	if _, err := s.db.Exec(`INSERT INTO channel_videos (video_id, channel_id, state) VALUES ('ignored-kept-read', 'UCx', 'ignored')`); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	seed("pending-read", StatusNew, false, "s")
	if _, err := s.db.Exec(`INSERT INTO channel_videos (video_id, channel_id, state) VALUES ('pending-read', 'UCx', 'pending')`); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	seed("no-transcript", StatusDownloaded, false, "s")
	if _, err := s.db.Exec(`DELETE FROM video_transcripts WHERE video_id = 'no-transcript'`); err != nil {
		t.Fatalf("drop transcript: %v", err)
	}

	n, err := s.QueueMissingInDepth()
	if err != nil || n != 4 {
		t.Fatalf("queued %d, %v; want 4", n, err)
	}
	got := map[string]bool{}
	for {
		id, _, err := s.NextInDepthRetry(time.Now().Add(time.Hour))
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if id == "" {
			break
		}
		got[id] = true
		if err := s.ClearInDepthRetry(id); err != nil {
			t.Fatalf("clear: %v", err)
		}
	}
	if len(got) != 4 || !got["inbox"] || !got["library"] || !got["partial"] || !got["pending-read"] {
		t.Fatalf("queued %v; want inbox, library, partial, pending-read", got)
	}
}

// Every boot runs it: a video already queued keeps its row (attempts, error,
// backoff) instead of being reset to due-now.
func TestQueueMissingInDepth_keepsAQueuedVideosRow(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")
	if _, err := s.db.Exec(`UPDATE videos SET summary = 's', summary_status = ? WHERE id = 'v1'`, SummaryDone); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.SetTranscript("v1", TranscriptSourceDownload, "WEBVTT\n"); err != nil {
		t.Fatalf("transcript: %v", err)
	}
	later := time.Now().Add(6 * time.Hour)
	if err := s.DeferInDepthRetry("v1", "boom", 4, later); err != nil {
		t.Fatalf("defer: %v", err)
	}
	if n, err := s.QueueMissingInDepth(); err != nil || n != 0 {
		t.Fatalf("queued %d, %v; want 0", n, err)
	}
	if id, _, _ := s.NextInDepthRetry(time.Now()); id != "" {
		t.Fatalf("%s due now, want its backoff kept", id)
	}
	if id, attempts, _ := s.NextInDepthRetry(later); id != "v1" || attempts != 4 {
		t.Fatalf("next = %q attempts=%d; want v1 attempts=4", id, attempts)
	}
}
