package videos

import "testing"

func TestInDepth_roundTripsAndReadsEmptyWhenAbsent(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")

	if got, err := s.InDepth("v1"); err != nil || got != "" {
		t.Fatalf("before any write: %q, %v; want empty, nil", got, err)
	}
	if err := s.SetInDepth("v1", "first"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.SetInDepth("v1", "second"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if got, err := s.InDepth("v1"); err != nil || got != "second" {
		t.Fatalf("after overwrite: %q, %v; want second", got, err)
	}
}

// Both ways of wiping a video's analysis must take the in-depth text with it.
// The worker skips the step while a row exists, so a survivor would hand a
// re-analysed video back its old reading.
func TestInDepth_clearedWithTheRestOfTheAnalysis(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")
	seedThumbVideo(t, s, "v2")
	for _, id := range []string{"v1", "v2"} {
		if err := s.SetInDepth(id, "text"); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	if err := s.ClearSummary("v1"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := s.ResetAndEnqueueSummary("v2"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	for _, id := range []string{"v1", "v2"} {
		if got, err := s.InDepth(id); err != nil || got != "" {
			t.Fatalf("%s after wipe: %q, %v; want empty", id, got, err)
		}
	}
}

// A new summary makes the old in-depth text stale, and the worker skips the
// step while a row exists — so writing a summary must drop it, whatever path
// cleared the summary before.
func TestSetSummaryText_dropsTheInDepthText(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")
	if err := s.SetInDepth("v1", "old"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.SetSummaryText("v1", "a new summary"); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	if got, err := s.InDepth("v1"); err != nil || got != "" {
		t.Fatalf("after a new summary: %q, %v; want empty", got, err)
	}
}

func TestDeleteVideo_cascadesToInDepth(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")
	if err := s.SetInDepth("v1", "text"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM videos WHERE id = ?`, "v1"); err != nil {
		t.Fatalf("delete video: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM video_in_depth`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows left = %d, %v; want 0", n, err)
	}
}
