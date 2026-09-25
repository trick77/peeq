package scan

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/ytdlp"
)

// loggedSched rebuilds the harness scheduler with a logger that writes to
// the returned buffer, so a test can assert on a store fault's log line.
func loggedSched(h *scanHarness, lister ChannelLister) (*Scheduler, *bytes.Buffer) {
	var buf bytes.Buffer
	sched := New(Deps{
		Channels: h.channels, Ledger: h.ledger, Videos: h.videos, Jobs: h.jobs,
		Settings:     h.settings,
		Lister:       lister,
		CookieStatus: func(context.Context) string { return h.cookieStatus },
		Activity:     h.activity,
		Now:          func() time.Time { return fixedNow },
		PollInterval: 5 * time.Millisecond,
		Logger:       slog.New(slog.NewTextHandler(&buf, nil)),
	})
	return sched, &buf
}

// TestScan_backoffStoreFaultIsLogged: a failed scan backs the channel off;
// when that write itself fails the pass still ends, and the fault is logged
// with the channel id.
func TestScan_backoffStoreFaultIsLogged(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	sched, buf := loggedSched(h, errLister{err: errors.New("listing broke")})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if sub == nil {
		t.Fatal("expected a due subscription")
	}
	if _, err := h.db.Exec(`CREATE TRIGGER block_backoff BEFORE UPDATE OF next_scan_at ON subscriptions
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}

	sched.scanChannel(context.Background(), sub)

	if out := buf.String(); !strings.Contains(out, "scan: backoff failed") || !strings.Contains(out, "channel_id=UC1") {
		t.Fatalf("backoff fault not logged with the channel id: %s", out)
	}
}

// TestScan_clearScanRequestStoreFaultIsLogged: a requested scan spends its
// marker when the pass ends; a failed clear is logged, not swallowed.
func TestScan_clearScanRequestStoreFaultIsLogged(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	h.lister.set("UC1", []ytdlp.ChannelEntry{{ID: "v1", DurationSeconds: 600, LiveStatus: "not_live"}})
	if err := h.channels.RequestScan("UC1", h.nowStr()); err != nil {
		t.Fatal(err)
	}
	sched, buf := loggedSched(h, h.lister)
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if sub == nil {
		t.Fatal("expected a due subscription")
	}
	if _, err := h.db.Exec(`CREATE TRIGGER block_clear BEFORE UPDATE OF scan_requested_at ON subscriptions
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}

	sched.scanChannel(context.Background(), sub)

	if out := buf.String(); !strings.Contains(out, "scan: clear scan request failed") || !strings.Contains(out, "channel_id=UC1") {
		t.Fatalf("clear fault not logged with the channel id: %s", out)
	}
}
