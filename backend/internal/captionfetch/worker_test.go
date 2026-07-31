package captionfetch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"database/sql"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

type harness struct {
	db       *sql.DB
	ledger   *channelvideos.Store
	videos   *videos.Store
	summary  *summaryjobs.Store
	channels *channels.Store
	mediaDir string
}

// newHarness builds a migrated database with one subscribed channel and one
// pending inbox video, which is the only state this worker ever starts from.
func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO channels (id, name) VALUES ('UC1', 'A channel')`); err != nil {
		t.Fatalf("insert channel: %v", err)
	}
	h := &harness{
		db: db, ledger: channelvideos.New(db), videos: videos.New(db),
		summary: summaryjobs.New(db), channels: channels.New(db),
		mediaDir: t.TempDir(),
	}
	if err := h.ledger.Insert(channelvideos.Entry{
		VideoID: "v1", ChannelID: "UC1", Title: "A video",
		URL: "https://youtu.be/v1", State: channelvideos.StatePending,
	}); err != nil {
		t.Fatalf("insert ledger row: %v", err)
	}
	return h
}

// fetcher is a scripted SubtitleFetcher: one entry per call.
type fetcher struct {
	results []string
	errs    []error
	calls   int
}

func (f *fetcher) Subtitles(ctx context.Context, videoID, rawURL, subLang string) (string, error) {
	i := f.calls
	f.calls++
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	var rel string
	if i < len(f.results) {
		rel = f.results[i]
	}
	return rel, err
}

func (h *harness) worker(f *fetcher) *Worker {
	return NewWorker(Deps{Fetcher: f, Ledger: h.ledger, Videos: h.videos, Summaries: h.summary, MediaDir: h.mediaDir})
}

// writeCaption puts a .vtt where the fetcher claims to have written one. The
// worker reads it into the row and removes it, so a test that skips this sees
// the caption treated as never having arrived.
func writeCaption(t *testing.T, h *harness, rel string) {
	t.Helper()
	full := filepath.Join(h.mediaDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte("WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nhello\n"), 0o644); err != nil {
		t.Fatalf("write caption: %v", err)
	}
}

// TestCaptionsArriveQueuesExactlyOneSummary is the happy path: a caption file
// lands, the video gets a row it can hang a summary off, and the analysis is
// queued once.
func TestCaptionsArriveQueuesExactlyOneSummary(t *testing.T) {
	h := newHarness(t)
	rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.en.vtt")
	writeCaption(t, h, rel)
	f := &fetcher{results: []string{rel}}

	h.worker(f).pass(context.Background())

	v, err := h.videos.Get("v1")
	if err != nil || v == nil {
		t.Fatalf("expected a videos row: %v", err)
	}
	if v.Status != videos.StatusNew {
		t.Fatalf("status = %q, want new — the video is read, not requested", v.Status)
	}
	// The text is what matters, and it is stored as a caption read: an
	// inbox video's analysis stops after the prose.
	tr, terr := h.videos.GetTranscript("v1")
	if terr != nil || tr == nil {
		t.Fatalf("transcript not stored: %v, %v", tr, terr)
	}
	if tr.Source != videos.TranscriptSourceCaption {
		t.Fatalf("source = %q, want caption", tr.Source)
	}
	active, err := h.summary.ListActive()
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("queued %d summary jobs, want exactly 1", len(active))
	}

	// A settled row must never be offered again, or every tick re-fetches a
	// caption peeq already has.
	if c, err := h.ledger.NextCaptionCandidate(); err != nil || c != nil {
		t.Fatalf("candidate after success = %v (err %v), want none", c, err)
	}

	// And it stays in the Inbox: the summary informs a decision nobody has made.
	e, err := h.ledger.Get("v1")
	if err != nil || e == nil {
		t.Fatalf("get ledger row: %v", err)
	}
	if e.State != channelvideos.StatePending {
		t.Fatalf("ledger state = %q, want it still pending", e.State)
	}
}

// TestLadderRunsOutAndSettlesAsNoTranscript walks the whole retry ladder. The
// count matters: with CaptionMaxAttempts of 5 and four rungs of Backoff, a
// fifth miss is the last one, and it has to leave a terminal state behind —
// otherwise the card spins forever on a video YouTube never captioned.
func TestLadderRunsOutAndSettlesAsNoTranscript(t *testing.T) {
	h := newHarness(t)
	f := &fetcher{}
	w := h.worker(f)

	// Each rung schedules the next attempt into the future, so the wait is
	// cleared between passes rather than slept through.
	for i := 0; i < channelvideos.CaptionMaxAttempts; i++ {
		w.pass(context.Background())
		mustBeDue(t, h)
	}

	if f.calls != channelvideos.CaptionMaxAttempts {
		t.Fatalf("fetched %d times, want %d", f.calls, channelvideos.CaptionMaxAttempts)
	}
	v, err := h.videos.Get("v1")
	if err != nil || v == nil {
		t.Fatalf("get video: %v", err)
	}
	if v.SummaryStatus != videos.SummaryNoTranscript {
		t.Fatalf("summary_status = %q, want no_transcript", v.SummaryStatus)
	}
	if c, err := h.ledger.NextCaptionCandidate(); err != nil || c != nil {
		t.Fatalf("candidate after the last rung = %v (err %v), want none", c, err)
	}
	active, err := h.summary.ListActive()
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("queued %d summary jobs for a video with no transcript, want 0", len(active))
	}
}

// TestGatedFetchGivesTheAttemptBack is the failure mode the ladder would
// otherwise turn into data loss. While the cookie is expired or the kill-switch
// is on, peeq never talks to YouTube at all — so spending rungs during that
// window would silently exhaust every video discovered in it, and they would
// all settle as having no transcript the moment the cookie was fixed.
//
// All four sentinels, not just the two gates. ErrCookieExpired and ErrBlocked
// arrive via stderr classification rather than a gate, which makes them look
// like this call failing — but during bot detection or an expired cookie they
// hit every video alike, so counting them would exhaust the whole inbox within
// five ticks and settle it permanently as no_transcript. That is the half of
// this failure mode that actually happens: cookies expire far more often than
// the kill-switch is thrown.
func TestGatedFetchGivesTheAttemptBack(t *testing.T) {
	h := newHarness(t)
	f := &fetcher{errs: []error{
		ytdlp.ErrNoCookie, ytdlp.ErrPaused,
		ytdlp.ErrCookieExpired, ytdlp.ErrBlocked,
	}}
	w := h.worker(f)

	for range f.errs {
		w.pass(context.Background())
	}

	if f.calls != 4 {
		t.Fatalf("fetched %d times, want 4", f.calls)
	}
	c, err := h.ledger.NextCaptionCandidate()
	if err != nil {
		t.Fatalf("candidate: %v", err)
	}
	if c == nil {
		t.Fatal("a gated video must still be due; it was never actually tried")
	}
	if c.Attempts != 0 {
		t.Fatalf("attempts = %d after four gated passes, want 0", c.Attempts)
	}
}

// TestOptedOutChannelIsNeverRead pins the per-channel switch at the only place
// that can honour it — the claim. A worker that fetched first and checked later
// would already have cost the YouTube call the switch exists to avoid.
func TestOptedOutChannelIsNeverRead(t *testing.T) {
	h := newHarness(t)
	if _, ok, err := h.channels.SetAutoSummary("UC1", false); err != nil || !ok {
		t.Fatalf("opt out: ok=%v err=%v", ok, err)
	}
	f := &fetcher{results: []string{"never"}}

	h.worker(f).pass(context.Background())

	if f.calls != 0 {
		t.Fatalf("fetched %d times for an opted-out channel, want 0", f.calls)
	}
	if v, err := h.videos.Get("v1"); err != nil || v != nil {
		t.Fatalf("opted-out video got a row (%v, err %v); it must stay untouched", v, err)
	}
}

// TestErrorOnTheLastRungStillSettles covers the difference between "YouTube has
// no captions" and "the call failed". Both exhaust the ladder, and both must
// end in a state the card can render — a failure on the final attempt is not a
// reason to leave the video spinning.
func TestErrorOnTheLastRungStillSettles(t *testing.T) {
	h := newHarness(t)
	boom := errors.New("yt-dlp exploded")
	f := &fetcher{errs: []error{boom, boom, boom, boom, boom}}
	w := h.worker(f)

	for i := 0; i < channelvideos.CaptionMaxAttempts; i++ {
		w.pass(context.Background())
		mustBeDue(t, h)
	}

	v, err := h.videos.Get("v1")
	if err != nil || v == nil {
		t.Fatalf("get video: %v", err)
	}
	if v.SummaryStatus != videos.SummaryNoTranscript {
		t.Fatalf("summary_status = %q, want no_transcript", v.SummaryStatus)
	}
}

// mustBeDue clears the scheduled wait so the next pass runs immediately,
// standing in for the ladder's delay elapsing. It writes SQL directly rather
// than going through the store: no production caller ever wants "forget the
// wait but keep the count", so exposing a method for it would be inventing API
// for the tests to use.
func mustBeDue(t *testing.T, h *harness) {
	t.Helper()
	if _, err := h.db.Exec(
		`UPDATE channel_videos SET next_caption_attempt_at = NULL WHERE video_id = 'v1'`); err != nil {
		t.Fatalf("make due: %v", err)
	}
}

// failingLedger lets a test drive the error branches that a healthy database
// never reaches: they are the ones that decide whether a fetch is attempted at
// all, so silently swallowing one would mean burning YouTube calls with nothing
// recorded.
type failingLedger struct {
	*channelvideos.Store
	failNext   bool
	failRecord bool
}

func (f *failingLedger) NextCaptionCandidate() (*channelvideos.CaptionCandidate, error) {
	if f.failNext {
		return nil, errors.New("ledger unavailable")
	}
	return f.Store.NextCaptionCandidate()
}

func (f *failingLedger) RecordCaptionAttempt(videoID string, delaySeconds int) error {
	if f.failRecord {
		return errors.New("write failed")
	}
	return f.Store.RecordCaptionAttempt(videoID, delaySeconds)
}

// A ledger read that fails must not be read as "nothing to do" — and must
// certainly not lead to a fetch with no attempt recorded against it.
func TestClaimFailureFetchesNothing(t *testing.T) {
	h := newHarness(t)
	f := &fetcher{results: []string{"never"}}
	w := NewWorker(Deps{
		Fetcher:   f,
		Ledger:    &failingLedger{Store: h.ledger, failNext: true},
		Videos:    h.videos,
		Summaries: h.summary,
	})

	w.pass(context.Background())

	if f.calls != 0 {
		t.Fatalf("fetched %d times after a failed claim, want 0", f.calls)
	}
}

// The rung is burned before the fetch precisely so a crash cannot loop forever.
// If that write fails, the fetch must not happen either — otherwise the guard
// is gone and every tick calls YouTube again.
func TestAttemptWriteFailureFetchesNothing(t *testing.T) {
	h := newHarness(t)
	f := &fetcher{results: []string{"never"}}
	w := NewWorker(Deps{
		Fetcher:   f,
		Ledger:    &failingLedger{Store: h.ledger, failRecord: true},
		Videos:    h.videos,
		Summaries: h.summary,
	})

	w.pass(context.Background())

	if f.calls != 0 {
		t.Fatalf("fetched %d times after a failed attempt write, want 0", f.calls)
	}
	if v, err := h.videos.Get("v1"); err != nil || v != nil {
		t.Fatalf("a row was created despite no attempt being recorded: %v (err %v)", v, err)
	}
}

// An empty inbox is the common state, and it must be silent: no fetch, no row,
// no summary job.
func TestNothingDueDoesNothing(t *testing.T) {
	h := newHarness(t)
	if err := h.ledger.MarkCaptionSettled("v1"); err != nil {
		t.Fatalf("settle: %v", err)
	}
	f := &fetcher{results: []string{"never"}}

	h.worker(f).pass(context.Background())

	if f.calls != 0 {
		t.Fatalf("fetched %d times with nothing due, want 0", f.calls)
	}
}

// Run must return on a cancelled context rather than spinning: it is one of ten
// goroutines the process waits on at shutdown.
func TestRunStopsOnContextCancel(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		h.worker(&fetcher{}).Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return on a cancelled context")
	}
}

// A caption that yt-dlp reported but did not actually leave on disk must not
// queue an analysis: there is nothing to summarize, and a summary job would run
// against a video with no transcript.
func TestCaptionUnreadableDoesNotQueueASummary(t *testing.T) {
	h := newHarness(t)
	rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.en.vtt")
	f := &fetcher{results: []string{rel}} // no writeCaption: the file is absent

	h.worker(f).pass(context.Background())

	if tr, err := h.videos.GetTranscript("v1"); err != nil || tr != nil {
		t.Fatalf("a transcript was stored from a file that does not exist: %v, %v", tr, err)
	}
	active, err := h.summary.ListActive()
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("queued %d summaries for a video with no transcript, want 0", len(active))
	}
}

// The caption directory goes as soon as its text is in the row, so a read
// leaves nothing behind in the media tree.
func TestCaptionArrival_removesTheCaptionDirectory(t *testing.T) {
	h := newHarness(t)
	rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.en.vtt")
	writeCaption(t, h, rel)
	f := &fetcher{results: []string{rel}}

	h.worker(f).pass(context.Background())

	if _, err := os.Stat(filepath.Join(h.mediaDir, ytdlp.SummaryDirName, "v1")); !os.IsNotExist(err) {
		t.Fatalf("caption directory survived the read (err = %v)", err)
	}
}
