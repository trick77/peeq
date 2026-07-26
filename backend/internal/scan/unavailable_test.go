package scan

import (
	"context"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/ytdlp"
)

// entryPublishedAt is late enough to clear the isBackCatalogue gate against the
// harness's baselined_at, so these tests exercise the availability branch
// rather than the back-catalogue one that sits above it.
const entryPublishedAt = "2026-07-19"

// gatedEntry is a listing entry yt-dlp flagged as members-only.
func gatedEntry(id string) ytdlp.ChannelEntry {
	return ytdlp.ChannelEntry{
		ID: id, Title: id, DurationSeconds: 600, LiveStatus: "not_live",
		PublishedAt: entryPublishedAt, Availability: "subscriber_only",
	}
}

// openEntry is the same video with yt-dlp explicitly saying it is public —
// the positive evidence that revives a parked row immediately.
func openEntry(id string) ytdlp.ChannelEntry {
	e := gatedEntry(id)
	e.Availability = "public"
	return e
}

// silentEntry is the common real-world shape: a flat listing that carries no
// availability at all, so it is evidence of nothing either way.
func silentEntry(id string) ytdlp.ChannelEntry {
	e := gatedEntry(id)
	e.Availability = ""
	return e
}

// fakeProber is a canned VideoProber: it answers with a fixed availability
// string, or a fixed error, and counts how many times it was asked.
type fakeProber struct {
	availability string
	err          error
	calls        int
}

func (p *fakeProber) Metadata(context.Context, string) (*ytdlp.Meta, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return &ytdlp.Meta{Availability: p.availability}, nil
}

// useProber installs a prober and returns it, rebuilding the scheduler so the
// new dep is picked up.
func useProber(t *testing.T, h *scanHarness, p *fakeProber) *fakeProber {
	t.Helper()
	h.sched.d.Prober = p
	return p
}

// backdate pushes a parked row's stamp past the re-check window, which is what
// makes a probe eligible. Done in SQL because unavailable_at is written by
// SQLite's clock, not the scheduler's injectable one.
func backdate(t *testing.T, h *scanHarness, videoID string) {
	t.Helper()
	parked := fixedNow.Add(-unavailableRecheckWindow - time.Hour)
	if _, err := h.db.Exec(
		`UPDATE channel_videos SET unavailable_at = ? WHERE video_id = ?`,
		parked.Format(sqlTimeLayout), videoID,
	); err != nil {
		t.Fatal(err)
	}
}

// scanAgain re-claims the subscription and runs another pass.
func scanAgain(t *testing.T, h *scanHarness) {
	t.Helper()
	if err := h.sched.scanOnce(context.Background(), h.forceDue()); err != nil {
		t.Fatal(err)
	}
}

// parkedHarness is the shared setup: a baselined channel with one gated video
// that the first pass parks as unavailable.
func parkedHarness(t *testing.T, autodownload bool) *scanHarness {
	t.Helper()
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", autodownload, "")
	h.markBaselined("UC1", []string{"old"})
	h.lister.set("UC1", []ytdlp.ChannelEntry{gatedEntry("v1")})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestScan_gatedEntry_neverReachesInbox(t *testing.T) {
	h := parkedHarness(t, false /*autodownload*/)

	if st := h.ledgerState("v1"); st != channelvideos.StateUnavailable {
		t.Fatalf("v1 state = %q, want %q", st, channelvideos.StateUnavailable)
	}
	// The whole point: the user is never offered a video that would fail the
	// moment they clicked Download.
	if p, _ := h.ledger.ListPending(); len(p) != 0 {
		t.Fatalf("pending = %d, want 0", len(p))
	}
	row, _ := h.ledger.Get("v1")
	if row.UnavailableReason != "members" {
		t.Fatalf("reason = %q, want members", row.UnavailableReason)
	}
	if row.UnavailableAt == "" {
		t.Fatal("unavailable_at must be stamped so the re-offer clock can start")
	}
}

func TestScan_gatedEntry_notQueuedOnAutodownloadChannel(t *testing.T) {
	h := parkedHarness(t, true /*autodownload*/)

	if st := h.ledgerState("v1"); st != channelvideos.StateUnavailable {
		t.Fatalf("v1 state = %q, want %q", st, channelvideos.StateUnavailable)
	}
	// Autodownload must not queue a guaranteed failure.
	if jobsList, _ := h.jobs.List(); len(jobsList) != 0 {
		t.Fatalf("jobs = %d, want 0", len(jobsList))
	}
}

// A listing that still shows the badge is fresh evidence, so it stays parked
// and spends no probe — the cheap signal is enough.
func TestScan_stillGatedInListing_staysParkedWithoutProbing(t *testing.T) {
	h := parkedHarness(t, false)
	backdate(t, h, "v1") // eligible for a probe, if one were warranted
	prober := useProber(t, h, &fakeProber{availability: "public"})

	scanAgain(t, h)

	if st := h.ledgerState("v1"); st != channelvideos.StateUnavailable {
		t.Fatalf("state = %q, want %q", st, channelvideos.StateUnavailable)
	}
	if prober.calls != 0 {
		t.Fatalf("probed %d times; a visible badge needs no probe", prober.calls)
	}
}

func TestScan_gateLifted_returnsToInbox(t *testing.T) {
	h := parkedHarness(t, false)
	h.lister.set("UC1", []ytdlp.ChannelEntry{openEntry("v1")})

	scanAgain(t, h)

	if st := h.ledgerState("v1"); st != "pending" {
		t.Fatalf("v1 state = %q, want pending", st)
	}
	pending, _ := h.ledger.ListPending()
	if len(pending) != 1 || pending[0].VideoID != "v1" {
		t.Fatalf("pending = %+v, want just v1", pending)
	}
	// The reason and its clock belong to the parked state only.
	row, _ := h.ledger.Get("v1")
	if row.UnavailableReason != "" || row.UnavailableAt != "" {
		t.Fatalf("revived row still carries %q/%q", row.UnavailableReason, row.UnavailableAt)
	}
}

func TestScan_gateLifted_autodownloadQueuesInsteadOfAsking(t *testing.T) {
	h := parkedHarness(t, true /*autodownload*/)
	h.lister.set("UC1", []ytdlp.ChannelEntry{openEntry("v1")})

	scanAgain(t, h)

	if st := h.ledgerState("v1"); st != "queued" {
		t.Fatalf("v1 state = %q, want queued", st)
	}
	jobsList, _ := h.jobs.List()
	if len(jobsList) != 1 {
		t.Fatalf("jobs = %d, want 1", len(jobsList))
	}
}

// The reported bug in miniature: a members-only video whose listing carries no
// badge. Silence must never revive it — that is what would put it back in the
// inbox on every pass, to fail again on every click.
func TestScan_silentListing_neverRevivesOnSilenceAlone(t *testing.T) {
	h := parkedHarness(t, false)
	h.lister.set("UC1", []ytdlp.ChannelEntry{silentEntry("v1")})
	backdate(t, h, "v1")
	// No prober wired: silence is all there is.

	scanAgain(t, h)

	if st := h.ledgerState("v1"); st != channelvideos.StateUnavailable {
		t.Fatalf("v1 state = %q, want it to stay %q", st, channelvideos.StateUnavailable)
	}
}

// ...and the same video must not be probed on every pass either, or a parked
// back catalogue would cost a yt-dlp call per video per scan.
func TestScan_silentListing_doesNotProbeBeforeTheWindow(t *testing.T) {
	h := parkedHarness(t, false)
	h.lister.set("UC1", []ytdlp.ChannelEntry{silentEntry("v1")})
	prober := useProber(t, h, &fakeProber{availability: "public"})

	scanAgain(t, h)

	if prober.calls != 0 {
		t.Fatalf("probed %d times inside the window, want 0", prober.calls)
	}
	if st := h.ledgerState("v1"); st != channelvideos.StateUnavailable {
		t.Fatalf("v1 state = %q, want %q", st, channelvideos.StateUnavailable)
	}
}

// The release case the whole state exists for: the channel makes the video
// public, the probe sees it, and it comes back to the inbox.
func TestScan_probeSaysPublic_returnsToInbox(t *testing.T) {
	h := parkedHarness(t, false)
	h.lister.set("UC1", []ytdlp.ChannelEntry{silentEntry("v1")})
	backdate(t, h, "v1")
	prober := useProber(t, h, &fakeProber{availability: "public"})

	scanAgain(t, h)

	if prober.calls != 1 {
		t.Fatalf("probed %d times, want 1", prober.calls)
	}
	if st := h.ledgerState("v1"); st != "pending" {
		t.Fatalf("v1 state = %q, want pending", st)
	}
}

// Still gated: the probe's answer is evidence too, so the clock resets and the
// next probe is a full window away.
func TestScan_probeSaysStillGated_restampsAndStaysParked(t *testing.T) {
	h := parkedHarness(t, false)
	h.lister.set("UC1", []ytdlp.ChannelEntry{silentEntry("v1")})
	backdate(t, h, "v1")
	before, _ := h.ledger.Get("v1")
	useProber(t, h, &fakeProber{availability: "subscriber_only"})

	scanAgain(t, h)

	after, _ := h.ledger.Get("v1")
	if after.State != channelvideos.StateUnavailable {
		t.Fatalf("state = %q, want %q", after.State, channelvideos.StateUnavailable)
	}
	if after.UnavailableAt == before.UnavailableAt {
		t.Fatal("a confirming probe must restamp, or every pass probes again")
	}
	if after.UnavailableReason != "members" {
		t.Fatalf("reason = %q, want the original members", after.UnavailableReason)
	}
}

// A terminal yt-dlp error is an answer — the wall is still there.
func TestScan_probeHitsTheWall_countsAsConfirmation(t *testing.T) {
	h := parkedHarness(t, false)
	h.lister.set("UC1", []ytdlp.ChannelEntry{silentEntry("v1")})
	backdate(t, h, "v1")
	before, _ := h.ledger.Get("v1")
	useProber(t, h, &fakeProber{err: &ytdlp.TerminalError{Reason: "members"}})

	scanAgain(t, h)

	after, _ := h.ledger.Get("v1")
	if after.State != channelvideos.StateUnavailable {
		t.Fatalf("state = %q, want %q", after.State, channelvideos.StateUnavailable)
	}
	if after.UnavailableAt == before.UnavailableAt {
		t.Fatal("a terminal probe answer must restamp")
	}
}

// A transient failure is NOT an answer. Restamping on it would push the next
// real check out by a fortnight every time the network hiccuped — silent
// burial by accident.
func TestScan_probeFailsTransiently_changesNothing(t *testing.T) {
	h := parkedHarness(t, false)
	h.lister.set("UC1", []ytdlp.ChannelEntry{silentEntry("v1")})
	backdate(t, h, "v1")
	before, _ := h.ledger.Get("v1")
	useProber(t, h, &fakeProber{err: ytdlp.ErrBlocked})

	scanAgain(t, h)

	after, _ := h.ledger.Get("v1")
	if after.State != channelvideos.StateUnavailable {
		t.Fatalf("state = %q, want %q", after.State, channelvideos.StateUnavailable)
	}
	if after.UnavailableAt != before.UnavailableAt {
		t.Fatalf("stamp moved on a non-answer: %q -> %q", before.UnavailableAt, after.UnavailableAt)
	}
}

// The per-pass budget bounds a channel with a large walled-off back catalogue.
func TestScan_probeBudgetCapsOnePass(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", []string{"old"})
	var gated []ytdlp.ChannelEntry
	for _, id := range []string{"v1", "v2", "v3", "v4", "v5"} {
		gated = append(gated, gatedEntry(id))
	}
	h.lister.set("UC1", gated)
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	// Re-list them without badges and make every one probe-eligible.
	var silent []ytdlp.ChannelEntry
	for _, e := range gated {
		silent = append(silent, silentEntry(e.ID))
	}
	h.lister.set("UC1", silent)
	for _, e := range gated {
		backdate(t, h, e.ID)
	}
	prober := useProber(t, h, &fakeProber{availability: "subscriber_only"})

	scanAgain(t, h)

	if prober.calls != maxUnavailableProbes {
		t.Fatalf("probed %d times, want the cap of %d", prober.calls, maxUnavailableProbes)
	}
}

func TestScan_recheckDue_missingStampErrsTowardOffering(t *testing.T) {
	h := newScanHarness(t)
	// Silent burial is the failure mode this state exists to prevent, so a row
	// with no usable stamp must come back rather than sit forever.
	if !h.sched.recheckDue("") {
		t.Fatal("empty stamp must be due")
	}
	if !h.sched.recheckDue("not a timestamp") {
		t.Fatal("unparsable stamp must be due")
	}
	if h.sched.recheckDue(fixedNow.Format(sqlTimeLayout)) {
		t.Fatal("a row parked just now must not be due")
	}
}

func TestScan_gateChanged_reparksUnderTheNewReason(t *testing.T) {
	h := parkedHarness(t, false)
	premium := gatedEntry("v1")
	premium.Availability = "premium_only"
	h.lister.set("UC1", []ytdlp.ChannelEntry{premium})

	scanAgain(t, h)

	row, _ := h.ledger.Get("v1")
	if row.State != channelvideos.StateUnavailable {
		t.Fatalf("state = %q, want %q", row.State, channelvideos.StateUnavailable)
	}
	if row.UnavailableReason != "premium" {
		t.Fatalf("reason = %q, want premium", row.UnavailableReason)
	}
}

// The duration floor is matched before the availability branch on purpose: a
// gated video that is also too short gets the cheaper terminal 'seen' and is
// never re-checked again.
func TestScan_gatedButTooShort_staysSeen(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", []string{"old"})
	floor := 300
	if err := h.settings.Update(context.Background(), settings.Patch{MinVideoDurationSeconds: &floor}); err != nil {
		t.Fatal(err)
	}
	short := gatedEntry("v1")
	short.DurationSeconds = 30
	h.lister.set("UC1", []ytdlp.ChannelEntry{short})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if st := h.ledgerState("v1"); st != "seen" {
		t.Fatalf("v1 state = %q, want seen", st)
	}
}
