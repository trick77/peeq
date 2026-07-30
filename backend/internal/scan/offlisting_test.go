package scan

import (
	"context"
	"errors"
	"testing"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// dropFromListing replaces the channel's listing with entries that no longer
// mention the parked video — what happens naturally as a channel keeps
// publishing and an older upload falls past defaultListSize.
func dropFromListing(t *testing.T, h *scanHarness, keep ...ytdlp.ChannelEntry) {
	t.Helper()
	if keep == nil {
		keep = []ytdlp.ChannelEntry{}
	}
	h.lister.set("UC1", keep)
}

// TestScan_parkedVideoOffListing_isStillRechecked is the whole point of the
// off-listing pass.
//
// A video parked as unavailable also had its videos row discarded
// (Worker.park), so the ledger row is the only record of it left. Re-checking
// only the ids the LISTING returned meant that record went unread forever once
// the video aged past the listing cap — permanent loss, and permanent
// specifically for videos parked in error, since a stale yt-dlp condemns a
// batch that then ages out together.
func TestScan_parkedVideoOffListing_isStillRechecked(t *testing.T) {
	h := parkedHarness(t, false /*autodownload*/)
	dropFromListing(t, h)
	backdate(t, h, "v1")
	prober := useProber(t, h, &fakeProber{availability: "public"})

	scanAgain(t, h)

	if prober.calls != 1 {
		t.Fatalf("probed %d times for a parked video the listing dropped, want 1", prober.calls)
	}
	if st := h.ledgerState("v1"); st != channelvideos.StatePending {
		t.Fatalf("v1 state = %q, want %q — a reachable video must return to the inbox", st, channelvideos.StatePending)
	}
	pending, _ := h.ledger.ListPending()
	if len(pending) != 1 || pending[0].VideoID != "v1" {
		t.Fatalf("pending = %+v, want exactly v1", pending)
	}
}

// The revived row is the ONLY description of the video left, so an
// autodownload channel has to be able to enqueue straight from it — the
// listing entry the ordinary path uses does not exist here.
func TestScan_parkedVideoOffListing_autodownloadQueuesFromTheLedgerRow(t *testing.T) {
	h := parkedHarness(t, true /*autodownload*/)
	dropFromListing(t, h)
	backdate(t, h, "v1")
	useProber(t, h, &fakeProber{availability: "public"})

	scanAgain(t, h)

	if st := h.ledgerState("v1"); st != channelvideos.StateQueued {
		t.Fatalf("v1 state = %q, want %q", st, channelvideos.StateQueued)
	}
	jobsList, _ := h.jobs.List()
	if len(jobsList) != 1 {
		t.Fatalf("jobs = %d, want 1", len(jobsList))
	}
	v, err := h.videos.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if v == nil {
		t.Fatal("no videos row was recreated; the download would have nothing to work from")
	}
	// Title and URL come off the ledger row, which is why parking keeps them.
	if v.URL == "" || v.Title == "" {
		t.Fatalf("videos row = %+v, want URL and title carried over from the ledger", v)
	}
}

// A probe that answers "still walled off" is evidence, and restamping is what
// spaces the next probe a window out — otherwise a long parked tail would be
// re-probed on every single pass.
func TestScan_parkedVideoOffListing_stillGated_restampsAndStaysParked(t *testing.T) {
	h := parkedHarness(t, false)
	dropFromListing(t, h)
	backdate(t, h, "v1")
	before, _ := h.ledger.Get("v1")
	prober := useProber(t, h, &fakeProber{availability: "subscriber_only"})

	scanAgain(t, h)

	if prober.calls != 1 {
		t.Fatalf("probed %d times, want 1", prober.calls)
	}
	if st := h.ledgerState("v1"); st != channelvideos.StateUnavailable {
		t.Fatalf("v1 state = %q, want it to stay %q", st, channelvideos.StateUnavailable)
	}
	after, _ := h.ledger.Get("v1")
	if after.UnavailableAt == before.UnavailableAt {
		t.Fatal("a confirmed gate did not restamp, so the next pass would re-probe immediately")
	}
	// The original reason survives: the probe confirmed the wall, it did not
	// re-diagnose why.
	if after.UnavailableReason != before.UnavailableReason {
		t.Fatalf("reason changed to %q, want %q kept", after.UnavailableReason, before.UnavailableReason)
	}
}

// A probe that produces no ANSWER (network trouble, the kill-switch) must
// change nothing at all. Restamping there would push the next real check out a
// full window per failure — the slow-motion version of burying the video, and
// the failure mode most likely to coincide with a broken yt-dlp.
func TestScan_parkedVideoOffListing_probeFailsTransiently_changesNothing(t *testing.T) {
	h := parkedHarness(t, false)
	dropFromListing(t, h)
	backdate(t, h, "v1")
	before, _ := h.ledger.Get("v1")
	useProber(t, h, &fakeProber{err: errors.New("network unreachable")})

	scanAgain(t, h)

	after, _ := h.ledger.Get("v1")
	if after.UnavailableAt != before.UnavailableAt {
		t.Fatalf("a failed probe restamped unavailable_at (%q -> %q), delaying the next real check",
			before.UnavailableAt, after.UnavailableAt)
	}
	if st := h.ledgerState("v1"); st != channelvideos.StateUnavailable {
		t.Fatalf("v1 state = %q, want %q", st, channelvideos.StateUnavailable)
	}
}

// The window still governs off-listing rows: a recently-parked video is not
// re-probed just because it left the listing.
func TestScan_parkedVideoOffListing_respectsTheRecheckWindow(t *testing.T) {
	h := parkedHarness(t, false)
	dropFromListing(t, h)
	// Deliberately NOT backdated.
	prober := useProber(t, h, &fakeProber{availability: "public"})

	scanAgain(t, h)

	if prober.calls != 0 {
		t.Fatalf("probed %d times inside the re-check window, want 0", prober.calls)
	}
	if st := h.ledgerState("v1"); st != channelvideos.StateUnavailable {
		t.Fatalf("v1 state = %q, want %q", st, channelvideos.StateUnavailable)
	}
}

// The off-listing pass shares the SAME per-pass budget as the listing loop,
// and runs after it. A channel with a long parked tail must not be able to
// turn one scan into an unbounded run of per-video yt-dlp calls.
func TestScan_parkedVideoOffListing_sharesTheOnePassProbeBudget(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", []string{"old"})
	ids := []string{"v1", "v2", "v3", "v4", "v5", "v6"}
	var gated []ytdlp.ChannelEntry
	for _, id := range ids {
		gated = append(gated, gatedEntry(id))
	}
	h.lister.set("UC1", gated)
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}

	// Every one of them now falls off the listing entirely and is eligible.
	dropFromListing(t, h)
	for _, id := range ids {
		backdate(t, h, id)
	}
	prober := useProber(t, h, &fakeProber{availability: "subscriber_only"})

	scanAgain(t, h)

	if prober.calls != maxUnavailableProbes {
		t.Fatalf("probed %d times off-listing, want the shared cap of %d", prober.calls, maxUnavailableProbes)
	}
}

// The listing loop gets first call on the budget: a video the channel still
// lists is the cheaper and likelier revival, so an off-listing tail must not
// starve it.
func TestScan_parkedVideoOffListing_listedRowsProbeFirst(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", []string{"old"})
	ids := []string{"v1", "v2", "v3", "v4", "v5", "v6"}
	var gated []ytdlp.ChannelEntry
	for _, id := range ids {
		gated = append(gated, gatedEntry(id))
	}
	h.lister.set("UC1", gated)
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		backdate(t, h, id)
	}
	// v6 alone is still listed, silently; the rest have fallen off.
	dropFromListing(t, h, silentEntry("v6"))
	useProber(t, h, &fakeProber{availability: "public"})

	scanAgain(t, h)

	// The still-listed one is probed within the budget and revived, whatever
	// the off-listing tail consumed after it.
	if st := h.ledgerState("v6"); st != channelvideos.StatePending {
		t.Fatalf("still-listed v6 state = %q, want %q — the off-listing tail starved it",
			st, channelvideos.StatePending)
	}
}

// A first pass has nothing parked from an earlier one, and its job is to
// snapshot what already exists rather than revive anything. Probing there
// would spend budget on a question that cannot have an answer yet.
func TestScan_baselinePass_doesNotProbeOffListing(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.lister.set("UC1", []ytdlp.ChannelEntry{silentEntry("v1")})
	prober := useProber(t, h, &fakeProber{availability: "public"})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}

	if prober.calls != 0 {
		t.Fatalf("baseline pass probed %d times, want 0", prober.calls)
	}
}

// Another channel's parked videos are not this channel's business: the query
// is scoped, or one scan would probe the whole library's parked tail.
func TestScan_parkedVideoOffListing_isScopedToTheChannel(t *testing.T) {
	h := parkedHarness(t, false)
	h.addAndSubscribe("UC2", false, "")
	h.markBaselined("UC2", []string{"old2"})
	dropFromListing(t, h)
	backdate(t, h, "v1")
	prober := useProber(t, h, &fakeProber{availability: "public"})

	// Scan UC2, which has nothing parked of its own.
	sub, err := h.channels.Get("UC2")
	if err != nil || sub == nil {
		t.Fatalf("get UC2: %v", err)
	}
	h.lister.set("UC2", []ytdlp.ChannelEntry{})
	if _, err := h.db.Exec(`UPDATE subscriptions SET next_scan_at = '2000-01-01 00:00:00' WHERE channel_id = 'UC2'`); err != nil {
		t.Fatal(err)
	}
	claimed, _ := h.channels.ClaimDue(h.nowStr())
	if claimed == nil || claimed.ChannelID != "UC2" {
		t.Skipf("claimed %v, expected UC2 — ordering is not this test's contract", claimed)
	}
	if err := h.sched.scanOnce(context.Background(), claimed); err != nil {
		t.Fatal(err)
	}

	if prober.calls != 0 {
		t.Fatalf("scanning UC2 probed %d of UC1's parked videos, want 0", prober.calls)
	}
	if st := h.ledgerState("v1"); st != channelvideos.StateUnavailable {
		t.Fatalf("v1 state = %q, want it untouched at %q", st, channelvideos.StateUnavailable)
	}
}
