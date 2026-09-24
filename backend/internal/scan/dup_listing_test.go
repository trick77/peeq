package scan

import (
	"context"
	"testing"

	"github.com/trick77/peeq/internal/ytdlp"
)

// TestScan_idListedTwiceInOneTabIsRecordedOnce: nothing guarantees a tab
// lists an id once. The ledger dedup is a snapshot taken before the loop
// writes, so the repeat has to be skipped in the loop itself — or the second
// Insert would hit the primary key, abort the pass, and count a failure toward
// auto-pause.
func TestScan_idListedTwiceInOneTabIsRecordedOnce(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", []string{"old1"})
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "dup", DurationSeconds: 600, LiveStatus: "not_live"},
		{ID: "dup", DurationSeconds: 600, LiveStatus: "not_live"},
		{ID: "other", DurationSeconds: 600, LiveStatus: "not_live"},
	})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatalf("a repeated id must not fail the pass: %v", err)
	}
	if h.ledgerState("dup") != "pending" {
		t.Fatalf("dup state = %q, want pending", h.ledgerState("dup"))
	}
	if h.ledgerState("other") != "pending" {
		t.Fatalf("other state = %q, want pending — the rest of the listing must still be processed", h.ledgerState("other"))
	}
}
