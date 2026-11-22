package videos

import (
	"errors"
	"testing"
)

const fetchedVTT = "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nhallo\n"

// seedCaptionless is a downloaded video whose caption call found nothing.
func seedCaptionless(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Upsert(Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetDownloaded("v1", DownloadedResult{MediaPath: "UC1/v1/v1.mp4", AudioLanguage: "en"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	if err := s.SetSummaryStatus("v1", SummaryNoTranscript, ""); err != nil {
		t.Fatalf("set no_transcript: %v", err)
	}
}

func activeJobCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM summary_jobs WHERE video_id = 'v1' AND state IN ('pending','running')`).Scan(&n); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	return n
}

func TestStoreFetchedCaptions_storesAndEnqueues(t *testing.T) {
	s := newTestStore(t)
	seedCaptionless(t, s)

	jobID, err := s.StoreFetchedCaptions("v1", fetchedVTT, "de")
	if err != nil || jobID == 0 {
		t.Fatalf("StoreFetchedCaptions: id=%d err=%v", jobID, err)
	}
	tr, err := s.GetTranscript("v1")
	if err != nil || tr == nil || tr.Source != TranscriptSourceDownload || tr.VTT != fetchedVTT {
		t.Fatalf("transcript = %+v err=%v, want the download-source text", tr, err)
	}
	v, _ := s.Get("v1")
	if v.AudioLanguage != "de" || v.SummaryStatus != SummaryPending {
		t.Fatalf("audio_language=%q summary_status=%q, want de/pending", v.AudioLanguage, v.SummaryStatus)
	}
	if n := activeJobCount(t, s); n != 1 {
		t.Fatalf("active jobs = %d, want 1", n)
	}
}

// The user asked for captions, not for a new category: a hand-picked one
// stays, flag included, so the classifier keeps off it.
func TestStoreFetchedCaptions_keepsManualCategory(t *testing.T) {
	s := newTestStore(t)
	seedCaptionless(t, s)
	if err := s.SetCategory("v1", "gaming"); err != nil {
		t.Fatalf("set category: %v", err)
	}

	if _, err := s.StoreFetchedCaptions("v1", fetchedVTT, ""); err != nil {
		t.Fatalf("StoreFetchedCaptions: %v", err)
	}
	var category string
	var manual int
	if err := s.db.QueryRow(`SELECT category, category_manual FROM videos WHERE id = 'v1'`).Scan(&category, &manual); err != nil {
		t.Fatalf("read category: %v", err)
	}
	if category != "gaming" || manual != 1 {
		t.Fatalf("category=%q manual=%d, want gaming/1", category, manual)
	}
	// No language reported keeps the download's.
	if v, _ := s.Get("v1"); v.AudioLanguage != "en" {
		t.Fatalf("audio_language = %q, want en kept", v.AudioLanguage)
	}
}

// Reprocess keeps its own rule: a redo re-categorizes.
func TestResetAndEnqueueSummary_stillClearsManualCategory(t *testing.T) {
	s := newTestStore(t)
	seedCaptionless(t, s)
	if err := s.SetCategory("v1", "gaming"); err != nil {
		t.Fatalf("set category: %v", err)
	}
	if _, err := s.ResetAndEnqueueSummary("v1"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	var category string
	var manual int
	if err := s.db.QueryRow(`SELECT category, category_manual FROM videos WHERE id = 'v1'`).Scan(&category, &manual); err != nil {
		t.Fatalf("read category: %v", err)
	}
	if category != UncategorizedCategory || manual != 0 {
		t.Fatalf("category=%q manual=%d, want uncategorized/0", category, manual)
	}
}

// Every guard refuses with ErrCaptionsSuperseded and writes nothing.
func TestStoreFetchedCaptions_guards(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, s *Store)
	}{
		{"download queued", func(t *testing.T, s *Store) {
			if err := s.SetStatus("v1", StatusQueued, ""); err != nil {
				t.Fatal(err)
			}
		}},
		{"downloading", func(t *testing.T, s *Store) {
			if err := s.SetStatus("v1", StatusDownloading, ""); err != nil {
				t.Fatal(err)
			}
		}},
		{"transcript arrived", func(t *testing.T, s *Store) {
			if err := s.SetTranscript("v1", TranscriptSourceDownload, "WEBVTT\n\nother\n"); err != nil {
				t.Fatal(err)
			}
		}},
		{"summary job active", func(t *testing.T, s *Store) {
			if _, err := s.db.Exec(`INSERT INTO summary_jobs (video_id) VALUES ('v1')`); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			seedCaptionless(t, s)
			tc.setup(t, s)
			jobsBefore := activeJobCount(t, s)

			_, err := s.StoreFetchedCaptions("v1", fetchedVTT, "de")

			if !errors.Is(err, ErrCaptionsSuperseded) {
				t.Fatalf("err = %v, want ErrCaptionsSuperseded", err)
			}
			if n := activeJobCount(t, s); n != jobsBefore {
				t.Fatalf("active jobs = %d, want %d", n, jobsBefore)
			}
			if tr, _ := s.GetTranscript("v1"); tr != nil && tr.VTT == fetchedVTT {
				t.Fatal("fetched transcript was stored")
			}
			if v, _ := s.Get("v1"); v.AudioLanguage != "en" {
				t.Fatalf("audio_language = %q, want en untouched", v.AudioLanguage)
			}
		})
	}
}

func TestStoreFetchedCaptions_unknownVideo(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.StoreFetchedCaptions("nope", fetchedVTT, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// A failed enqueue rolls the transcript back with it: no stored text without
// the analysis that reads it.
func TestStoreFetchedCaptions_isOneTransaction(t *testing.T) {
	s := newTestStore(t)
	seedCaptionless(t, s)
	if _, err := s.db.Exec(`CREATE TRIGGER no_job BEFORE INSERT ON summary_jobs BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, err := s.StoreFetchedCaptions("v1", fetchedVTT, "de"); err == nil {
		t.Fatal("expected the blocked enqueue to fail the call")
	}
	if tr, _ := s.GetTranscript("v1"); tr != nil {
		t.Fatal("transcript survived a rolled-back enqueue")
	}
	v, _ := s.Get("v1")
	if v.AudioLanguage != "en" || v.SummaryStatus != SummaryNoTranscript {
		t.Fatalf("audio_language=%q summary_status=%q, want en/no_transcript", v.AudioLanguage, v.SummaryStatus)
	}
}
