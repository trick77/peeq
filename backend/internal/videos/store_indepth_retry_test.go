package videos

import (
	"testing"
	"time"
)

var retryNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func TestInDepthRetry_dueOnlyFromItsNextAt(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")

	if id, _, err := s.NextInDepthRetry(retryNow); err != nil || id != "" {
		t.Fatalf("empty queue: %q, %v; want none", id, err)
	}
	if err := s.DeferInDepthRetry("v1", "timeout", 1, retryNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("defer: %v", err)
	}
	if id, _, err := s.NextInDepthRetry(retryNow.Add(time.Minute)); err != nil || id != "" {
		t.Fatalf("before next_at: %q, %v; want none", id, err)
	}
	id, attempts, err := s.NextInDepthRetry(retryNow.Add(2 * time.Minute))
	if err != nil || id != "v1" || attempts != 1 {
		t.Fatalf("at next_at: %q attempts=%d, %v; want v1 attempts=1", id, attempts, err)
	}
	if n, err := s.InDepthRetryAttempts("v1"); err != nil || n != 1 {
		t.Fatalf("attempts = %d, %v; want 1", n, err)
	}
	if n, err := s.InDepthRetryAttempts("none"); err != nil || n != 0 {
		t.Fatalf("attempts of an unqueued video = %d, %v; want 0", n, err)
	}
}

// A second failure replaces the row's attempt count, error and time rather
// than adding a row: one video, one pending retry.
func TestInDepthRetry_deferReplacesTheRow(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")
	if err := s.DeferInDepthRetry("v1", "first", 1, retryNow); err != nil {
		t.Fatalf("defer 1: %v", err)
	}
	if err := s.DeferInDepthRetry("v1", "second", 2, retryNow.Add(10*time.Minute)); err != nil {
		t.Fatalf("defer 2: %v", err)
	}
	var n, attempts int
	var lastErr string
	if err := s.db.QueryRow(`SELECT COUNT(*), MAX(attempts), MAX(last_error) FROM in_depth_retry`).Scan(&n, &attempts, &lastErr); err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 1 || attempts != 2 || lastErr != "second" {
		t.Fatalf("rows=%d attempts=%d last_error=%q; want one row, 2, second", n, attempts, lastErr)
	}
	if id, _, _ := s.NextInDepthRetry(retryNow.Add(5 * time.Minute)); id != "" {
		t.Fatalf("due at %q before the new next_at", id)
	}
}

func TestInDepthRetry_oldestDueFirst(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "late")
	seedThumbVideo(t, s, "early")
	if err := s.DeferInDepthRetry("late", "", 1, retryNow.Add(-time.Minute)); err != nil {
		t.Fatalf("defer late: %v", err)
	}
	if err := s.DeferInDepthRetry("early", "", 1, retryNow.Add(-time.Hour)); err != nil {
		t.Fatalf("defer early: %v", err)
	}
	if id, _, err := s.NextInDepthRetry(retryNow); err != nil || id != "early" {
		t.Fatalf("next = %q, %v; want early", id, err)
	}
}

func TestInDepthRetry_clearAndCascade(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")
	seedThumbVideo(t, s, "v2")
	for _, id := range []string{"v1", "v2"} {
		if err := s.DeferInDepthRetry(id, "", 1, retryNow); err != nil {
			t.Fatalf("defer %s: %v", id, err)
		}
	}
	if err := s.ClearInDepthRetry("v1"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := s.ClearInDepthRetry("v1"); err != nil {
		t.Fatalf("clear twice: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM videos WHERE id = ?`, "v2"); err != nil {
		t.Fatalf("delete video: %v", err)
	}
	if id, _, err := s.NextInDepthRetry(retryNow); err != nil || id != "" {
		t.Fatalf("after clear + delete: %q, %v; want none", id, err)
	}
}

func TestInDepthRetry_storeErrorsAreReturned(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`DROP TABLE in_depth_retry`); err != nil {
		t.Fatalf("break: %v", err)
	}
	if err := s.DeferInDepthRetry("v1", "", 1, retryNow); err == nil {
		t.Error("defer: want an error")
	}
	if _, _, err := s.NextInDepthRetry(retryNow); err == nil {
		t.Error("next: want an error")
	}
	if _, err := s.InDepthRetryAttempts("v1"); err == nil {
		t.Error("attempts: want an error")
	}
	if err := s.ClearInDepthRetry("v1"); err == nil {
		t.Error("clear: want an error")
	}
	if _, err := s.QueueMissingInDepth(); err == nil {
		t.Error("backfill: want an error")
	}
}
