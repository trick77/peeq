package scan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

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
// called and returns pre-seeded entries per ucid. When panicMsg is set it
// panics instead (exercising the scan panic guard). It is safe for concurrent
// use so the goroutine-driven no-cookie test stays race-clean.
type fakeLister struct {
	mu       sync.Mutex
	entries  map[string][]ytdlp.ChannelEntry
	calls    int
	panicMsg string
}

func newFakeLister() *fakeLister {
	return &fakeLister{entries: map[string][]ytdlp.ChannelEntry{}}
}

func (f *fakeLister) set(ucid string, e []ytdlp.ChannelEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[ucid] = e
}

func (f *fakeLister) ChannelVideos(_ context.Context, ucid string, _ int) ([]ytdlp.ChannelEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.panicMsg != "" {
		panic(f.panicMsg)
	}
	return f.entries[ucid], nil
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

// trackAndSubscribe tracks ucid and subscribes it with a next_scan_at in the
// past, so the first ClaimDue(nowStr) finds it.
func (h *scanHarness) trackAndSubscribe(ucid string, autodownload bool, format string) {
	h.t.Helper()
	if err := h.channels.Upsert(channels.Channel{ID: ucid, Name: ucid}); err != nil {
		h.t.Fatalf("track %s: %v", ucid, err)
	}
	if err := h.channels.Subscribe(ucid, "2000-01-01 00:00:00"); err != nil {
		h.t.Fatalf("subscribe %s: %v", ucid, err)
	}
	if _, err := h.channels.UpdateConfig(ucid, autodownload, format); err != nil {
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

func TestScan_firstRunBaseline_queuesNothing(t *testing.T) {
	h := newScanHarness(t)
	h.trackAndSubscribe("UC1", false /*autodownload*/, "" /*format*/)
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
	h.trackAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", []string{"old1"}) // seed ledger 'seen' + baselined_at set
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "old1", DurationSeconds: 600, LiveStatus: "not_live"},  // dedup: skip
		{ID: "newp", DurationSeconds: 600, LiveStatus: "not_live"},  // NEW → pending
		{ID: "short", DurationSeconds: 60, LiveStatus: "not_live"},  // <180s → seen
		{ID: "up", DurationSeconds: 600, LiveStatus: "is_upcoming"}, // upcoming → seen
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	p, _ := h.ledger.ListPending()
	if len(p) != 1 || p[0].VideoID != "newp" {
		t.Fatalf("pending = %+v, want [newp]", p)
	}
	// Filtered-out ids must be 'seen' specifically (not merely non-pending:
	// 'ignored' would also pass ListPending but means something else).
	if st := h.ledgerState("short"); st != "seen" {
		t.Fatalf("short state = %q, want seen", st)
	}
	if st := h.ledgerState("up"); st != "seen" {
		t.Fatalf("up state = %q, want seen", st)
	}
	if jobsList, _ := h.jobs.List(); len(jobsList) != 0 {
		t.Fatalf("non-autodownload must not enqueue; got %d jobs", len(jobsList))
	}
}

func TestScan_autodownloadEnqueuesWithFormatOverride(t *testing.T) {
	h := newScanHarness(t)
	h.trackAndSubscribe("UC1", true /*autodownload*/, "bestvideo+bestaudio")
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
	h.trackAndSubscribe("UC1", true, "bestvideo+bestaudio")
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

// TestScan_panicDuringScan_backsOff proves the panic guard backs the
// subscription off (bounding a persistently-panicking channel to ~1/hour)
// without clearing its baseline.
func TestScan_panicDuringScan_backsOff(t *testing.T) {
	h := newScanHarness(t)
	h.trackAndSubscribe("UC1", false, "")
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

// TestScan_blockedCookie_flipsStatus proves FIX 1: when a SCAN surfaces
// ytdlp.ErrBlocked (a bot-block, not a download), the scheduler flips
// cookie_status to "blocked" so its own cookie gate trips on the next pass and
// stops hammering YouTube on a dead cookie.
func TestScan_blockedCookie_flipsStatus(t *testing.T) {
	h := newScanHarness(t)
	h.trackAndSubscribe("UC1", false, "")
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
	h.trackAndSubscribe("UC1", false, "")
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
	h.trackAndSubscribe("UC1", false, "")
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
	h.trackAndSubscribe("UC1", false, "")
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
	h.trackAndSubscribe("UC1", false, "")
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
	h.trackAndSubscribe("UC1", false, "")
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
	h.trackAndSubscribe("UC1", false, "")
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
	h.trackAndSubscribe("UC1", false, "")
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
	h.trackAndSubscribe("UC1", false, "")
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
	h.trackAndSubscribe("UC1", false, "")
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
	h.trackAndSubscribe("UC1", false, "")
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
	h.trackAndSubscribe("UC1", false, "")
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
	}{
		{name: "cookie blocked", cookieStatus: "blocked"},
		{name: "cookie stale", cookieStatus: "stale"},
		{name: "cookie absent", cookieStatus: "absent"},
		{name: "youtube paused", paused: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newScanHarness(t)
			h.trackAndSubscribe("UC1", false, "")
			h.markBaselined("UC1", nil)
			sub, _ := h.channels.ClaimDue(h.nowStr())
			if sub == nil {
				t.Fatal("expected a due subscription")
			}

			status := "valid"
			if tc.cookieStatus != "" {
				status = tc.cookieStatus
			}
			if err := h.settings.SetCookie(context.Background(), "", status); err != nil {
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
			h.trackAndSubscribe("UC1", false, "")
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
