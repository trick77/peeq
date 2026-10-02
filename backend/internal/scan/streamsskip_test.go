package scan

import (
	"context"
	"errors"
	"testing"

	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/ytdlp"
)

func (h *scanHarness) streamsMissingAt() string {
	h.t.Helper()
	var at string
	if err := h.db.QueryRow(`SELECT COALESCE(streams_missing_at, '') FROM subscriptions WHERE channel_id = 'UC1'`).Scan(&at); err != nil {
		h.t.Fatal(err)
	}
	return at
}

func (h *scanHarness) scanAgain() {
	h.t.Helper()
	if err := h.sched.scanOnce(context.Background(), h.forceDue()); err != nil {
		h.t.Fatal(err)
	}
}

func (h *scanHarness) streamCalls() int {
	h.lister.mu.Lock()
	defer h.lister.mu.Unlock()
	return h.lister.streamCalls
}

// Most channels have never streamed and have no /streams tab, and every tab is
// a throttled yt-dlp call of its own. A scan that is told the tab does not
// exist remembers it and stops asking for a week.
func TestScan_missingStreamsTabIsNotAskedForAgainForAWeek(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)

	h.scanAgain()
	if got := h.streamCalls(); got != 1 {
		t.Fatalf("first scan made %d streams calls, want 1", got)
	}
	if h.streamsMissingAt() != h.nowStr() {
		t.Fatalf("streams_missing_at = %q, want the scan time %q", h.streamsMissingAt(), h.nowStr())
	}

	h.scanAgain()
	h.scanAgain()
	if got := h.streamCalls(); got != 1 {
		t.Fatalf("later scans made %d streams calls in total, want still 1", got)
	}

	// A week on, the channel may have started streaming: ask once more.
	old := fixedNow.Add(-streamsTabRecheck - 1).Format(store.TimeLayout)
	if _, err := h.db.Exec(`UPDATE subscriptions SET streams_missing_at = ? WHERE channel_id = 'UC1'`, old); err != nil {
		t.Fatal(err)
	}
	h.scanAgain()
	if got := h.streamCalls(); got != 2 {
		t.Fatalf("after the recheck window: %d streams calls, want 2", got)
	}
	if h.streamsMissingAt() != h.nowStr() {
		t.Fatalf("a still-missing tab must restart the window, got %q", h.streamsMissingAt())
	}
}

// A tab that answers is a channel that streams: the note is cleared and the
// tab is read on every scan again.
func TestScan_streamsTabThatAnswersClearsTheNote(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	old := fixedNow.Add(-streamsTabRecheck - 1).Format(store.TimeLayout)
	if _, err := h.db.Exec(`UPDATE subscriptions SET streams_missing_at = ? WHERE channel_id = 'UC1'`, old); err != nil {
		t.Fatal(err)
	}
	h.lister.setStreams("UC1", []ytdlp.ChannelEntry{})

	h.scanAgain()
	if h.streamsMissingAt() != "" {
		t.Fatalf("streams_missing_at = %q after the tab answered, want cleared", h.streamsMissingAt())
	}
	h.scanAgain()
	if got := h.streamCalls(); got != 2 {
		t.Fatalf("%d streams calls over two scans of a streaming channel, want 2", got)
	}
}

// The baseline pass must see the channel whole, whatever an earlier
// subscription to the same channel noted, so it never skips.
func TestScan_baselineNeverSkipsTheStreamsTab(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	if _, err := h.db.Exec(`UPDATE subscriptions SET streams_missing_at = ? WHERE channel_id = 'UC1'`, h.nowStr()); err != nil {
		t.Fatal(err)
	}
	h.scanAgain()
	if got := h.streamCalls(); got != 1 {
		t.Fatalf("baseline made %d streams calls, want 1", got)
	}
}

// With no /videos tab the streams tab is all there is to read; skipping it
// would scan nothing.
func TestScan_noVideosTabStillReadsStreams(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	if _, err := h.db.Exec(`UPDATE subscriptions SET streams_missing_at = ? WHERE channel_id = 'UC1'`, h.nowStr()); err != nil {
		t.Fatal(err)
	}
	h.lister.err = &ytdlp.ExecError{Err: errors.New("exit status 1"), Stderr: "ERROR: [youtube:tab] UC1: This channel does not have a videos tab"}
	h.scanAgain()
	if got := h.streamCalls(); got != 1 {
		t.Fatalf("%d streams calls for a channel without a videos tab, want 1", got)
	}
}

// A scan the user pressed for looks at everything, noted or not.
func TestScan_scanNowReadsTheStreamsTabAnyway(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	if _, err := h.db.Exec(`UPDATE subscriptions SET streams_missing_at = ? WHERE channel_id = 'UC1'`, h.nowStr()); err != nil {
		t.Fatal(err)
	}
	h.requestScan("UC1")
	h.scanAgain()
	if got := h.streamCalls(); got != 1 {
		t.Fatalf("a requested scan made %d streams calls, want 1", got)
	}
}
