package scan

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/trick77/vark/internal/channels"
	"github.com/trick77/vark/internal/channelvideos"
	"github.com/trick77/vark/internal/jobs"
	"github.com/trick77/vark/internal/settings"
	"github.com/trick77/vark/internal/store"
	"github.com/trick77/vark/internal/videos"
	"github.com/trick77/vark/internal/ytdlp"
)

// fakeLister is a canned ChannelLister: it records how many times it was
// called and returns pre-seeded entries per ucid. It is safe for concurrent
// use so the goroutine-driven no-cookie test stays race-clean.
type fakeLister struct {
	mu      sync.Mutex
	entries map[string][]ytdlp.ChannelEntry
	calls   int
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
	return f.entries[ucid], nil
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
	// Fixed clock keeps next_scan_at math deterministic; scanOnce only reads
	// Now for the schedule stamps, which the tests don't assert on.
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	h.sched = New(Deps{
		Channels:     h.channels,
		Ledger:       h.ledger,
		Videos:       h.videos,
		Jobs:         h.jobs,
		Settings:     h.settings,
		Lister:       h.lister,
		CookieStatus: func(context.Context) string { return h.cookieStatus },
		Now:          func() time.Time { return now },
		PollInterval: 5 * time.Millisecond,
	})
	return h
}

// nowStr is the harness's fixed clock in SQLite text form.
func (h *scanHarness) nowStr() string {
	return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC).Format(sqlTimeLayout)
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
	if err := h.channels.UpdateConfig(ucid, autodownload, format); err != nil {
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
	ok1, _ := h.ledger.Exists("v1")
	ok2, _ := h.ledger.Exists("v2")
	if !ok1 || !ok2 {
		t.Fatal("baseline must record all current ids as seen")
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
