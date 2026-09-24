package settings

import (
	"context"
	"database/sql"
	"testing"
)

// TestYoutubePaused_readErrorFailsClosed: the kill-switch is a gate in front
// of every YouTube call, and a gate that cannot be read must refuse, the same
// way CookieStatus answers "absent" when the row cannot be read. It used to
// fail open, which let a SQLITE_BUSY during the poll wave calls through. The
// error travels with the answer so a handler can report it rather than
// presenting the refusal as a pause someone set.
func TestYoutubePaused_readErrorFailsClosed(t *testing.T) {
	s := newTestStore(t)
	// A closed handle makes every query fail without touching the schema.
	if err := s.db.(*sql.DB).Close(); err != nil {
		t.Fatal(err)
	}
	paused, reason, err := s.YoutubePaused(context.Background())
	if err == nil {
		t.Fatal("a failed read must be reported")
	}
	if !paused {
		t.Fatal("a settings read error must report paused, not open")
	}
	if reason != "" {
		t.Fatalf("reason = %q; an unreadable row has no operator-set reason to show", reason)
	}
}
