package scan

import (
	"context"
	"errors"
	"testing"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// missingTabErr is yt-dlp's "this channel has no such tab" failure, the one
// listChannel swallows. IsMissingTab matches on the "does not have a"
// substring, so any stderr carrying that phrase lands here.
func missingTabErr(tab string) error {
	return &ytdlp.ExecError{
		Err:    errors.New("exit status 1"),
		Stderr: "ERROR: [youtube:tab] UC1: This channel does not have a " + tab + " tab",
	}
}

// undatedEntry is a listing entry with NO publish date. This is the shape that
// matters: isBackCatalogue returns false for an empty PublishedAt, so such a
// video is never filtered as back catalogue however old it really is. Rows
// written before migration 0008 and any listing yt-dlp returns without a tab
// date look like this.
func undatedEntry(id string) ytdlp.ChannelEntry {
	return ytdlp.ChannelEntry{
		ID: id, Title: id, DurationSeconds: 600, LiveStatus: "not_live",
		URL: "https://www.youtube.com/watch?v=" + id,
	}
}

// blindBaselineHarness subscribes a channel whose FIRST pass can see nothing:
// both tabs answer with yt-dlp's missing-tab error, so listChannel swallows
// both and hands back an empty listing with no error at all.
func blindBaselineHarness(t *testing.T, autodownload bool) *scanHarness {
	t.Helper()
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", autodownload, "")
	h.lister.err = missingTabErr("videos")
	h.lister.streamErr = missingTabErr("streams")
	return h
}

// baselinedAt reads the subscription's baseline stamp.
func baselinedAt(t *testing.T, h *scanHarness) string {
	t.Helper()
	var got string
	if err := h.db.QueryRow(
		`SELECT COALESCE(baselined_at, '') FROM subscriptions WHERE channel_id = 'UC1'`,
	).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

// TestScan_baselineSawNothing_doesNotClaimToBeBaselined is the fix.
//
// A baseline snapshot is the one listing that MUST be complete: everything it
// fails to record is judged against baselined_at forever after. A pass where
// both tabs failed learned nothing at all, so stamping baselined_at on it
// converts "yt-dlp could not answer" into "this channel had nothing" — a
// permanent claim, made on no evidence.
//
// listChannel already refuses to swallow a /streams failure on a baseline pass
// for exactly this reason. It swallowed a missing-tab failure regardless, so
// two swallowed tabs still produced a confident empty snapshot.
func TestScan_baselineSawNothing_doesNotClaimToBeBaselined(t *testing.T) {
	h := blindBaselineHarness(t, false /*autodownload*/)

	sub, _ := h.channels.ClaimDue(h.nowStr())
	err := h.sched.scanOnce(context.Background(), sub)

	if err == nil {
		t.Fatal("a baseline that saw neither tab reported success")
	}
	if got := baselinedAt(t, h); got != "" {
		t.Fatalf("baselined_at = %q after a pass that saw nothing, want it unstamped", got)
	}
}

// The flood this prevents, driven end to end.
//
// With baselined_at stamped by a blind pass, the next pass judges the
// channel's real back catalogue against it. Entries carrying no publish date
// are not back catalogue by definition (isBackCatalogue is false for ""), so
// every one of them is offered as an undecided inbox item — a channel's whole
// history arriving at once, which a completed baseline would have recorded as
// seen.
func TestScan_blindBaseline_doesNotFloodTheInboxWithBackCatalogue(t *testing.T) {
	h := blindBaselineHarness(t, false)

	// Pass 1: both tabs blind.
	sub, _ := h.channels.ClaimDue(h.nowStr())
	_ = h.sched.scanOnce(context.Background(), sub)

	// Pass 2: yt-dlp recovers and the channel's real, undated back catalogue
	// appears.
	h.lister.err = nil
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		undatedEntry("old1"), undatedEntry("old2"), undatedEntry("old3"),
	})
	scanAgain(t, h)

	pending, err := h.ledger.ListPending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("%d back-catalogue videos reached the inbox, want 0 — the first pass "+
			"that could actually see the channel is its baseline: %+v", len(pending), pending)
	}
	for _, id := range []string{"old1", "old2", "old3"} {
		if st := h.ledgerState(id); st != channelvideos.StateSeen {
			t.Fatalf("%s state = %q, want %q", id, st, channelvideos.StateSeen)
		}
	}
	if baselinedAt(t, h) == "" {
		t.Fatal("the pass that DID see the channel failed to baseline it")
	}
}

// The expensive version of the same flood: with autodownload on, a back
// catalogue that reaches the inbox is not merely noise, it is real downloads.
func TestScan_blindBaseline_doesNotQueueBackCatalogueForDownload(t *testing.T) {
	h := blindBaselineHarness(t, true /*autodownload*/)

	sub, _ := h.channels.ClaimDue(h.nowStr())
	_ = h.sched.scanOnce(context.Background(), sub)

	h.lister.err = nil
	h.lister.set("UC1", []ytdlp.ChannelEntry{undatedEntry("old1"), undatedEntry("old2")})
	scanAgain(t, h)

	jobsList, _ := h.jobs.List()
	if len(jobsList) != 0 {
		t.Fatalf("%d back-catalogue downloads were queued, want 0", len(jobsList))
	}
}

// A channel that genuinely has no streams tab — the overwhelmingly common
// shape — must still baseline off its /videos tab alone. The guard keys on
// "no tab answered", not on "some tab was missing", or it would block almost
// every channel peeq has.
func TestScan_baselineWithOnlyAVideosTab_stillBaselines(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	// newFakeLister already defaults streamErr to the missing-tab error.
	h.lister.set("UC1", []ytdlp.ChannelEntry{undatedEntry("v1")})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatalf("scanOnce: %v", err)
	}

	if baselinedAt(t, h) == "" {
		t.Fatal("a channel with only a videos tab failed to baseline")
	}
	if st := h.ledgerState("v1"); st != channelvideos.StateSeen {
		t.Fatalf("v1 state = %q, want %q", st, channelvideos.StateSeen)
	}
}

// A channel whose /videos tab answers with a genuinely EMPTY list is a real
// answer, not a blind pass: the tab was read, and it had nothing in it. That
// channel must baseline, or a brand-new empty channel would never complete one
// and its first upload would be judged with no baseline at all.
func TestScan_baselineWithAnEmptyButAnsweringTab_stillBaselines(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.lister.set("UC1", []ytdlp.ChannelEntry{}) // answered, and empty

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatalf("scanOnce: %v", err)
	}

	if baselinedAt(t, h) == "" {
		t.Fatal("an empty channel that answered failed to baseline; its first upload would have no baseline to be judged against")
	}
}

// A blind pass must not be mistaken for a dead channel either: it is a failure
// to see, and auto-unsubscribe exists for channels yt-dlp says are GONE.
func TestScan_blindBaseline_doesNotCountAsADeadScan(t *testing.T) {
	h := blindBaselineHarness(t, false)

	sub, _ := h.channels.ClaimDue(h.nowStr())
	_ = h.sched.scanOnce(context.Background(), sub)

	if n := h.deadScanCount("UC1"); n != 0 {
		t.Fatalf("dead scan count = %d after a blind baseline, want 0", n)
	}
}
