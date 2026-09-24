package scan

import (
	"context"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/ytdlp"
)

// cancelLister stands in for a scan interrupted by process shutdown: the
// listing is in flight when the parent context is cancelled.
type cancelLister struct{ cancel context.CancelFunc }

func (l cancelLister) ChannelVideos(ctx context.Context, _ string, _ int) ([]ytdlp.ChannelEntry, error) {
	l.cancel()
	return nil, ctx.Err()
}

func (l cancelLister) ChannelStreams(ctx context.Context, _ string, _ int) ([]ytdlp.ChannelEntry, error) {
	l.cancel()
	return nil, ctx.Err()
}

// TestScan_shutdownMidScanIsNotAFailure: a shutdown that lands during a scan
// must not count toward the auto-pause breaker, write a "scan failed" Activity
// row, or back the channel off for an hour. The next boot simply scans it.
func TestScan_shutdownMidScanIsNotAFailure(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	// A "Scan now" the user is owed an answer for: it must survive the shutdown
	// so the next boot's pass announces itself as that answer.
	if err := h.channels.RequestScan("UC1", h.nowStr()); err != nil {
		t.Fatalf("request scan: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failed := 0
	h.sched = New(Deps{
		Channels: h.channels, Ledger: h.ledger, Videos: h.videos, Jobs: h.jobs,
		Settings:     h.settings,
		Lister:       cancelLister{cancel: cancel},
		CookieStatus: func(context.Context) string { return h.cookieStatus },
		Activity:     h.activity,
		FailMonitor:  &fakeMonitor{onFail: func(string) { failed++ }},
		Now:          func() time.Time { return fixedNow },
		PollInterval: 5 * time.Millisecond,
	})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if sub == nil {
		t.Fatal("expected a due subscription")
	}
	before := h.nextScanAt(t, "UC1")
	h.sched.scanChannel(ctx, sub)

	if failed != 0 {
		t.Fatalf("FailMonitor.Fail called %d times for a shutdown", failed)
	}
	if ev := h.activity.scanEvents(); len(ev) != 0 {
		t.Fatalf("activity rows written for a shutdown: %+v", ev)
	}
	if after := h.nextScanAt(t, "UC1"); after != before {
		t.Fatalf("next_scan_at moved %q -> %q; a shutdown must not back the channel off", before, after)
	}
	var requested string
	if err := h.db.QueryRow(`SELECT COALESCE(scan_requested_at, '') FROM subscriptions WHERE channel_id = 'UC1'`).Scan(&requested); err != nil {
		t.Fatal(err)
	}
	if requested == "" {
		t.Fatal("the scan-now marker was cleared by a shutdown that answered nothing")
	}
}

func (h *scanHarness) nextScanAt(t *testing.T, channelID string) string {
	t.Helper()
	var next string
	if err := h.db.QueryRow(`SELECT COALESCE(next_scan_at, '') FROM subscriptions WHERE channel_id = ?`, channelID).Scan(&next); err != nil {
		t.Fatalf("read next_scan_at: %v", err)
	}
	return next
}
