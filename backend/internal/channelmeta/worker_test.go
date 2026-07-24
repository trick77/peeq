package channelmeta

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/ytdlp"
)

// fakeResolver stands in for yt-dlp: it records the urls it was handed and
// returns a canned answer (or error, or panic) without ever shelling out.
type fakeResolver struct {
	mu     sync.Mutex
	urls   []string
	info   ytdlp.ChannelInfo
	err    error
	panics bool
}

func (f *fakeResolver) ResolveChannel(ctx context.Context, url string) (ytdlp.ChannelInfo, error) {
	f.mu.Lock()
	f.urls = append(f.urls, url)
	f.mu.Unlock()
	if f.panics {
		panic("yt-dlp went sideways")
	}
	return f.info, f.err
}

func (f *fakeResolver) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.urls...)
}

func newTestStore(t *testing.T) *channels.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return channels.New(db)
}

// quietLogger keeps worker logs out of the test output; failures are asserted
// from the database, not from what was printed.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestWorker builds a worker whose clock is fixed and whose poll interval
// is tiny, so Run can be driven by a short-lived context.
func newTestWorker(t *testing.T, s *channels.Store, r Resolver, d Deps) *Worker {
	t.Helper()
	if d.Refresher == nil {
		d.Refresher = &Refresher{Channels: s, Resolver: r, MediaDir: t.TempDir(), Logger: quietLogger()}
	}
	if d.CookieStatus == nil {
		d.CookieStatus = func(context.Context) string { return "valid" }
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC) }
	}
	if d.PollInterval == 0 {
		d.PollInterval = time.Millisecond
	}
	d.Logger = quietLogger()
	return NewWorker(d)
}

// seedDue tracks, subscribes and back-dates a channel so it is due for a
// metadata refresh with no scan anywhere near it.
func seedDue(t *testing.T, s *channels.Store, channelID string) {
	t.Helper()
	if err := s.Upsert(channels.Channel{ID: channelID, Name: "Old Name", ResolvedAt: "2026-07-01 00:00:00"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Track(channelID, "2026-01-01 00:00:00"); err != nil {
		t.Fatalf("track: %v", err)
	}
	if err := s.Subscribe(channelID, "2026-07-23 12:00:00"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := s.MarkMetaRefreshed(channelID, "2026-07-15 12:00:00"); err != nil {
		t.Fatalf("schedule: %v", err)
	}
}

// runOnce drives Run until the resolver has been called (or a deadline
// passes), then cancels. The worker's own loop does the claiming, so this
// exercises the real gates rather than a hand-called helper.
func runUntilResolved(t *testing.T, w *Worker, r *fakeResolver) bool {
	return runUntilResolvedWithin(t, w, r, 2*time.Second)
}

// runUntilResolvedWithin is runUntilResolved with an explicit deadline. A test
// asserting a call does NOT happen has to wait out its whole deadline, so the
// gate tests pass a short one rather than sitting for two seconds each.
func runUntilResolvedWithin(t *testing.T, w *Worker, r *fakeResolver, within time.Duration) bool {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	deadline := time.After(within)
	for {
		if len(r.calls()) > 0 {
			cancel()
			<-done
			return true
		}
		select {
		case <-deadline:
			cancel()
			<-done
			return false
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func nextMetaRefreshAt(t *testing.T, s *channels.Store, channelID string) string {
	t.Helper()
	var next sql.NullString
	err := s.DB().QueryRow(
		`SELECT next_meta_refresh_at FROM subscriptions WHERE channel_id = ?`, channelID).Scan(&next)
	if err != nil {
		t.Fatalf("read schedule: %v", err)
	}
	return next.String
}

func TestWorker_refreshesADueChannelAndReschedulesIt(t *testing.T) {
	s := newTestStore(t)
	seedDue(t, s, "UCa")
	r := &fakeResolver{info: ytdlp.ChannelInfo{
		UCID: "UCa", Name: "New Name", Handle: "@new", Description: "Fresh copy.",
		Subscribers: 4200, Verified: true,
	}}
	w := newTestWorker(t, s, r, Deps{})

	if !runUntilResolved(t, w, r) {
		t.Fatal("worker never refreshed the due channel")
	}

	c, err := s.Get("UCa")
	if err != nil || c == nil {
		t.Fatalf("get: %v, %v", c, err)
	}
	if c.Name != "New Name" || c.Description != "Fresh copy." || c.Subscribers != 4200 || !c.Verified {
		t.Fatalf("metadata was not updated: %+v", *c)
	}
	if !c.ResolveOk {
		t.Fatal("a successful refresh did not set resolve_ok")
	}
	// Rescheduled roughly a week out (7d ± 12h), so it is no longer due.
	if next := nextMetaRefreshAt(t, s, "UCa"); next < "2026-07-28" || next > "2026-07-30" {
		t.Fatalf("next_meta_refresh_at = %q; want ~7 days out", next)
	}
}

// TestWorker_failedRefreshIsStillRescheduled is the anti-hammering invariant:
// a channel that fails must not stay due, or the worker retries it every
// single poll forever.
func TestWorker_failedRefreshIsStillRescheduled(t *testing.T) {
	s := newTestStore(t)
	seedDue(t, s, "UCa")
	r := &fakeResolver{err: errors.New("channel unavailable")}
	w := newTestWorker(t, s, r, Deps{})

	if !runUntilResolved(t, w, r) {
		t.Fatal("worker never attempted the due channel")
	}

	if next := nextMetaRefreshAt(t, s, "UCa"); next < "2026-07-28" || next > "2026-07-30" {
		t.Fatalf("a failed refresh left next_meta_refresh_at = %q", next)
	}
	// The stored metadata survives a failed attempt; only the freshness claim
	// changes.
	c, err := s.Get("UCa")
	if err != nil || c == nil {
		t.Fatalf("get: %v, %v", c, err)
	}
	if c.Name != "Old Name" {
		t.Fatalf("a failed refresh clobbered the stored name: %q", c.Name)
	}
	if c.ResolveOk {
		t.Fatal("a failed refresh still claims resolve_ok")
	}
}

// TestWorker_failureDoesNotFeedTheDeadScanCounter: deciding a channel is gone
// belongs to the scan scheduler, which guards that call against peeq's own
// cookie being the real problem. A metadata refresh must never nudge a channel
// toward auto-unsubscription.
func TestWorker_failureDoesNotFeedTheDeadScanCounter(t *testing.T) {
	s := newTestStore(t)
	seedDue(t, s, "UCa")
	r := &fakeResolver{err: errors.New("channel unavailable")}
	w := newTestWorker(t, s, r, Deps{})

	if !runUntilResolved(t, w, r) {
		t.Fatal("worker never attempted the due channel")
	}

	var deadScans int
	if err := s.DB().QueryRow(
		`SELECT dead_scan_count FROM subscriptions WHERE channel_id = ?`, "UCa").Scan(&deadScans); err != nil {
		t.Fatalf("read dead_scan_count: %v", err)
	}
	if deadScans != 0 {
		t.Fatalf("dead_scan_count = %d; a failed metadata refresh must not count", deadScans)
	}
}

// TestWorker_panicIsContainedAndRescheduled: the refresher parses external
// input, so a panic must not take the process down — and the channel must
// still be pushed out of the due set, or the worker would panic on it forever.
func TestWorker_panicIsContainedAndRescheduled(t *testing.T) {
	s := newTestStore(t)
	seedDue(t, s, "UCa")
	r := &fakeResolver{panics: true}
	w := newTestWorker(t, s, r, Deps{})

	if !runUntilResolved(t, w, r) {
		t.Fatal("worker never attempted the due channel")
	}

	if next := nextMetaRefreshAt(t, s, "UCa"); next < "2026-07-28" || next > "2026-07-30" {
		t.Fatalf("a panicking refresh left next_meta_refresh_at = %q", next)
	}
}

// TestWorker_cookieGate: refreshing without a valid cookie only burns failed
// attempts and stamps them as tried, so the pass is skipped entirely.
func TestWorker_cookieGate(t *testing.T) {
	for _, status := range []string{"absent", "stale", "blocked"} {
		t.Run(status, func(t *testing.T) {
			s := newTestStore(t)
			seedDue(t, s, "UCa")
			r := &fakeResolver{}
			w := newTestWorker(t, s, r, Deps{
				CookieStatus: func(context.Context) string { return status },
			})

			if runUntilResolvedWithin(t, w, r, 200*time.Millisecond) {
				t.Fatalf("worker called YouTube with a %q cookie", status)
			}
		})
	}
}

// TestWorker_anonymousEscapeHatch mirrors the scan scheduler: the dev-only
// flag lets a cookieless install proceed, exactly like ytdlp's own cookieGate.
func TestWorker_anonymousEscapeHatch(t *testing.T) {
	s := newTestStore(t)
	seedDue(t, s, "UCa")
	r := &fakeResolver{info: ytdlp.ChannelInfo{UCID: "UCa", Name: "Anon"}}
	w := newTestWorker(t, s, r, Deps{
		CookieStatus:   func(context.Context) string { return "absent" },
		AllowAnonymous: true,
	})

	if !runUntilResolved(t, w, r) {
		t.Fatal("anonymous mode did not let the refresh through")
	}
}

// TestWorker_killSwitch: youtube_paused stops every outbound call, and this
// worker is no exception.
func TestWorker_killSwitch(t *testing.T) {
	s := newTestStore(t)
	seedDue(t, s, "UCa")
	r := &fakeResolver{}
	w := newTestWorker(t, s, r, Deps{
		YoutubePaused: func(context.Context) bool { return true },
	})

	if runUntilResolvedWithin(t, w, r, 200*time.Millisecond) {
		t.Fatal("worker called YouTube while paused")
	}
}

// TestWorker_drainsTheNeverReadBacklog covers the channel the user just
// tracked: it has no name, no artwork and no schedule, and it must fill itself
// in without anyone opening its page.
func TestWorker_drainsTheNeverReadBacklog(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(channels.Channel{ID: "UCnew"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Track("UCnew", "2026-07-22 11:00:00"); err != nil {
		t.Fatalf("track: %v", err)
	}
	r := &fakeResolver{info: ytdlp.ChannelInfo{UCID: "UCnew", Name: "Just Tracked", Subscribers: 12}}
	w := newTestWorker(t, s, r, Deps{})

	if !runUntilResolved(t, w, r) {
		t.Fatal("worker never picked up the never-read channel")
	}

	c, err := s.Get("UCnew")
	if err != nil || c == nil {
		t.Fatalf("get: %v, %v", c, err)
	}
	if c.Name != "Just Tracked" || c.Subscribers != 12 {
		t.Fatalf("backlog channel was not filled in: %+v", *c)
	}
}

// TestWorker_dueRotationBeatsTheBacklog: due work is work that was promised at
// a specific time; the backlog has no deadline and drains in the gaps.
func TestWorker_dueRotationBeatsTheBacklog(t *testing.T) {
	s := newTestStore(t)
	seedDue(t, s, "UCdue")
	if err := s.Upsert(channels.Channel{ID: "UCbacklog"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Track("UCbacklog", "2026-01-01 00:00:00"); err != nil {
		t.Fatalf("track: %v", err)
	}
	r := &fakeResolver{info: ytdlp.ChannelInfo{UCID: "UCdue", Name: "Refreshed"}}
	w := newTestWorker(t, s, r, Deps{})

	if !runUntilResolved(t, w, r) {
		t.Fatal("worker never refreshed anything")
	}
	if got := r.calls()[0]; !strings.Contains(got, "UCdue") {
		t.Fatalf("first call was %q; want the due channel before the backlog", got)
	}
}

// TestResolve_escapesTheIDIntoOneSegment asserts a channel id cannot steer the
// resolve at a different page. Ids reach Resolve from a URL path segment and
// Go's ServeMux hands back the DECODED value, so a request for
// ".. %2F.. %2Fwatch" (without the spaces) arrives as a real "../../watch" —
// raw concatenation would have built
// "https://www.youtube.com/channel/../../watch?v=abc" and yt-dlp would have
// followed it.
func TestResolve_escapesTheIDIntoOneSegment(t *testing.T) {
	s := newTestStore(t)
	r := &fakeResolver{info: ytdlp.ChannelInfo{Name: "X"}}
	f := &Refresher{Channels: s, Resolver: r, MediaDir: t.TempDir(), Logger: quietLogger()}

	if err := f.Resolve(context.Background(), "../../watch?v=abc", nil); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	got := r.calls()[0]
	if strings.Contains(got, "/watch") || strings.Contains(got, "?v=") {
		t.Fatalf("the id escaped its path segment: %q", got)
	}
	if !strings.HasPrefix(got, "https://www.youtube.com/channel/") {
		t.Fatalf("url = %q", got)
	}
}

// TestResolve_failureOnAnUnknownChannelIsRemembered mirrors the pre-existing
// httpapi behavior the refresher inherited: a channel with no row gets a bare
// one purely to carry resolved_at, so the failure is remembered rather than
// retried on every visit.
func TestResolve_failureOnAnUnknownChannelIsRemembered(t *testing.T) {
	s := newTestStore(t)
	r := &fakeResolver{err: errors.New("nope")}
	f := &Refresher{Channels: s, Resolver: r, MediaDir: t.TempDir(), Logger: quietLogger()}

	if err := f.Resolve(context.Background(), "UCghost", nil); err == nil {
		t.Fatal("resolve error was swallowed")
	}

	c, err := s.Get("UCghost")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if c == nil || c.ResolvedAt == "" {
		t.Fatalf("the failed attempt was not recorded: %+v", c)
	}
	if c.ResolveOk {
		t.Fatal("a failed resolve claims resolve_ok")
	}
}

// TestResolve_refusesADifferentChannel: yt-dlp's UCID is the identity of
// whatever it actually fetched, which a stale or redirecting url makes a
// DIFFERENT channel. Writing that onto this row would swap one channel's name,
// artwork and subscriber count for another's while resolve_ok asserts the
// result is current — and now that refreshes run weekly and unattended, that
// corruption would happen with nobody watching.
func TestResolve_refusesADifferentChannel(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(channels.Channel{ID: "UCa", Name: "Uncanny Expeditions"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	cached, err := s.Get("UCa")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	r := &fakeResolver{info: ytdlp.ChannelInfo{UCID: "UCsomeoneElse", Name: "Someone Else"}}
	f := &Refresher{Channels: s, Resolver: r, MediaDir: t.TempDir(), Logger: quietLogger()}

	if err := f.Resolve(context.Background(), "UCa", cached); err == nil {
		t.Fatal("resolving to a different channel was accepted")
	}

	after, err := s.Get("UCa")
	if err != nil || after == nil {
		t.Fatalf("get: %v, %v", after, err)
	}
	if after.Name != "Uncanny Expeditions" {
		t.Fatalf("another channel's metadata was written over this row: %q", after.Name)
	}
	// The refusal must also RECORD the attempt (#89). resolved_at is what
	// stops maybeResolveChannel re-fetching on every page visit, and a
	// mismatch is persistent — the url resolves elsewhere and will keep doing
	// so — so an unrecorded refusal turns one broken channel into an unbounded
	// stream of YouTube calls.
	if after.ResolvedAt == "" {
		t.Fatal("the mismatch was refused without recording the attempt; the channel will re-fetch on every visit")
	}
	if after.ResolveOk {
		t.Fatal("a refused mismatch still claims resolve_ok")
	}
}

// TestResolve_mismatchOnAnUnknownChannelIsRemembered is the same rule for a
// channel with no row at all: a bare row is written purely to carry
// resolved_at, so the mismatch is remembered rather than retried forever.
func TestResolve_mismatchOnAnUnknownChannelIsRemembered(t *testing.T) {
	s := newTestStore(t)
	r := &fakeResolver{info: ytdlp.ChannelInfo{UCID: "UCsomeoneElse", Name: "Someone Else"}}
	f := &Refresher{Channels: s, Resolver: r, MediaDir: t.TempDir(), Logger: quietLogger()}

	if err := f.Resolve(context.Background(), "UCghost", nil); err == nil {
		t.Fatal("resolving to a different channel was accepted")
	}

	c, err := s.Get("UCghost")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if c == nil || c.ResolvedAt == "" {
		t.Fatalf("the mismatch was not recorded: %+v", c)
	}
	if c.Name == "Someone Else" {
		t.Fatal("the other channel's name was written onto this row")
	}
}

// TestResolve_handlePrecedence: a stored handle came from a url the user
// pasted, so a refresh must not rewrite it underneath them — but yt-dlp's is
// what gives a handle to a channel that has none.
func TestResolve_handlePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name         string
		storedHandle string
		wantHandle   string
	}{
		{"keeps the handle the user pasted", "@uncanny", "@uncanny"},
		{"fills an absent handle from yt-dlp", "", "@fromytdlp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := s.Upsert(channels.Channel{ID: "UCa", Name: "Uncanny", Handle: tc.storedHandle}); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			cached, err := s.Get("UCa")
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			r := &fakeResolver{info: ytdlp.ChannelInfo{UCID: "UCa", Name: "Uncanny", Handle: "@fromytdlp"}}
			f := &Refresher{Channels: s, Resolver: r, MediaDir: t.TempDir(), Logger: quietLogger()}

			if err := f.Resolve(context.Background(), "UCa", cached); err != nil {
				t.Fatalf("resolve: %v", err)
			}
			after, err := s.Get("UCa")
			if err != nil || after == nil {
				t.Fatalf("get: %v, %v", after, err)
			}
			if after.Handle != tc.wantHandle {
				t.Fatalf("handle = %q, want %q", after.Handle, tc.wantHandle)
			}
		})
	}
}

// seedBacklog tracks a channel WITHOUT subscribing it: the never-read backlog
// shape. This is the row shape the panic/failure tests above miss, and the one
// where a missed settle is unbounded rather than merely late — there is no
// subscriptions row for MarkMetaRefreshed to update, so channels.resolved_at is
// the only thing that can take it back out of the claim set.
func seedBacklog(t *testing.T, s *channels.Store, channelID string) {
	t.Helper()
	if err := s.Upsert(channels.Channel{ID: channelID}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Track(channelID, "2026-07-22 11:00:00"); err != nil {
		t.Fatalf("track: %v", err)
	}
}

func resolvedAt(t *testing.T, s *channels.Store, channelID string) string {
	t.Helper()
	c, err := s.Get(channelID)
	if err != nil || c == nil {
		t.Fatalf("get %s: %v, %v", channelID, c, err)
	}
	return c.ResolvedAt
}

// TestWorker_panickingBacklogChannelIsNotReclaimed is the regression test for
// the review's top finding. A panic on an UNSUBSCRIBED backlog channel used to
// leave nothing written at all: MarkMetaRefreshed matched no subscriptions row,
// resolved_at stayed NULL, and ClaimUnresolved handed the same channel back
// every poll — a yt-dlp call and a panic every five minutes, forever.
func TestWorker_panickingBacklogChannelIsNotReclaimed(t *testing.T) {
	s := newTestStore(t)
	seedBacklog(t, s, "UCboom")
	r := &fakeResolver{panics: true}
	w := newTestWorker(t, s, r, Deps{})

	if !runUntilResolved(t, w, r) {
		t.Fatal("worker never attempted the backlog channel")
	}

	if resolvedAt(t, s, "UCboom") == "" {
		t.Fatal("a panicking backlog channel was left unstamped; it will be re-claimed every poll forever")
	}
	// The claim query itself must now pass it over — the property that actually
	// stops the loop, asserted directly rather than inferred from the column.
	if got, err := s.ClaimUnresolved("2026-07-22 12:00:00"); err != nil || got != nil {
		t.Fatalf("backlog still returns the panicking channel: %+v (err=%v)", got, err)
	}
}

// TestWorker_settleDoesNotOverwriteARealOutcome: the backstop stamp is
// conditional, so a successful resolve keeps the timestamp (and resolve_ok)
// that Resolve wrote rather than having it replaced by the settle that follows.
func TestWorker_settleDoesNotOverwriteARealOutcome(t *testing.T) {
	s := newTestStore(t)
	seedBacklog(t, s, "UCgood")
	r := &fakeResolver{info: ytdlp.ChannelInfo{UCID: "UCgood", Name: "Filled In", Subscribers: 99}}
	w := newTestWorker(t, s, r, Deps{})

	if !runUntilResolved(t, w, r) {
		t.Fatal("worker never picked up the backlog channel")
	}

	c, err := s.Get("UCgood")
	if err != nil || c == nil {
		t.Fatalf("get: %v, %v", c, err)
	}
	if c.Name != "Filled In" || c.Subscribers != 99 {
		t.Fatalf("metadata was not written: %+v", *c)
	}
	if !c.ResolveOk {
		t.Fatal("settle clobbered a successful resolve's resolve_ok")
	}
}

// fakeMetaRecorder captures activity events from the metadata worker.
type fakeMetaRecorder struct {
	mu     sync.Mutex
	events []activity.Event
}

func (f *fakeMetaRecorder) Record(e activity.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

// TestWorker_failedRefreshRecordsActivity is the Activity-feed half of the
// failed-refresh path: a failure records one channel_meta/warn row (a routine
// success records nothing — only failures are surfaced). runUntilResolved's
// <-done waits for the whole refresh (record included) to finish.
func TestWorker_failedRefreshRecordsActivity(t *testing.T) {
	s := newTestStore(t)
	r := &fakeResolver{err: errors.New("channel unavailable")}
	rec := &fakeMetaRecorder{}
	w := newTestWorker(t, s, r, Deps{Activity: rec})
	seedDue(t, s, "UCfail")

	if !runUntilResolved(t, w, r) {
		t.Fatal("resolver was never called")
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.events) != 1 {
		t.Fatalf("recorded %d events, want 1", len(rec.events))
	}
	e := rec.events[0]
	if e.Kind != activity.KindChannelMeta || e.Outcome != activity.OutcomeWarn {
		t.Fatalf("event = %+v, want channel_meta/warn", e)
	}
	if e.Subject != "Old Name" {
		t.Fatalf("subject = %q, want the cached channel name", e.Subject)
	}
}
