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

// A queued retry belongs to the analysis being wiped: the next one must start
// at attempt 1, not inherit the old count and backoff.
func TestInDepth_wipesTakeTheRetryRowToo(t *testing.T) {
	s := newTestStore(t)
	wipes := map[string]func(id string) error{
		"clear":     s.ClearSummary,
		"reprocess": func(id string) error { _, err := s.ResetAndEnqueueSummary(id); return err },
		"summary":   func(id string) error { return s.SetSummaryText(id, "new") },
	}
	for name, wipe := range wipes {
		seedThumbVideo(t, s, name)
		if err := s.DeferInDepthRetry(name, "old", 5, retryNow); err != nil {
			t.Fatalf("%s: queue: %v", name, err)
		}
		if err := wipe(name); err != nil {
			t.Fatalf("%s: wipe: %v", name, err)
		}
		if n, err := s.InDepthRetryAttempts(name); err != nil || n != 0 {
			t.Fatalf("%s: attempts after wipe = %d, %v; want no row", name, n, err)
		}
	}
}

func TestSetInDepthIfSummary_writesOnlyBesideTheSameFinishedSummary(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")
	if _, err := s.db.Exec(`UPDATE videos SET summary = 'old', summary_status = ? WHERE id = 'v1'`, SummaryDone); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if ok, err := s.SetInDepthIfSummary("v1", "text", "old"); err != nil || !ok {
		t.Fatalf("same summary: %v, %v; want written", ok, err)
	}
	if ok, err := s.SetInDepthIfSummary("v1", "text2", "older"); err != nil || ok {
		t.Fatalf("another summary: %v, %v; want refused", ok, err)
	}
	if err := s.ClearSummary("v1"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if ok, err := s.SetInDepthIfSummary("v1", "text3", "old"); err != nil || ok {
		t.Fatalf("after a wipe: %v, %v; want refused", ok, err)
	}
	if got, _ := s.InDepth("v1"); got != "" {
		t.Fatalf("in-depth = %q, want none after the refused writes", got)
	}
}

func TestIgnoredRead(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")
	if ignored, err := s.IgnoredRead("v1"); err != nil || ignored {
		t.Fatalf("no ledger row: %v, %v; want false", ignored, err)
	}
	if _, err := s.db.Exec(`INSERT INTO channels (id) VALUES ('UCx')`); err != nil {
		t.Fatalf("channel: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO channel_videos (video_id, channel_id, state) VALUES ('v1', 'UCx', 'ignored')`); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if ignored, err := s.IgnoredRead("v1"); err != nil || !ignored {
		t.Fatalf("ignored: %v, %v; want true", ignored, err)
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
