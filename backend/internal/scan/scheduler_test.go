package scan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// fixedNow is the harness's frozen wall clock, so next_scan_at / backoff math
// is deterministic across tests.
var fixedNow = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

// fakeLister is a canned ChannelLister: it records how many times it was
// called and returns pre-seeded entries per ucid, per tab. When panicMsg is set
// it panics instead (exercising the scan panic guard). It is safe for
// concurrent use so the goroutine-driven no-cookie test stays race-clean.
//
// The /streams tab defaults to failing the way yt-dlp fails for a channel that
// has never gone live, so every test that seeds only uploads exercises the
// common real-world shape rather than an artificial empty streams tab.
type fakeLister struct {
	mu          sync.Mutex
	entries     map[string][]ytdlp.ChannelEntry
	streams     map[string][]ytdlp.ChannelEntry
	calls       int
	streamCalls int
	panicMsg    string
	// err, when set, is returned instead of entries — a plain (unclassified)
	// listing failure, which is what the default branch of scanChannel's error
	// classification handles.
	err error
	// streamErr is the same seam for the /streams tab, and defaults to yt-dlp's
	// missing-tab failure rather than nil (see newFakeLister).
	streamErr error
}

func newFakeLister() *fakeLister {
	return &fakeLister{
		entries: map[string][]ytdlp.ChannelEntry{},
		streams: map[string][]ytdlp.ChannelEntry{},
		streamErr: &ytdlp.ExecError{
			Err:    errors.New("exit status 1"),
			Stderr: "ERROR: [youtube:tab] UC1: This channel does not have a streams tab",
		},
	}
}

func (f *fakeLister) set(ucid string, e []ytdlp.ChannelEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[ucid] = e
}

// setStreams seeds the /streams tab for a ucid and clears the default
// missing-tab error — i.e. makes this a channel that does stream.
func (f *fakeLister) setStreams(ucid string, e []ytdlp.ChannelEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streams[ucid] = e
	f.streamErr = nil
}

func (f *fakeLister) ChannelVideos(_ context.Context, ucid string, _ int) ([]ytdlp.ChannelEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.panicMsg != "" {
		panic(f.panicMsg)
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.entries[ucid], nil
}

func (f *fakeLister) ChannelStreams(_ context.Context, ucid string, _ int) ([]ytdlp.ChannelEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.streamCalls++
	if f.panicMsg != "" {
		panic(f.panicMsg)
	}
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	return f.streams[ucid], nil
}

// fakeRecorder captures the activity rows a scan pass writes, so a test can
// assert on the feed the user actually sees. Concurrency-safe for the same
// reason fakeLister is: the goroutine-driven tests share it.
type fakeRecorder struct {
	mu     sync.Mutex
	events []activity.Event
}

func (r *fakeRecorder) Record(e activity.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// scanEvents returns the captured rows of kind "scan".
func (r *fakeRecorder) scanEvents() []activity.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []activity.Event
	for _, e := range r.events {
		if e.Kind == activity.KindScan {
			out = append(out, e)
		}
	}
	return out
}

// failOnceJobs wraps a real jobs store and fails the first Enqueue, then
// delegates — a transient enqueue failure for the reorder/self-heal test.
type failOnceJobs struct {
	inner  *jobs.Store
	failed bool
}

func (f *failOnceJobs) Enqueue(videoID string, priority int) (int64, error) {
	if !f.failed {
		f.failed = true
		return 0, fmt.Errorf("transient enqueue failure")
	}
	return f.inner.Enqueue(videoID, priority)
}

// scanHarness wires a migrated temp DB, all five stores over that same DB, a
// fake Lister, and a Scheduler. cookieStatus is what the injected
// CookieStatus func returns (default "valid"); tests flip it to exercise the
// cookie gate.
type scanHarness struct {
	t            *testing.T
	db           *sql.DB
	channels     *channels.Store
	ledger       *channelvideos.Store
	videos       *videos.Store
	jobs         *jobs.Store
	settings     *settings.Store
	lister       *fakeLister
	activity     *fakeRecorder
	sched        *Scheduler
	cookieStatus string
}

func newScanHarness(t *testing.T) *scanHarness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	h := &scanHarness{
		t:            t,
		db:           db,
		channels:     channels.New(db),
		ledger:       channelvideos.New(db),
		videos:       videos.New(db),
		jobs:         jobs.New(db),
		settings:     settings.New(db),
		lister:       newFakeLister(),
		activity:     &fakeRecorder{},
		cookieStatus: "valid",
	}
	h.sched = h.buildSched(h.jobs)
	return h
}

// buildSched constructs a Scheduler over the harness's stores with the given
// job enqueuer and the frozen clock.
func (h *scanHarness) buildSched(j JobEnqueuer) *Scheduler {
	return New(Deps{
		Channels:     h.channels,
		Ledger:       h.ledger,
		Videos:       h.videos,
		Jobs:         j,
		Settings:     h.settings,
		Lister:       h.lister,
		CookieStatus: func(context.Context) string { return h.cookieStatus },
		Activity:     h.activity,
		Now:          func() time.Time { return fixedNow },
		PollInterval: 5 * time.Millisecond,
	})
}

// useJobs rebuilds h.sched with a custom job enqueuer (e.g. a failing fake).
func (h *scanHarness) useJobs(j JobEnqueuer) {
	h.sched = h.buildSched(j)
}

// nowStr is the harness's fixed clock in SQLite text form.
func (h *scanHarness) nowStr() string {
	return fixedNow.Format(sqlTimeLayout)
}

// addAndSubscribe adds ucid and subscribes it with a next_scan_at in the
// past, so the first ClaimDue(nowStr) finds it. Mirrors the real API flow
// (MarkAdded before Subscribe — subscribing requires an already-added
// channel), so the harness state matches what enqueueAuto's added_at guard
// sees in production.
func (h *scanHarness) addAndSubscribe(ucid string, autodownload bool, format string) {
	h.t.Helper()
	if err := h.channels.Upsert(channels.Channel{ID: ucid, Name: ucid}); err != nil {
		h.t.Fatalf("add %s: %v", ucid, err)
	}
	if err := h.channels.MarkAdded(ucid, h.nowStr()); err != nil {
		h.t.Fatalf("add %s: %v", ucid, err)
	}
	if err := h.channels.Subscribe(ucid, "2000-01-01 00:00:00"); err != nil {
		h.t.Fatalf("subscribe %s: %v", ucid, err)
	}
	if _, _, _, err := h.channels.UpdateConfig(ucid, &autodownload, &format); err != nil {
		h.t.Fatalf("config %s: %v", ucid, err)
	}
}

// markBaselined seeds seenIDs as 'seen' ledger rows and stamps baselined_at
// directly, WITHOUT advancing next_scan_at — so the subscription stays due
// and the next ClaimDue(nowStr) still returns it.
func (h *scanHarness) markBaselined(ucid string, seenIDs []string) {
	h.t.Helper()
	for _, id := range seenIDs {
		if err := h.ledger.Insert(channelvideos.Entry{
			VideoID: id, ChannelID: ucid, State: "seen",
		}); err != nil {
			h.t.Fatalf("seed seen %s: %v", id, err)
		}
	}
	if _, err := h.db.Exec(
		`UPDATE subscriptions SET baselined_at = ? WHERE channel_id = ?`,
		h.nowStr(), ucid,
	); err != nil {
		h.t.Fatalf("stamp baselined_at %s: %v", ucid, err)
	}
}

// forceDue pushes next_scan_at back into the past and re-claims the
// subscription, so a test can run a second scanOnce over the same channel.
func (h *scanHarness) forceDue() *channels.Subscription {
	h.t.Helper()
	if err := h.channels.Backoff("UC1", "2000-01-01 00:00:00"); err != nil {
		h.t.Fatalf("force due: %v", err)
	}
	sub, err := h.channels.ClaimDue(h.nowStr())
	if err != nil || sub == nil {
		h.t.Fatalf("claim due again: sub=%+v err=%v", sub, err)
	}
	return sub
}

// ledgerState returns the ledger state for videoID (fails the test if absent).
func (h *scanHarness) ledgerState(videoID string) string {
	h.t.Helper()
	e, err := h.ledger.Get(videoID)
	if err != nil {
		h.t.Fatalf("get ledger %s: %v", videoID, err)
	}
	if e == nil {
		h.t.Fatalf("ledger row for %s missing", videoID)
	}
	return e.State
}

// ledgerStateOrAbsent is ledgerState for assertions where "no row at all" is a
// legitimate outcome — an unfinished stream is deliberately not recorded, so the
// absence itself is the thing under test. Returns "" when there is no row.
func (h *scanHarness) ledgerStateOrAbsent(videoID string) string {
	h.t.Helper()
	e, err := h.ledger.Get(videoID)
	if err != nil {
		h.t.Fatalf("get ledger %s: %v", videoID, err)
	}
	if e == nil {
		return ""
	}
	return e.State
}

func TestScan_firstRunBaseline_queuesNothing(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false /*autodownload*/, "" /*format*/)
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "v1", Title: "A", DurationSeconds: 600, LiveStatus: "not_live"},
		{ID: "v2", Title: "B", DurationSeconds: 600, LiveStatus: "not_live"},
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	// Baseline: both recorded as 'seen', nothing pending, no jobs enqueued.
	if p, _ := h.ledger.ListPending(); len(p) != 0 {
		t.Fatalf("baseline pending = %d, want 0", len(p))
	}
	if jobsList, _ := h.jobs.List(); len(jobsList) != 0 {
		t.Fatalf("baseline jobs = %d, want 0", len(jobsList))
	}
	if st := h.ledgerState("v1"); st != "seen" {
		t.Fatalf("v1 state = %q, want seen", st)
	}
	if st := h.ledgerState("v2"); st != "seen" {
		t.Fatalf("v2 state = %q, want seen", st)
	}
	// baselined_at now set.
	sub2, _ := h.channels.ClaimDue("2999-01-01 00:00:00")
	if sub2 != nil && sub2.BaselinedAt == "" {
		t.Fatal("baselined_at must be set after first scan")
	}
}

func TestScan_subsequentNewVideo_pendingVsAutodownload(t *testing.T) {
	// Non-autodownload: new id after baseline → pending.
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", []string{"old1"}) // seed ledger 'seen' + baselined_at set
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "old1", DurationSeconds: 600, LiveStatus: "not_live"},  // dedup: skip
		{ID: "newp", DurationSeconds: 600, LiveStatus: "not_live"},  // NEW → pending
		{ID: "short", DurationSeconds: 60, LiveStatus: "not_live"},  // <180s → seen
		{ID: "up", DurationSeconds: 600, LiveStatus: "is_upcoming"}, // upcoming → no row at all
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	p, _ := h.ledger.ListPending()
	if len(p) != 1 || p[0].VideoID != "newp" {
		t.Fatalf("pending = %+v, want [newp]", p)
	}
	// A duration-filtered id must be 'seen' specifically (not merely non-pending:
	// 'ignored' would also pass ListPending but means something else). 'seen' is
	// terminal, which is correct here — a 60s video will not grow longer.
	if st := h.ledgerState("short"); st != "seen" {
		t.Fatalf("short state = %q, want seen", st)
	}
	// An unfinished stream must leave NO ledger row: 'seen' is terminal, so
	// recording one would lose the video permanently once the stream ended.
	if st := h.ledgerStateOrAbsent("up"); st != "" {
		t.Fatalf("upcoming state = %q, want no row", st)
	}
	if jobsList, _ := h.jobs.List(); len(jobsList) != 0 {
		t.Fatalf("non-autodownload must not enqueue; got %d jobs", len(jobsList))
	}
}

// TestScan_tabsOverlappingOnAnIdCountItOnce: nothing guarantees YouTube keeps
// the two tabs disjoint forever, and an id listed twice would be offered twice.
func TestScan_tabsOverlappingOnAnIdCountItOnce(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	both := ytdlp.ChannelEntry{ID: "vod00001", Title: "On both tabs", DurationSeconds: 7200, LiveStatus: "was_live"}
	h.lister.set("UC1", []ytdlp.ChannelEntry{both})
	h.lister.setStreams("UC1", []ytdlp.ChannelEntry{both})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	p, _ := h.ledger.ListPending()
	if len(p) != 1 || p[0].VideoID != "vod00001" {
		t.Fatalf("pending = %+v, want the id exactly once", p)
	}
}

// TestScan_completedLivestreamIsOffered proves the point of listing /streams at
// all: a finished stream VOD reaches the inbox exactly like an upload, even
// though it never appears on the /videos tab.
func TestScan_completedLivestreamIsOffered(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "upload01", DurationSeconds: 600, LiveStatus: "not_live"},
	})
	h.lister.setStreams("UC1", []ytdlp.ChannelEntry{
		{ID: "vod00001", Title: "Sunday stream", DurationSeconds: 7200, LiveStatus: "was_live"},
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if st := h.ledgerState("vod00001"); st != "pending" {
		t.Fatalf("completed stream state = %q, want pending", st)
	}
	if st := h.ledgerState("upload01"); st != "pending" {
		t.Fatalf("upload state = %q, want pending", st)
	}
}

// TestScan_unfinishedStreamDeferredThenOffered is the whole reason live entries
// get no ledger row: the same id is skipped while it is airing and picked up on
// the next scan, once it has become a VOD. A 'seen' row on the first pass would
// have masked it forever.
func TestScan_unfinishedStreamDeferredThenOffered(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	h.lister.setStreams("UC1", []ytdlp.ChannelEntry{
		{ID: "vod00001", Title: "Live now", DurationSeconds: 0, LiveStatus: "is_live"},
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if st := h.ledgerStateOrAbsent("vod00001"); st != "" {
		t.Fatalf("airing stream state = %q, want no row", st)
	}
	if p, _ := h.ledger.ListPending(); len(p) != 0 {
		t.Fatalf("airing stream must not be offered; got %+v", p)
	}

	// Same id, now finished.
	h.lister.setStreams("UC1", []ytdlp.ChannelEntry{
		{ID: "vod00001", Title: "Live now", DurationSeconds: 5400, LiveStatus: "was_live"},
	})
	sub2 := h.forceDue()
	if err := h.sched.scanOnce(context.Background(), sub2); err != nil {
		t.Fatal(err)
	}
	if st := h.ledgerState("vod00001"); st != "pending" {
		t.Fatalf("finished stream state = %q, want pending", st)
	}
}

// TestScan_postLiveStreamDeferred covers the window where a stream has ended but
// YouTube has not finished cutting the VOD: not recordable yet, and — like the
// airing case — left without a row so a later scan can offer the finished cut.
func TestScan_postLiveStreamDeferred(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", true /*autodownload*/, "")
	h.markBaselined("UC1", nil)
	h.lister.setStreams("UC1", []ytdlp.ChannelEntry{
		{ID: "vod00001", DurationSeconds: 3600, LiveStatus: "post_live"},
		{ID: "vod00002", DurationSeconds: 3600, LiveStatus: "some_future_status"},
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"vod00001", "vod00002"} {
		if st := h.ledgerStateOrAbsent(id); st != "" {
			t.Fatalf("%s state = %q, want no row", id, st)
		}
	}
	if jobsList, _ := h.jobs.List(); len(jobsList) != 0 {
		t.Fatalf("unsettled streams must not be downloaded; got %d jobs", len(jobsList))
	}
}

// TestScan_baselineCoversStreamsTab proves the baseline snapshot spans both
// tabs. Without this, subscribing to a channel that streams would treat its
// whole back catalogue of VODs as new on the second scan.
func TestScan_baselineCoversStreamsTab(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.lister.set("UC1", []ytdlp.ChannelEntry{{ID: "upload01", DurationSeconds: 600, LiveStatus: "not_live"}})
	h.lister.setStreams("UC1", []ytdlp.ChannelEntry{{ID: "vod00001", DurationSeconds: 7200, LiveStatus: "was_live"}})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if st := h.ledgerState("vod00001"); st != "seen" {
		t.Fatalf("baselined stream state = %q, want seen", st)
	}
	if p, _ := h.ledger.ListPending(); len(p) != 0 {
		t.Fatalf("baseline must queue nothing; got %+v", p)
	}
}

// TestScan_baseline_streamsFailure_doesNotBaselineHalfAChannel: a baseline is
// the ONE listing that must be complete — every id it fails to see counts as
// new on the next pass. Swallowing a transient /streams failure here would
// stamp baselined_at from an uploads-only snapshot and then dump the channel's
// whole back catalogue of VODs into the inbox on the following scan.
func TestScan_baseline_streamsFailure_doesNotBaselineHalfAChannel(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.lister.set("UC1", []ytdlp.ChannelEntry{{ID: "upload01", DurationSeconds: 600, LiveStatus: "not_live"}})
	h.lister.setStreams("UC1", nil)
	h.lister.streamErr = errors.New("some transient yt-dlp hiccup")

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err == nil {
		t.Fatal("a baseline pass must fail rather than snapshot half a channel")
	}
	if st := h.ledgerStateOrAbsent("upload01"); st != "" {
		t.Fatalf("failed baseline must record nothing; upload01 = %q", st)
	}
	// Still unbaselined, so a later pass gets to take the full snapshot.
	sub2, _ := h.channels.ClaimDue("2999-01-01 00:00:00")
	if sub2 != nil && sub2.BaselinedAt != "" {
		t.Fatalf("baselined_at = %q, want unset after a failed baseline", sub2.BaselinedAt)
	}
}

// TestScan_baseline_missingStreamsTab_stillBaselines: the strictness above is
// about UNCERTAINTY, not about the streams call failing. A channel that has
// never gone live has no tab to list and nothing to miss, so the common case
// must still baseline on the first pass.
func TestScan_baseline_missingStreamsTab_stillBaselines(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	// The harness lister already errors on /streams the way yt-dlp does.
	h.lister.set("UC1", []ytdlp.ChannelEntry{{ID: "upload01", DurationSeconds: 600, LiveStatus: "not_live"}})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatalf("a missing streams tab must not fail the baseline: %v", err)
	}
	if st := h.ledgerState("upload01"); st != "seen" {
		t.Fatalf("upload01 state = %q, want seen", st)
	}
	sub2, _ := h.channels.ClaimDue("2999-01-01 00:00:00")
	if sub2 != nil && sub2.BaselinedAt == "" {
		t.Fatal("baselined_at must be set after the first scan")
	}
}

// TestScan_missingVideosTab_streamOnlyChannelStillScans is the case this whole
// feature exists for, taken to its limit: a channel that publishes ONLY
// livestreams has no /videos tab at all, and yt-dlp refuses that call exactly
// the way it refuses /streams elsewhere. Treating it as fatal would return
// before the streams call and leave such a channel permanently unscannable.
func TestScan_missingVideosTab_streamOnlyChannelStillScans(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	h.lister.err = &ytdlp.ExecError{
		Err:    errors.New("exit status 1"),
		Stderr: "ERROR: [youtube:tab] UC1: This channel does not have a videos tab",
	}
	h.lister.setStreams("UC1", []ytdlp.ChannelEntry{
		{ID: "vod00001", Title: "Sunday stream", DurationSeconds: 7200, LiveStatus: "was_live"},
	})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatalf("a missing videos tab must not fail the scan: %v", err)
	}
	if h.lister.streamCalls != 1 {
		t.Fatalf("streams calls = %d, want 1 — the streams tab must still be listed", h.lister.streamCalls)
	}
	if st := h.ledgerState("vod00001"); st != "pending" {
		t.Fatalf("stream state = %q, want pending", st)
	}
}

// TestScan_missingStreamsTab_scanStillSucceeds covers the majority of channels:
// they have never gone live, so yt-dlp fails the /streams call outright. That
// must not fail the scan or cost the channel its uploads.
func TestScan_missingStreamsTab_scanStillSucceeds(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	// The harness lister already errors on /streams the way yt-dlp does.
	h.lister.set("UC1", []ytdlp.ChannelEntry{{ID: "newp", DurationSeconds: 600, LiveStatus: "not_live"}})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatalf("a missing streams tab must not fail the scan: %v", err)
	}
	if st := h.ledgerState("newp"); st != "pending" {
		t.Fatalf("newp state = %q, want pending", st)
	}
}

// TestScan_streamsTabError_toleratedExceptSentinels: an unrecognised /streams
// failure is tolerated (uploads still land), but a bot-block or dead cookie is
// account-wide news and must fail the scan so the cookie status gets flipped.
func TestScan_streamsTabError_toleratedExceptSentinels(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		wantErr bool
	}{
		{"unrecognised", errors.New("some transient yt-dlp hiccup"), false},
		{"blocked", ytdlp.ErrBlocked, true},
		{"cookie expired", ytdlp.ErrCookieExpired, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newScanHarness(t)
			h.addAndSubscribe("UC1", false, "")
			h.markBaselined("UC1", nil)
			h.lister.set("UC1", []ytdlp.ChannelEntry{{ID: "newp", DurationSeconds: 600, LiveStatus: "not_live"}})
			h.lister.setStreams("UC1", nil)
			h.lister.streamErr = tc.err

			sub, _ := h.channels.ClaimDue(h.nowStr())
			err := h.sched.scanOnce(context.Background(), sub)
			if tc.wantErr {
				if !errors.Is(err, tc.err) {
					t.Fatalf("scanOnce err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("streams failure must not fail the scan: %v", err)
			}
			if st := h.ledgerState("newp"); st != "pending" {
				t.Fatalf("newp state = %q, want pending", st)
			}
		})
	}
}

// TestScan_uploadsFailure_skipsStreamsCall keeps the throttle budget honest: a
// channel whose first call already failed must not spend a second one.
func TestScan_uploadsFailure_skipsStreamsCall(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	h.lister.err = errors.New("listing failed")

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err == nil {
		t.Fatal("want error when the uploads listing fails")
	}
	if h.lister.streamCalls != 0 {
		t.Fatalf("streams calls = %d, want 0", h.lister.streamCalls)
	}
}

func TestScan_autodownloadEnqueuesWithFormatOverride(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", true /*autodownload*/, "bestvideo+bestaudio")
	h.markBaselined("UC1", nil)
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "newv", Title: "N", URL: "https://www.youtube.com/watch?v=newv", DurationSeconds: 600, LiveStatus: "not_live"},
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	// videos row upserted + status queued + requested_format set + job enqueued at priority 0.
	v, _ := h.videos.Get("newv")
	if v == nil || v.Status != "queued" || v.RequestedFormat != "bestvideo+bestaudio" {
		t.Fatalf("video = %+v", v)
	}
	jobsList, _ := h.jobs.List()
	if len(jobsList) != 1 || jobsList[0].Priority != 0 {
		t.Fatalf("jobs = %+v, want one at priority 0", jobsList)
	}
	// ledger marked queued (not pending).
	if p, _ := h.ledger.ListPending(); len(p) != 0 {
		t.Fatalf("autodownloaded id must not be pending; got %+v", p)
	}
	if st := h.ledgerState("newv"); st != "queued" {
		t.Fatalf("newv state = %q, want queued", st)
	}
}

// TestScan_autodownloadEnqueueFailure_notMaskedByLedger proves the write
// reorder: when enqueueAuto fails, no ledger row is written (so the id is not
// permanently masked by an invisible 'queued' ledger row); the Upserted video
// row remains, keeping the half-done id visible and catchable by the
// videos-table dedup on the next scan.
func TestScan_autodownloadEnqueueFailure_notMaskedByLedger(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", true, "bestvideo+bestaudio")
	h.markBaselined("UC1", nil)
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "newv", Title: "N", URL: "https://www.youtube.com/watch?v=newv", DurationSeconds: 600, LiveStatus: "not_live"},
	})
	h.useJobs(&failOnceJobs{inner: h.jobs}) // Enqueue fails once

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err == nil {
		t.Fatal("expected scanOnce to surface the transient enqueue failure")
	}
	// The ledger must NOT hold a masking row for the failed id.
	if ok, _ := h.ledger.Exists("newv"); ok {
		t.Fatal("failed enqueue must not leave a masking ledger row")
	}
	// The video row exists (enqueueAuto Upserts before enqueuing), so the id
	// is visible and the next scan's videos-dedup catches it.
	if v, _ := h.videos.Get("newv"); v == nil {
		t.Fatal("enqueueAuto should have Upserted the video row before failing")
	}
	// Next scan (enqueue now works) must dedup via the videos table — no
	// ledger 'queued' row resurrected.
	sub2, _ := h.channels.ClaimDue(h.nowStr())
	if sub2 == nil {
		t.Fatal("subscription should still be due after a failed scan (no MarkScanned)")
	}
	if err := h.sched.scanOnce(context.Background(), sub2); err != nil {
		t.Fatal(err)
	}
	if ok, _ := h.ledger.Exists("newv"); ok {
		t.Fatal("retry must dedup via videos table, not create a ledger row")
	}
}

// TestScan_autodownload_notEnqueued_whenChannelNotAdded proves enqueueAuto's
// guard checks added_at, not mere row presence. channels is a metadata
// cache: maybeResolveChannel can re-create a cache-only row (added_at
// NULL) for an id the user just not-added/deleted, while a scan for its
// still-due subscription is in flight. Get() != nil alone would look
// identical to a genuinely added channel, so a scan should NOT enqueue a
// download for a channel whose row exists but isn't added.
func TestScan_autodownload_notEnqueued_whenChannelNotAdded(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", true /*autodownload*/, "")
	h.markBaselined("UC1", nil)
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "newv", Title: "N", URL: "https://www.youtube.com/watch?v=newv", DurationSeconds: 600, LiveStatus: "not_live"},
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if sub == nil {
		t.Fatal("expected a due subscription")
	}

	// Simulate the race: the channel is not-added (row reverts to cache-only)
	// after ClaimDue but before the scan's enqueueAuto call. The subscription
	// row is left dangling, mirroring maybeResolveChannel re-creating a
	// cache-only row for a just-deleted/not-added channel mid-scan.
	if _, err := h.db.Exec(`UPDATE channels SET added_at = NULL WHERE id = ?`, "UC1"); err != nil {
		t.Fatalf("un-add UC1: %v", err)
	}

	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if jobsList, _ := h.jobs.List(); len(jobsList) != 0 {
		t.Fatalf("must not enqueue a download for an not-added channel; got %+v", jobsList)
	}
	if v, _ := h.videos.Get("newv"); v != nil {
		t.Fatalf("must not upsert a video row for an not-added channel; got %+v", v)
	}
}

// TestScan_panicDuringScan_backsOff proves the panic guard backs the
// subscription off (bounding a persistently-panicking channel to ~1/hour)
// without clearing its baseline.
func TestScan_panicDuringScan_backsOff(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil) // baselined_at set
	h.lister.panicMsg = "boom"  // ChannelVideos panics

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if sub == nil {
		t.Fatal("expected a due subscription")
	}
	// Must not propagate the panic; must back off internally.
	h.sched.scanChannel(context.Background(), sub)

	// Backed off: no longer due at now (next_scan_at pushed ~1h out).
	if s2, _ := h.channels.ClaimDue(h.nowStr()); s2 != nil {
		t.Fatalf("panic must back the subscription off; still due: %+v", s2)
	}
	// Baseline preserved.
	s3, _ := h.channels.ClaimDue("2999-01-01 00:00:00")
	if s3 == nil || s3.BaselinedAt == "" {
		t.Fatalf("backoff must not clear baselined_at; sub=%+v", s3)
	}
}

// errLister is a ChannelLister that always returns a fixed error, exercising
// the scan error path (cookie-status flip + backoff).
type errLister struct{ err error }

func (l errLister) ChannelVideos(context.Context, string, int) ([]ytdlp.ChannelEntry, error) {
	return nil, l.err
}

func (l errLister) ChannelStreams(context.Context, string, int) ([]ytdlp.ChannelEntry, error) {
	return nil, l.err
}

// TestScan_blockedCookie_flipsStatus proves FIX 1: when a SCAN surfaces
// ytdlp.ErrBlocked (a bot-block, not a download), the scheduler flips
// cookie_status to "blocked" so its own cookie gate trips on the next pass and
// stops hammering YouTube on a dead cookie.
func TestScan_blockedCookie_flipsStatus(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	// Seed cookie_status="valid" via the status-only path (empty text skips
	// cookie validation), so we can observe the flip away from "valid".
	if err := h.settings.SetCookie(context.Background(), "", "valid"); err != nil {
		t.Fatalf("seed cookie status: %v", err)
	}
	h.sched = New(Deps{
		Channels: h.channels, Ledger: h.ledger, Videos: h.videos, Jobs: h.jobs,
		Settings:     h.settings,
		Lister:       errLister{err: ytdlp.ErrBlocked},
		CookieStatus: func(context.Context) string { return h.cookieStatus },
		Now:          func() time.Time { return fixedNow },
		PollInterval: 5 * time.Millisecond,
	})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if sub == nil {
		t.Fatal("expected a due subscription")
	}
	h.sched.scanChannel(context.Background(), sub)

	if st := h.settings.CookieStatus(context.Background()); st != "blocked" {
		t.Fatalf("cookie_status = %q, want blocked (gate must trip next pass)", st)
	}
}

// TestScan_expiredCookie_flipsStale proves the ErrCookieExpired branch of FIX
// 1 flips cookie_status to "stale".
func TestScan_expiredCookie_flipsStale(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	if err := h.settings.SetCookie(context.Background(), "", "valid"); err != nil {
		t.Fatalf("seed cookie status: %v", err)
	}
	h.sched = New(Deps{
		Channels: h.channels, Ledger: h.ledger, Videos: h.videos, Jobs: h.jobs,
		Settings:     h.settings,
		Lister:       errLister{err: ytdlp.ErrCookieExpired},
		CookieStatus: func(context.Context) string { return h.cookieStatus },
		Now:          func() time.Time { return fixedNow },
		PollInterval: 5 * time.Millisecond,
	})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	h.sched.scanChannel(context.Background(), sub)

	if st := h.settings.CookieStatus(context.Background()); st != "stale" {
		t.Fatalf("cookie_status = %q, want stale", st)
	}
}

// fakeMonitor is a FailMonitor test double: it records Fail/Reset calls via
// caller-supplied callbacks (nil callbacks are simply no-ops). Mirrors the
// download package's test double of the same name/shape.
type fakeMonitor struct {
	onFail  func(string)
	onReset func()
}

func (f *fakeMonitor) Fail(id string) {
	if f.onFail != nil {
		f.onFail(id)
	}
}

func (f *fakeMonitor) Reset() {
	if f.onReset != nil {
		f.onReset()
	}
}

// TestScanSkipsWhilePaused proves the youtube_paused kill-switch gate: when
// YoutubePaused returns true, Run must never invoke the Lister, mirroring the
// existing cookie-gate test (TestScan_noCookie_skipsScan) above.
func TestScanSkipsWhilePaused(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.lister.set("UC1", []ytdlp.ChannelEntry{{ID: "v1", DurationSeconds: 600}})
	h.sched = New(Deps{
		Channels: h.channels, Ledger: h.ledger, Videos: h.videos, Jobs: h.jobs,
		Settings:      h.settings,
		Lister:        h.lister,
		CookieStatus:  func(context.Context) string { return h.cookieStatus },
		YoutubePaused: func(context.Context) bool { return true },
		Now:           func() time.Time { return fixedNow },
		PollInterval:  5 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sched.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	// Lister was never called (paused): nothing recorded.
	if ok, _ := h.ledger.Exists("v1"); ok {
		t.Fatal("must not scan while youtube is paused")
	}
	h.lister.mu.Lock()
	calls := h.lister.calls
	h.lister.mu.Unlock()
	if calls != 0 {
		t.Fatalf("lister called %d times while paused", calls)
	}
}

// TestScanCountsDistinctChannelFailures proves the FailMonitor hooks:
// a non-cookie ChannelVideos error feeds Fail(channelID), and a clean scan
// pass feeds Reset().
func TestScanCountsDistinctChannelFailures(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)

	var fails []string
	var resets int
	fm := &fakeMonitor{
		onFail:  func(id string) { fails = append(fails, id) },
		onReset: func() { resets++ },
	}
	h.sched = New(Deps{
		Channels: h.channels, Ledger: h.ledger, Videos: h.videos, Jobs: h.jobs,
		Settings:     h.settings,
		Lister:       errLister{err: errors.New("boom")}, // neither ErrBlocked nor ErrCookieExpired
		CookieStatus: func(context.Context) string { return h.cookieStatus },
		FailMonitor:  fm,
		Now:          func() time.Time { return fixedNow },
		PollInterval: 5 * time.Millisecond,
	})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if sub == nil {
		t.Fatal("expected a due subscription")
	}
	h.sched.scanChannel(context.Background(), sub)

	if len(fails) != 1 || fails[0] != "UC1" {
		t.Fatalf("fails = %+v, want [UC1]", fails)
	}
	if resets != 0 {
		t.Fatalf("resets = %d, want 0 on a failed pass", resets)
	}
}

// TestScanTerminalErrorDoesNotCountFailure mirrors the download worker's
// classify: a *ytdlp.TerminalError (members-only/deleted/private/age/geo
// channel) is a permanent, per-channel-expected condition, not a real scan
// failure, so it must NOT feed FailMonitor.Fail — only unclassified errors
// (exec/extractor, RetryableError) should count toward the shared
// auto-pause heuristic.
func TestScanTerminalErrorDoesNotCountFailure(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)

	var fails []string
	fm := &fakeMonitor{
		onFail: func(id string) { fails = append(fails, id) },
	}
	h.sched = New(Deps{
		Channels:     h.channels,
		Ledger:       h.ledger,
		Videos:       h.videos,
		Jobs:         h.jobs,
		Settings:     h.settings,
		Lister:       errLister{err: &ytdlp.TerminalError{Reason: "members"}},
		CookieStatus: func(context.Context) string { return h.cookieStatus },
		FailMonitor:  fm,
		Now:          func() time.Time { return fixedNow },
		PollInterval: 5 * time.Millisecond,
	})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if sub == nil {
		t.Fatal("expected a due subscription")
	}
	h.sched.scanChannel(context.Background(), sub)

	if len(fails) != 0 {
		t.Fatalf("fails = %+v, want none for a TerminalError", fails)
	}
}

// TestScanPausedErrorDoesNotCountFailure mirrors the download worker's
// classify for ytdlp.ErrPaused: the kill-switch tripping mid-scan is not a
// real failure and must not feed FailMonitor.Fail.
func TestScanPausedErrorDoesNotCountFailure(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)

	var fails []string
	fm := &fakeMonitor{
		onFail: func(id string) { fails = append(fails, id) },
	}
	h.sched = New(Deps{
		Channels:     h.channels,
		Ledger:       h.ledger,
		Videos:       h.videos,
		Jobs:         h.jobs,
		Settings:     h.settings,
		Lister:       errLister{err: ytdlp.ErrPaused},
		CookieStatus: func(context.Context) string { return h.cookieStatus },
		FailMonitor:  fm,
		Now:          func() time.Time { return fixedNow },
		PollInterval: 5 * time.Millisecond,
	})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if sub == nil {
		t.Fatal("expected a due subscription")
	}
	h.sched.scanChannel(context.Background(), sub)

	if len(fails) != 0 {
		t.Fatalf("fails = %+v, want none for ErrPaused", fails)
	}
}

// TestScanErrNoCookieDoesNotCountTowardAutoPause mirrors the download
// worker's classify for ytdlp.ErrNoCookie: no cookie at all is race-only and
// self-limiting (the scheduler's own cookie gate stops scanning next pass),
// so it must NOT feed FailMonitor.Fail.
func TestScanErrNoCookieDoesNotCountTowardAutoPause(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)

	var fails []string
	fm := &fakeMonitor{
		onFail: func(id string) { fails = append(fails, id) },
	}
	h.sched = New(Deps{
		Channels:     h.channels,
		Ledger:       h.ledger,
		Videos:       h.videos,
		Jobs:         h.jobs,
		Settings:     h.settings,
		Lister:       errLister{err: ytdlp.ErrNoCookie},
		CookieStatus: func(context.Context) string { return h.cookieStatus },
		FailMonitor:  fm,
		Now:          func() time.Time { return fixedNow },
		PollInterval: 5 * time.Millisecond,
	})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if sub == nil {
		t.Fatal("expected a due subscription")
	}
	h.sched.scanChannel(context.Background(), sub)

	if len(fails) != 0 {
		t.Fatalf("fails = %+v, want none for ErrNoCookie", fails)
	}
}

func TestScanCleanPassResetsFailMonitor(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "v1", Title: "A", DurationSeconds: 600, LiveStatus: "not_live"},
	})

	var resets int
	fm := &fakeMonitor{onReset: func() { resets++ }}
	h.sched = New(Deps{
		Channels: h.channels, Ledger: h.ledger, Videos: h.videos, Jobs: h.jobs,
		Settings:     h.settings,
		Lister:       h.lister,
		CookieStatus: func(context.Context) string { return h.cookieStatus },
		FailMonitor:  fm,
		Now:          func() time.Time { return fixedNow },
		PollInterval: 5 * time.Millisecond,
	})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if sub == nil {
		t.Fatal("expected a due subscription")
	}
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if resets != 1 {
		t.Fatalf("resets = %d, want 1 on a clean pass", resets)
	}
}

func TestScan_noCookie_skipsScan(t *testing.T) {
	h := newScanHarness(t)
	h.cookieStatus = "absent" // harness wires CookieStatus to return this
	h.addAndSubscribe("UC1", false, "")
	h.lister.set("UC1", []ytdlp.ChannelEntry{{ID: "v1", DurationSeconds: 600}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sched.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	// Lister was never called (no cookie): nothing recorded.
	if ok, _ := h.ledger.Exists("v1"); ok {
		t.Fatal("must not scan without a valid cookie")
	}
	h.lister.mu.Lock()
	calls := h.lister.calls
	h.lister.mu.Unlock()
	if calls != 0 {
		t.Fatalf("lister called %d times without cookie", calls)
	}
}

// TestScan_anonymousAllowed_proceedsWithAbsentCookie locks the dev-only
// anonymous escape hatch at the scheduler's own cookie gate: when
// AllowAnonymous is set, an absent cookie must no longer block the poll from
// proceeding to scan.
func TestScan_anonymousAllowed_proceedsWithAbsentCookie(t *testing.T) {
	h := newScanHarness(t)
	h.cookieStatus = "absent"
	h.addAndSubscribe("UC1", false, "")
	h.lister.set("UC1", []ytdlp.ChannelEntry{{ID: "v1", DurationSeconds: 600, LiveStatus: "not_live"}})

	h.sched = New(Deps{
		Channels:       h.channels,
		Ledger:         h.ledger,
		Videos:         h.videos,
		Jobs:           h.jobs,
		Settings:       h.settings,
		Lister:         h.lister,
		CookieStatus:   func(context.Context) string { return h.cookieStatus },
		AllowAnonymous: true,
		Now:            func() time.Time { return fixedNow },
		PollInterval:   5 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sched.Run(ctx); close(done) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	if ok, _ := h.ledger.Exists("v1"); !ok {
		t.Fatal("anonymous mode must proceed to scan despite an absent cookie")
	}
	h.lister.mu.Lock()
	calls := h.lister.calls
	h.lister.mu.Unlock()
	if calls == 0 {
		t.Fatal("lister must be called when anonymous is allowed, even with an absent cookie")
	}
}

// Note: the scheduler's own poll-level gate is deliberately a blunt
// CookieStatus != "valid" check with no "expired"/"blocked" distinction —
// that finer-grained distinction (a real cookie exists and was rejected,
// vs. no cookie at all) is enforced downstream by ytdlp.Runner.cookieGate
// against the actual cookie text/status on every real yt-dlp call (see
// internal/ytdlp's TestCookieGate_anonymousAllowed_expiredStillErrors and
// TestCookieGate_anonymousAllowed_blockedStillErrors), not duplicated here.

// seedCookieStatusBypassingCheck writes status into settings.cookie_status
// directly, stepping around the schema's `cookie_status IN (...)` CHECK
// constraint via SQLite's ignore_check_constraints pragma. This exists for
// exactly one purpose: TestScan_interlockEngaged_neverUnsubscribes's
// "unknown future cookie status" case, which needs to simulate a status the
// current schema does not (and by design should not) allow through the real
// settings.Store.SetCookie path, to prove the scheduler's interlock is an
// allowlist rather than a denylist against exactly the statuses enumerated
// today. Both PRAGMA toggles and the UPDATE run inside one Exec call so the
// pooled *sql.DB is guaranteed to execute them all on the same connection —
// the pragma is per-connection, so splitting them across separate Exec calls
// would risk the UPDATE landing on a different pooled connection where the
// constraint is still enforced.
func (h *scanHarness) seedCookieStatusBypassingCheck(status string) {
	h.t.Helper()
	// The driver rejects bound parameters across a multi-statement Exec
	// ("sqlite3: multiple statements"), so status is inlined as a quoted
	// SQL string literal (single quotes, doubled per SQL escaping rules)
	// rather than bound — safe here because it is always a Go string
	// literal supplied by this test file, never external input.
	stmt := fmt.Sprintf(
		`PRAGMA ignore_check_constraints=ON; UPDATE settings SET cookie_status = '%s' WHERE id = 1; PRAGMA ignore_check_constraints=OFF;`,
		strings.ReplaceAll(status, "'", "''"),
	)
	if _, err := h.db.Exec(stmt); err != nil {
		h.t.Fatalf("seed cookie status (bypass check) = %q: %v", status, err)
	}
}

// deadScanCount reads subscriptions.dead_scan_count for channelID directly,
// bypassing RecordDeadScan (which would itself mutate the value under test).
func (h *scanHarness) deadScanCount(channelID string) int {
	h.t.Helper()
	var n int
	if err := h.db.QueryRow(
		`SELECT dead_scan_count FROM subscriptions WHERE channel_id = ?`, channelID,
	).Scan(&n); err != nil {
		h.t.Fatalf("read dead_scan_count %s: %v", channelID, err)
	}
	return n
}

// isSubscribed reports whether channelID currently has a subscriptions row.
func (h *scanHarness) isSubscribed(channelID string) bool {
	h.t.Helper()
	items, err := h.channels.List("all")
	if err != nil {
		h.t.Fatalf("list all: %v", err)
	}
	for _, it := range items {
		if it.ID == channelID {
			return it.Subscribed
		}
	}
	h.t.Fatalf("channel %s not found in List(all)", channelID)
	return false
}

// autoUnsubscribeReason returns the reason recorded in auto_unsubscribes for
// channelID, or "" if no row exists.
func (h *scanHarness) autoUnsubscribeReason(channelID string) string {
	h.t.Helper()
	var reason string
	err := h.db.QueryRow(
		`SELECT reason FROM auto_unsubscribes WHERE channel_id = ?`, channelID,
	).Scan(&reason)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		h.t.Fatalf("read auto_unsubscribes %s: %v", channelID, err)
	}
	return reason
}

// deletedSched builds a scheduler over h's stores whose Lister always
// returns a real *ytdlp.TerminalError{Reason: "deleted"} — the interlock
// tests need the genuine typed error since staleUnsubscribe branches on
// errors.As, not a sentinel string.
func (h *scanHarness) deletedSched() *Scheduler {
	return New(Deps{
		Channels: h.channels, Ledger: h.ledger, Videos: h.videos, Jobs: h.jobs,
		Settings:     h.settings,
		Lister:       errLister{err: &ytdlp.TerminalError{Reason: channels.ReasonDeleted}},
		CookieStatus: func(context.Context) string { return h.cookieStatus },
		Now:          func() time.Time { return fixedNow },
		PollInterval: 5 * time.Millisecond,
	})
}

// terminalSched builds a scheduler whose Lister always fails with a real
// *ytdlp.TerminalError of the given (non-deleted) reason.
func (h *scanHarness) terminalSched(reason string) *Scheduler {
	return New(Deps{
		Channels: h.channels, Ledger: h.ledger, Videos: h.videos, Jobs: h.jobs,
		Settings:     h.settings,
		Lister:       errLister{err: &ytdlp.TerminalError{Reason: reason}},
		CookieStatus: func(context.Context) string { return h.cookieStatus },
		Now:          func() time.Time { return fixedNow },
		PollInterval: 5 * time.Millisecond,
	})
}

// TestScan_deletedThreeTimes_autoUnsubscribes proves the happy path: three
// consecutive scans that surface a real *ytdlp.TerminalError{Reason:
// "deleted"} unsubscribe the channel and leave an auto_unsubscribes record
// behind (reason "deleted"), so the automatic action always leaves a trace.
func TestScan_deletedThreeTimes_autoUnsubscribes(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	// The interlock must be open (healthy cookie, not paused) for this test:
	// cookie_status defaults to "absent", which would otherwise mask the
	// behaviour under test.
	if err := h.settings.SetCookie(context.Background(), "", "valid"); err != nil {
		t.Fatalf("seed cookie status: %v", err)
	}
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if sub == nil {
		t.Fatal("expected a due subscription")
	}
	sched := h.deletedSched()

	for i := 0; i < channels.DeadScanThreshold; i++ {
		sched.scanChannel(context.Background(), sub)
	}

	if h.isSubscribed("UC1") {
		t.Fatal("channel should be auto-unsubscribed after DeadScanThreshold deleted scans")
	}
	if reason := h.autoUnsubscribeReason("UC1"); reason != channels.ReasonDeleted {
		t.Fatalf("auto_unsubscribes reason = %q, want %q", reason, channels.ReasonDeleted)
	}
}

// TestScan_deletedTwice_thenCleanScan_doesNotUnsubscribe proves the reset
// genuinely breaks a run: two deleted scans, then one clean scan (which must
// call ResetDeadScan alongside MarkScanned), then two more deleted scans —
// still short of the threshold's worth of CONSECUTIVE dead scans, so the
// channel must remain subscribed.
func TestScan_deletedTwice_thenCleanScan_doesNotUnsubscribe(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	if err := h.settings.SetCookie(context.Background(), "", "valid"); err != nil {
		t.Fatalf("seed cookie status: %v", err)
	}
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if sub == nil {
		t.Fatal("expected a due subscription")
	}

	deleted := h.deletedSched()
	deleted.scanChannel(context.Background(), sub)
	deleted.scanChannel(context.Background(), sub)
	if n := h.deadScanCount("UC1"); n != 2 {
		t.Fatalf("dead_scan_count after 2 deleted scans = %d, want 2", n)
	}

	// One clean scan: no entries, no error.
	clean := New(Deps{
		Channels: h.channels, Ledger: h.ledger, Videos: h.videos, Jobs: h.jobs,
		Settings:     h.settings,
		Lister:       h.lister, // returns no entries for UC1, no error
		CookieStatus: func(context.Context) string { return h.cookieStatus },
		Now:          func() time.Time { return fixedNow },
		PollInterval: 5 * time.Millisecond,
	})
	clean.scanChannel(context.Background(), sub)
	if n := h.deadScanCount("UC1"); n != 0 {
		t.Fatalf("dead_scan_count after clean scan = %d, want 0 (reset)", n)
	}

	deleted.scanChannel(context.Background(), sub)
	deleted.scanChannel(context.Background(), sub)
	if n := h.deadScanCount("UC1"); n != 2 {
		t.Fatalf("dead_scan_count after reset + 2 more deleted scans = %d, want 2", n)
	}
	if !h.isSubscribed("UC1") {
		t.Fatal("channel must still be subscribed: the run never reached 3 CONSECUTIVE dead scans")
	}
}

// TestScan_interlockEngaged_neverUnsubscribes is THE MOST IMPORTANT TEST IN
// THE SLICE. yt-dlp derives its error reason by substring-matching stderr;
// age/geo failures in particular can be symptoms of a dead cookie rather
// than a dead channel. If cookie access is unhealthy (blocked/stale/absent)
// or the youtube_paused kill-switch is set, staleUnsubscribe must be
// completely inert: not just skip unsubscribing, but never even increment
// the counter — otherwise an outage silently accumulates toward the
// threshold and fires the instant access is restored.
func TestScan_interlockEngaged_neverUnsubscribes(t *testing.T) {
	const consecutiveScans = 5

	cases := []struct {
		name         string
		cookieStatus string // "" leaves cookie_status at "valid"
		paused       bool
		bypassCheck  bool // seed cookieStatus around the schema's CHECK constraint
	}{
		{name: "cookie blocked", cookieStatus: "blocked"},
		{name: "cookie stale", cookieStatus: "stale"},
		{name: "cookie absent", cookieStatus: "absent"},
		{name: "youtube paused", paused: true},
		// Pins the allowlist fix: the interlock must deny anything that is
		// not exactly "valid", not merely the three statuses the schema's
		// CHECK constraint happens to enumerate today. A denylist form
		// would fail OPEN here and let this hypothetical future status
		// through. The schema's CHECK constraint does not allow this value
		// through the normal settings.Store.SetCookie path (by design —
		// that's the whole reason the allowlist is defense in depth rather
		// than provably unreachable), so this case seeds it directly via
		// seedCookieStatusBypassingCheck, simulating a future migration
		// that adds a 5th status without every caller being updated in
		// lockstep.
		{name: "unknown future cookie status", cookieStatus: "quarantined", bypassCheck: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newScanHarness(t)
			h.addAndSubscribe("UC1", false, "")
			h.markBaselined("UC1", nil)
			sub, _ := h.channels.ClaimDue(h.nowStr())
			if sub == nil {
				t.Fatal("expected a due subscription")
			}

			status := "valid"
			if tc.cookieStatus != "" {
				status = tc.cookieStatus
			}
			if tc.bypassCheck {
				h.seedCookieStatusBypassingCheck(status)
			} else if err := h.settings.SetCookie(context.Background(), "", status); err != nil {
				t.Fatalf("seed cookie status: %v", err)
			}
			if tc.paused {
				if err := h.settings.SetYoutubePaused(context.Background(), true, "test"); err != nil {
					t.Fatalf("seed youtube_paused: %v", err)
				}
			}

			sched := h.deletedSched()
			for i := 0; i < consecutiveScans; i++ {
				sched.scanChannel(context.Background(), sub)
			}

			if !h.isSubscribed("UC1") {
				t.Fatalf("%s: interlock must prevent auto-unsubscribe", tc.name)
			}
			if n := h.deadScanCount("UC1"); n != 0 {
				t.Fatalf("%s: dead_scan_count = %d, want 0 (must not even increment while interlocked)", tc.name, n)
			}
			if reason := h.autoUnsubscribeReason("UC1"); reason != "" {
				t.Fatalf("%s: unexpected auto_unsubscribes row (reason %q)", tc.name, reason)
			}
		})
	}
}

// TestScan_nonDeletedTerminalReasons_neverCount proves staleUnsubscribe
// checks specifically for "deleted", not merely "is this terminal?". Each
// reason is its own subtest: a single combined assertion would still pass
// if the code gated on terminal-ness alone, which would wrongly unsubscribe
// healthy channels whose latest video happens to be members-only/age-gated/
// geo-blocked/private.
func TestScan_nonDeletedTerminalReasons_neverCount(t *testing.T) {
	reasons := []string{"private", "members", "age", "geo"}

	for _, reason := range reasons {
		t.Run(reason, func(t *testing.T) {
			h := newScanHarness(t)
			h.addAndSubscribe("UC1", false, "")
			h.markBaselined("UC1", nil)
			// Cookie must be healthy here: the point of this test is that the
			// reason check alone (not the interlock) keeps a non-deleted
			// terminal error from counting. A default "absent" cookie would
			// let a buggy "is this terminal?" implementation pass too, by
			// returning at the interlock instead of the reason check.
			if err := h.settings.SetCookie(context.Background(), "", "valid"); err != nil {
				t.Fatalf("seed cookie status: %v", err)
			}
			sub, _ := h.channels.ClaimDue(h.nowStr())
			if sub == nil {
				t.Fatal("expected a due subscription")
			}

			sched := h.terminalSched(reason)
			for i := 0; i < channels.DeadScanThreshold; i++ {
				sched.scanChannel(context.Background(), sub)
			}

			if n := h.deadScanCount("UC1"); n != 0 {
				t.Fatalf("reason %q: dead_scan_count = %d, want 0", reason, n)
			}
			if !h.isSubscribed("UC1") {
				t.Fatalf("reason %q: channel must remain subscribed", reason)
			}
		})
	}
}

// TestScan_nonConsecutiveDeleted_resetsAndDoesNotUnsubscribe proves the
// counter really adds CONSECUTIVE dead scans, not merely "at least N dead
// scans out of the last M". The sequence deleted, deleted, members, deleted,
// deleted has four "deleted" results total (more than DeadScanThreshold) but
// never three IN A ROW: the "members" result in the middle is affirmative
// evidence the channel was alive at that point, so it must reset the streak
// and the channel must remain subscribed after all five scans.
func TestScan_nonConsecutiveDeleted_resetsAndDoesNotUnsubscribe(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	if err := h.settings.SetCookie(context.Background(), "", "valid"); err != nil {
		t.Fatalf("seed cookie status: %v", err)
	}
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if sub == nil {
		t.Fatal("expected a due subscription")
	}

	deleted := h.deletedSched()
	members := h.terminalSched("members")

	deleted.scanChannel(context.Background(), sub)
	deleted.scanChannel(context.Background(), sub)
	if n := h.deadScanCount("UC1"); n != 2 {
		t.Fatalf("dead_scan_count after 2 deleted scans = %d, want 2", n)
	}

	members.scanChannel(context.Background(), sub)
	if n := h.deadScanCount("UC1"); n != 0 {
		t.Fatalf("dead_scan_count after an interleaved members-only scan = %d, want 0 (reset)", n)
	}

	deleted.scanChannel(context.Background(), sub)
	deleted.scanChannel(context.Background(), sub)
	if n := h.deadScanCount("UC1"); n != 2 {
		t.Fatalf("dead_scan_count after reset + 2 more deleted scans = %d, want 2", n)
	}

	if !h.isSubscribed("UC1") {
		t.Fatal("channel must still be subscribed: 4 total 'deleted' results never formed 3 CONSECUTIVE ones")
	}
	if reason := h.autoUnsubscribeReason("UC1"); reason != "" {
		t.Fatalf("unexpected auto_unsubscribes row (reason %q)", reason)
	}
}

// TestScan_backfillsPublishedDateOnKnownRows is the regression guard for the
// bug that made this feature look broken: adding published_at alone fixes
// nothing for videos ALREADY in the inbox, because the scan short-circuits on
// a known id and never touches the row again. Every item a user is looking at
// today is such a row.
func TestScan_backfillsPublishedDateOnKnownRows(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	// A pending row written the old way: known to the ledger, no date.
	if err := h.ledger.Insert(channelvideos.Entry{
		VideoID: "oldrow", ChannelID: "UC1", State: "pending", DurationSeconds: 600,
	}); err != nil {
		t.Fatal(err)
	}
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "oldrow", DurationSeconds: 600, LiveStatus: "not_live", PublishedAt: "2026-07-11"},
		{ID: "newrow", DurationSeconds: 600, LiveStatus: "not_live", PublishedAt: "2026-07-18"},
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}

	healed, err := h.ledger.Get("oldrow")
	if err != nil {
		t.Fatal(err)
	}
	if healed.PublishedAt != "2026-07-11" {
		t.Fatalf("known row published = %q, want it healed to 2026-07-11", healed.PublishedAt)
	}
	// The row must still be pending: healing a date is not a decision, and
	// must not disturb the state machine.
	if healed.State != "pending" {
		t.Fatalf("heal changed state to %q, want pending", healed.State)
	}
	fresh, err := h.ledger.Get("newrow")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.PublishedAt != "2026-07-18" {
		t.Fatalf("new row published = %q", fresh.PublishedAt)
	}
}

// TestScan_autodownloadDoesNotSeedVideoPublishedAt guards the deliberate
// asymmetry in enqueueAuto: the ledger's date is yt-dlp's APPROXIMATE tab
// date, while videos.published_at is the exact upload_date the download's own
// metadata call writes. Seeding the videos row from the listing would quietly
// downgrade the date the Library renders.
func TestScan_autodownloadDoesNotSeedVideoPublishedAt(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", true /*autodownload*/, "")
	h.markBaselined("UC1", nil)
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "auto1", DurationSeconds: 600, LiveStatus: "not_live", PublishedAt: "2026-07-18"},
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	v, err := h.videos.Get("auto1")
	if err != nil {
		t.Fatal(err)
	}
	if v == nil {
		t.Fatal("autodownload must seed a videos row")
	}
	if v.PublishedAt != "" {
		t.Fatalf("videos.published_at = %q, want empty until real metadata arrives", v.PublishedAt)
	}
}

// requestScan marks the subscription as having a user waiting on it, exactly as
// the "Check now" endpoint does, and leaves next_scan_at due.
func (h *scanHarness) requestScan(ucid string) {
	h.t.Helper()
	if err := h.channels.RequestScan(ucid, h.nowStr()); err != nil {
		h.t.Fatalf("request scan %s: %v", ucid, err)
	}
}

// scanRequestedAt reads the raw marker column, so a test can assert it was
// cleared rather than inferring it from behaviour.
func (h *scanHarness) scanRequestedAt(ucid string) string {
	h.t.Helper()
	var v sql.NullString
	if err := h.db.QueryRow(
		`SELECT scan_requested_at FROM subscriptions WHERE channel_id = ?`, ucid,
	).Scan(&v); err != nil {
		h.t.Fatalf("read scan_requested_at %s: %v", ucid, err)
	}
	return v.String
}

func TestScan_requestedScan_reportsNothingNew(t *testing.T) {
	// The bug this fixes: a user pressed "Check now", the pass found nothing, and
	// the silence rule left them with no evidence the check ever happened.
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", []string{"old1"})
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "old1", DurationSeconds: 600, LiveStatus: "not_live"}, // known → nothing new
	})
	h.requestScan("UC1")

	sub, _ := h.channels.ClaimDue(h.nowStr())
	h.sched.scanChannel(context.Background(), sub)

	ev := h.activity.scanEvents()
	if len(ev) != 1 {
		t.Fatalf("scan events = %+v, want exactly one receipt", ev)
	}
	if ev[0].Outcome != activity.OutcomeOK {
		t.Fatalf("outcome = %q, want ok", ev[0].Outcome)
	}
	if ev[0].Summary != "checked on request" || ev[0].Detail != "nothing new" {
		t.Fatalf("row = %q / %q, want \"checked on request\" / \"nothing new\"", ev[0].Summary, ev[0].Detail)
	}
	if ev[0].SubjectID != "UC1" {
		t.Fatalf("subject id = %q, want UC1 (the agenda links it)", ev[0].SubjectID)
	}
	// Spent: the next automatic pass must not re-announce itself as this answer.
	if got := h.scanRequestedAt("UC1"); got != "" {
		t.Fatalf("scan_requested_at = %q, want cleared", got)
	}
}

func TestScan_automaticScan_staysSilentWhenNothingNew(t *testing.T) {
	// The silence rule itself must survive: an unrequested pass that finds
	// nothing writes nothing, or the agenda becomes a wall of "0 new".
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", []string{"old1"})
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "old1", DurationSeconds: 600, LiveStatus: "not_live"},
	})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	h.sched.scanChannel(context.Background(), sub)

	if ev := h.activity.scanEvents(); len(ev) != 0 {
		t.Fatalf("scan events = %+v, want none for an automatic nothing-new pass", ev)
	}
}

func TestScan_requestedScan_newVideoReportsTheVideoNotAReceipt(t *testing.T) {
	// A requested pass that DID find something already answers the user with the
	// normal "N new" row — it must not also emit the nothing-new receipt.
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "newp", DurationSeconds: 600, LiveStatus: "not_live"},
	})
	h.requestScan("UC1")

	sub, _ := h.channels.ClaimDue(h.nowStr())
	h.sched.scanChannel(context.Background(), sub)

	ev := h.activity.scanEvents()
	if len(ev) != 1 {
		t.Fatalf("scan events = %+v, want exactly one row", ev)
	}
	if ev[0].Summary != "1 new" {
		t.Fatalf("summary = %q, want \"1 new\"", ev[0].Summary)
	}
}

func TestScan_requestedScanFailure_recordsCheckFailedAndClearsMarker(t *testing.T) {
	// Without this the marker would survive the failure and some later automatic
	// pass would report itself as the answer to a request the user already saw
	// fail — worse than the silence being fixed.
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	h.lister.err = errors.New("yt-dlp exploded")
	h.requestScan("UC1")

	sub, _ := h.channels.ClaimDue(h.nowStr())
	h.sched.scanChannel(context.Background(), sub)

	ev := h.activity.scanEvents()
	if len(ev) != 1 {
		t.Fatalf("scan events = %+v, want one failure row", ev)
	}
	if ev[0].Outcome != activity.OutcomeFail {
		t.Fatalf("outcome = %q, want fail", ev[0].Outcome)
	}
	if ev[0].Summary != "check failed" {
		t.Fatalf("summary = %q, want \"check failed\" (the user pressed a button)", ev[0].Summary)
	}
	if got := h.scanRequestedAt("UC1"); got != "" {
		t.Fatalf("scan_requested_at = %q, want cleared even on failure", got)
	}
}

func TestScan_automaticFailure_keepsScanFailedWording(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	h.lister.err = errors.New("yt-dlp exploded")

	sub, _ := h.channels.ClaimDue(h.nowStr())
	h.sched.scanChannel(context.Background(), sub)

	ev := h.activity.scanEvents()
	if len(ev) != 1 || ev[0].Summary != "scan failed" {
		t.Fatalf("scan events = %+v, want one \"scan failed\" row", ev)
	}
}

func TestScan_requestedScan_paused_reportsInsteadOfSilence(t *testing.T) {
	// A kill-switch pause deliberately records nothing on a background pass. A
	// requested one must still answer, or the user waits on a check that will
	// never run.
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	h.lister.err = ytdlp.ErrPaused
	h.requestScan("UC1")

	sub, _ := h.channels.ClaimDue(h.nowStr())
	h.sched.scanChannel(context.Background(), sub)

	ev := h.activity.scanEvents()
	if len(ev) != 1 || ev[0].Summary != "check failed" || ev[0].Detail != "YouTube access is paused" {
		t.Fatalf("scan events = %+v, want a paused check-failed row", ev)
	}
	if got := h.scanRequestedAt("UC1"); got != "" {
		t.Fatalf("scan_requested_at = %q, want cleared", got)
	}
}

func TestScan_automaticScan_paused_staysSilent(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	h.lister.err = ytdlp.ErrPaused

	sub, _ := h.channels.ClaimDue(h.nowStr())
	h.sched.scanChannel(context.Background(), sub)

	if ev := h.activity.scanEvents(); len(ev) != 0 {
		t.Fatalf("scan events = %+v, want none (a pause is not a failure)", ev)
	}
}

func TestScan_liveEntry_notRecorded_thenPickedUpWhenFinished(t *testing.T) {
	// The permanent-loss bug: 'seen' is terminal, so a stream snapshotted while
	// live used to vanish for good — even after it became an ordinary video.
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "stream1", Title: "Launch stream", DurationSeconds: 0, LiveStatus: "is_live"},
	})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if st := h.ledgerStateOrAbsent("stream1"); st != "" {
		t.Fatalf("live entry state = %q, want no row so a later pass reconsiders it", st)
	}

	// The stream ends: same id, now an ordinary finished video.
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "stream1", Title: "Launch stream", DurationSeconds: 7200, LiveStatus: "was_live"},
	})
	if _, err := h.db.Exec(
		`UPDATE subscriptions SET next_scan_at = ? WHERE channel_id = ?`, h.nowStr(), "UC1",
	); err != nil {
		t.Fatal(err)
	}
	sub2, _ := h.channels.ClaimDue(h.nowStr())
	if sub2 == nil {
		t.Fatal("subscription must be due again")
	}
	if err := h.sched.scanOnce(context.Background(), sub2); err != nil {
		t.Fatal(err)
	}
	p, _ := h.ledger.ListPending()
	if len(p) != 1 || p[0].VideoID != "stream1" {
		t.Fatalf("pending = %+v, want the finished stream to land in the inbox", p)
	}
}

func TestPassesFilters(t *testing.T) {
	// passesFilters had no dedicated test; the duration floor's fail-open branch
	// in particular was only exercised incidentally.
	const minDuration = 180
	cases := []struct {
		name string
		e    ytdlp.ChannelEntry
		want bool
	}{
		{"ordinary video", ytdlp.ChannelEntry{DurationSeconds: 600, LiveStatus: "not_live"}, true},
		{"finished stream", ytdlp.ChannelEntry{DurationSeconds: 7200, LiveStatus: "was_live"}, true},
		{"exactly at the floor", ytdlp.ChannelEntry{DurationSeconds: 180, LiveStatus: "not_live"}, true},
		{"below the floor", ytdlp.ChannelEntry{DurationSeconds: 179, LiveStatus: "not_live"}, false},
		{"zero duration fails open", ytdlp.ChannelEntry{DurationSeconds: 0, LiveStatus: "not_live"}, true},
		{"live", ytdlp.ChannelEntry{DurationSeconds: 600, LiveStatus: "is_live"}, false},
		{"upcoming", ytdlp.ChannelEntry{DurationSeconds: 600, LiveStatus: "is_upcoming"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := passesFilters(c.e, minDuration); got != c.want {
				t.Fatalf("passesFilters = %v, want %v", got, c.want)
			}
		})
	}
}

// TestIsUnfinishedStream pins the allowlist: only the three settled statuses
// are recorded, and anything else — including a status yt-dlp has not shipped
// yet — is deferred rather than treated as a finished video.
func TestIsUnfinishedStream(t *testing.T) {
	for status, want := range map[string]bool{
		"is_live":            true,
		"is_upcoming":        true,
		"post_live":          true,
		"some_future_status": true,
		"was_live":           false,
		"not_live":           false,
		"":                   false,
	} {
		if got := isUnfinishedStream(ytdlp.ChannelEntry{LiveStatus: status}); got != want {
			t.Fatalf("isUnfinishedStream(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestScan_requestArrivingMidPassSurvives(t *testing.T) {
	// End to end through the scheduler: the pass that did not see the request
	// must leave both the marker and the due-now schedule alone, so the loop
	// re-claims the channel and the user still gets their answer.
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", []string{"old1"})
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "old1", DurationSeconds: 600, LiveStatus: "not_live"},
	})

	sub, _ := h.channels.ClaimDue(h.nowStr())
	// The click lands after the claim, before the pass finishes.
	h.requestScan("UC1")
	h.sched.scanChannel(context.Background(), sub)

	if got := h.scanRequestedAt("UC1"); got == "" {
		t.Fatal("the mid-pass request was consumed by a scan that never saw it")
	}
	if h.activity.scanEvents() != nil {
		t.Fatalf("scan events = %+v, want none yet — that pass owed nothing", h.activity.scanEvents())
	}
	// The loop re-claims it, and this time the receipt is owed.
	sub2, _ := h.channels.ClaimDue(h.nowStr())
	if sub2 == nil {
		t.Fatal("subscription must still be due so the request is honoured")
	}
	h.sched.scanChannel(context.Background(), sub2)

	ev := h.activity.scanEvents()
	if len(ev) != 1 || ev[0].Summary != "checked on request" {
		t.Fatalf("scan events = %+v, want the receipt on the re-claimed pass", ev)
	}
	if got := h.scanRequestedAt("UC1"); got != "" {
		t.Fatalf("scan_requested_at = %q, want cleared once answered", got)
	}
}

// TestScan_backCatalogue_isNotNew is the regression for the flood: adding the
// /streams tab made a source visible that no already-baselined channel had ever
// been listed against, so its entire history was absent from the ledger and
// every VOD in it read as brand new. baselined_at is 2026-07-19 12:00 here, so
// with the three-day grace the boundary sits at 2026-07-16 12:00. A publish
// date is date-only, so 2026-07-16 (midnight) is still BEFORE that boundary and
// is suppressed; 2026-07-17 is the first date admitted to the inbox.
func TestScan_backCatalogue_isNotNew(t *testing.T) {
	cases := []struct {
		name      string
		published string
		want      string
	}{
		{"years old", "2019-03-04", "seen"},
		{"inside the grace but before it", "2026-07-15", "seen"},
		// The boundary day itself: date-only midnight is before 07-16 12:00, so
		// the effective window is three-to-four days, not exactly three.
		{"the boundary day", "2026-07-16", "seen"},
		{"the first admitted day", "2026-07-17", "pending"},
		{"published today", "2026-07-19", "pending"},
		// Fails OPEN: an undated entry is a nuisance in the inbox, but marking
		// it 'seen' would be terminal and lose a real video.
		{"undated", "", "pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newScanHarness(t)
			h.addAndSubscribe("UC1", false, "")
			h.markBaselined("UC1", nil)
			h.lister.setStreams("UC1", []ytdlp.ChannelEntry{{
				ID: "vod00001", Title: "Stream", DurationSeconds: 7200,
				LiveStatus: "was_live", PublishedAt: tc.published,
			}})
			sub, _ := h.channels.ClaimDue(h.nowStr())
			if err := h.sched.scanOnce(context.Background(), sub); err != nil {
				t.Fatal(err)
			}
			if st := h.ledgerState("vod00001"); st != tc.want {
				t.Fatalf("published %q → state %q, want %q", tc.published, st, tc.want)
			}
		})
	}
}

// TestScan_backCatalogue_autodownloadDoesNotDownloadHistory covers the
// expensive half of the same bug: on an autodownload channel the back
// catalogue was not merely offered, it was queued for real download. This is
// why the gate sits BEFORE the autodownload branch.
func TestScan_backCatalogue_autodownloadDoesNotDownloadHistory(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", true, "")
	h.markBaselined("UC1", nil)
	h.lister.setStreams("UC1", []ytdlp.ChannelEntry{
		{ID: "oldvod01", DurationSeconds: 7200, LiveStatus: "was_live", PublishedAt: "2018-01-01"},
		{ID: "newvod01", DurationSeconds: 7200, LiveStatus: "was_live", PublishedAt: "2026-07-19"},
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if st := h.ledgerState("oldvod01"); st != "seen" {
		t.Fatalf("old vod state = %q, want seen", st)
	}
	if st := h.ledgerState("newvod01"); st != "queued" {
		t.Fatalf("new vod state = %q, want queued", st)
	}
	jobsList, _ := h.jobs.List()
	if len(jobsList) != 1 {
		t.Fatalf("jobs = %d, want exactly 1 (only the genuinely new vod)", len(jobsList))
	}
}

// TestScan_backCatalogue_doesNotStrandAnAiringStream pins the branch ORDER. A
// broadcast running right now is dated by when it STARTED, which can predate
// the baseline; if the back-catalogue gate ran before the unfinished-stream
// check it would write a terminal 'seen' row and the stream would be lost for
// good once it ended — the exact silent loss #142 was written to prevent.
func TestScan_backCatalogue_doesNotStrandAnAiringStream(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", nil)
	h.lister.setStreams("UC1", []ytdlp.ChannelEntry{{
		ID: "vod00001", Title: "Marathon", DurationSeconds: 0,
		LiveStatus: "is_live", PublishedAt: "2026-07-10", // started before the cutoff
	}})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if st := h.ledgerStateOrAbsent("vod00001"); st != "" {
		t.Fatalf("airing stream state = %q, want no row at all", st)
	}
}

// TestScan_backCatalogue_firstPassStillBaselines: a channel being followed for
// the first time has no baselined_at to compare against, and the baseline
// branch already owns that case. The gate must not disturb it.
func TestScan_backCatalogue_firstPassStillBaselines(t *testing.T) {
	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	h.lister.setStreams("UC1", []ytdlp.ChannelEntry{
		{ID: "vod00001", DurationSeconds: 7200, LiveStatus: "was_live", PublishedAt: "2019-01-01"},
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if st := h.ledgerState("vod00001"); st != "seen" {
		t.Fatalf("state = %q, want seen", st)
	}
	sub2, _ := h.channels.GetSubscription("UC1")
	if sub2 == nil || sub2.BaselinedAt == "" {
		t.Fatal("baselined_at must be stamped by the first pass")
	}
}
