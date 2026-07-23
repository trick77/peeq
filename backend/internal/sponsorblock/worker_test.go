package sponsorblock

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/videos"
)

// quietLogger keeps worker logs out of the test output; behaviour is asserted
// from the fake store, not from what was printed.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeFetcher stands in for the SponsorBlock API.
type fakeFetcher struct {
	mu     sync.Mutex
	asked  []string
	segs   []Segment
	err    error
	panics bool
}

func (f *fakeFetcher) Segments(_ context.Context, videoID string, _ float64) ([]Segment, error) {
	f.mu.Lock()
	f.asked = append(f.asked, videoID)
	f.mu.Unlock()
	if f.panics {
		panic("the API returned something absurd")
	}
	return f.segs, f.err
}

func (f *fakeFetcher) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

// fakeStore records what the worker claimed and stored.
type fakeStore struct {
	mu        sync.Mutex
	pending   []videos.SponsorblockCandidate
	claimErr  error
	storeErr  error
	stored    map[string]string
	claimSize int
}

func (s *fakeStore) ClaimSponsorblockStale(limit int) ([]videos.SponsorblockCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimSize = limit
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	out := s.pending
	// A real claim stops returning a video once it is stamped; the fake models
	// that by handing each batch out exactly once.
	s.pending = nil
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *fakeStore) SetSponsorblockSegments(id, segmentsJSON string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storeErr != nil {
		return s.storeErr
	}
	if s.stored == nil {
		s.stored = map[string]string{}
	}
	s.stored[id] = segmentsJSON
	return nil
}

func (s *fakeStore) written() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.stored {
		out[k] = v
	}
	return out
}

func newTestWorker(t *testing.T, store *fakeStore, fetcher *fakeFetcher) *Worker {
	t.Helper()
	return NewWorker(Deps{
		Fetcher:        fetcher,
		Videos:         store,
		PollInterval:   time.Hour, // one pass per Run in tests
		BetweenLookups: -1,        // no spacing
		Logger:         quietLogger(),
	})
}

// TestWorker_storesSegmentsForEachClaimedVideo is the happy path: every video
// in the batch is looked up and written.
func TestWorker_storesSegmentsForEachClaimedVideo(t *testing.T) {
	store := &fakeStore{pending: []videos.SponsorblockCandidate{
		{ID: "v1", DurationSeconds: 600},
		{ID: "v2", DurationSeconds: 300},
	}}
	fetcher := &fakeFetcher{segs: []Segment{{Category: "sponsor", StartTime: 10, EndTime: 25}}}
	testee := newTestWorker(t, store, fetcher)

	testee.pass(context.Background())

	if got := fetcher.calls(); len(got) != 2 {
		t.Fatalf("looked up %v, want both videos", got)
	}
	written := store.written()
	if len(written) != 2 {
		t.Fatalf("stored %v, want 2 rows", written)
	}
	want := `[{"category":"sponsor","start_time":10,"end_time":25}]`
	if written["v1"] != want {
		t.Fatalf("stored v1 = %s, want %s", written["v1"], want)
	}
}

// TestWorker_storesEmptyResult: most videos have no segments at all. Storing
// "[]" is what stamps the refresh time and stops the claim query handing the
// same video back every minute forever.
func TestWorker_storesEmptyResult(t *testing.T) {
	store := &fakeStore{pending: []videos.SponsorblockCandidate{{ID: "v1", DurationSeconds: 600}}}
	fetcher := &fakeFetcher{segs: nil}
	testee := newTestWorker(t, store, fetcher)

	testee.pass(context.Background())

	if got := store.written()["v1"]; got != "[]" {
		t.Fatalf("stored v1 = %q, want %q — an empty answer must still be recorded", got, "[]")
	}
}

// TestWorker_lookupFailureLeavesVideoUnstamped: a failed lookup must not
// record "no segments", or a transient outage would silently blank the whole
// library until the 30-day refresh came round.
func TestWorker_lookupFailureLeavesVideoUnstamped(t *testing.T) {
	store := &fakeStore{pending: []videos.SponsorblockCandidate{{ID: "v1"}, {ID: "v2"}}}
	fetcher := &fakeFetcher{err: errors.New("sponsorblock is down")}
	testee := newTestWorker(t, store, fetcher)

	testee.pass(context.Background())

	if written := store.written(); len(written) != 0 {
		t.Fatalf("stored %v, want nothing written on a failed lookup", written)
	}
	// The rest of the batch is still attempted — one bad video must not stall
	// the drain.
	if got := fetcher.calls(); len(got) != 2 {
		t.Fatalf("looked up %v, want the batch to continue past the failure", got)
	}
}

// TestWorker_survivesPanic: the fetcher parses remote input, so a panic there
// must be contained per video rather than taking the process down.
func TestWorker_survivesPanic(t *testing.T) {
	store := &fakeStore{pending: []videos.SponsorblockCandidate{{ID: "v1"}}}
	fetcher := &fakeFetcher{panics: true}
	testee := newTestWorker(t, store, fetcher)

	testee.pass(context.Background()) // must not panic out of the worker

	if written := store.written(); len(written) != 0 {
		t.Fatalf("stored %v, want nothing after a panic", written)
	}
}

// TestWorker_claimFailureKeepsLoopAlive: a database error is logged and the
// loop continues to the next pass rather than exiting.
func TestWorker_claimFailureKeepsLoopAlive(t *testing.T) {
	store := &fakeStore{claimErr: errors.New("database is locked")}
	testee := newTestWorker(t, store, &fakeFetcher{})

	if !testee.pass(context.Background()) {
		t.Fatal("pass() = false after a claim error, want the loop to keep running")
	}
}

// TestWorker_storeFailureIsLogged covers the write-error path: it must not
// panic and must not be mistaken for a successful store.
func TestWorker_storeFailureIsLogged(t *testing.T) {
	store := &fakeStore{
		pending:  []videos.SponsorblockCandidate{{ID: "v1"}},
		storeErr: errors.New("disk full"),
	}
	testee := newTestWorker(t, store, &fakeFetcher{segs: []Segment{{Category: "sponsor", EndTime: 5}}})

	testee.pass(context.Background())

	if written := store.written(); len(written) != 0 {
		t.Fatalf("stored %v, want nothing after a write error", written)
	}
}

// TestWorker_cancelledContextStopsMidBatch: shutdown must not have to wait for
// the rest of a claimed batch.
func TestWorker_cancelledContextStopsMidBatch(t *testing.T) {
	store := &fakeStore{pending: []videos.SponsorblockCandidate{{ID: "v1"}, {ID: "v2"}, {ID: "v3"}}}
	fetcher := &fakeFetcher{}
	testee := newTestWorker(t, store, fetcher)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if testee.pass(ctx) {
		t.Fatal("pass() = true on a cancelled context, want false so Run returns")
	}
	if got := fetcher.calls(); len(got) != 0 {
		t.Fatalf("looked up %v on a cancelled context, want none", got)
	}
}

// TestWorker_runStopsOnContextCancel is the shutdown contract main.go relies
// on: Run returns rather than leaking a goroutine past process shutdown.
func TestWorker_runStopsOnContextCancel(t *testing.T) {
	store := &fakeStore{}
	testee := NewWorker(Deps{
		Fetcher:      &fakeFetcher{},
		Videos:       store,
		PollInterval: time.Millisecond,
		Logger:       quietLogger(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		testee.Run(ctx)
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

// TestNewWorker_defaults documents the batch sizing main.go gets for free.
func TestNewWorker_defaults(t *testing.T) {
	testee := NewWorker(Deps{Fetcher: &fakeFetcher{}, Videos: &fakeStore{}})
	if testee.d.PollInterval != pollInterval {
		t.Fatalf("PollInterval = %v, want %v", testee.d.PollInterval, pollInterval)
	}
	if testee.d.BatchSize != batchSize {
		t.Fatalf("BatchSize = %d, want %d", testee.d.BatchSize, batchSize)
	}
	if testee.d.BetweenLookups != betweenLookups {
		t.Fatalf("BetweenLookups = %v, want %v", testee.d.BetweenLookups, betweenLookups)
	}
	if testee.d.Logger == nil {
		t.Fatal("Logger = nil, want a default")
	}
}

// TestWorker_claimsConfiguredBatchSize keeps the claim bounded: an unbounded
// claim on a large library would hold the whole table in memory.
func TestWorker_claimsConfiguredBatchSize(t *testing.T) {
	store := &fakeStore{}
	testee := NewWorker(Deps{
		Fetcher: &fakeFetcher{}, Videos: store,
		BatchSize: 7, Logger: quietLogger(),
	})

	testee.pass(context.Background())

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.claimSize != 7 {
		t.Fatalf("claim limit = %d, want 7", store.claimSize)
	}
}

// TestWorker_cancelDuringSpacingStopsBatch: the pause between lookups must be
// a cancellation point too, or shutdown would wait out the rest of a batch's
// spacing.
func TestWorker_cancelDuringSpacingStopsBatch(t *testing.T) {
	store := &fakeStore{pending: []videos.SponsorblockCandidate{{ID: "v1"}, {ID: "v2"}, {ID: "v3"}}}
	fetcher := &fakeFetcher{}
	ctx, cancel := context.WithCancel(context.Background())
	testee := NewWorker(Deps{
		Fetcher: fetcher, Videos: store,
		// Long enough that the test would hang if the sleep ignored ctx.
		BetweenLookups: time.Hour,
		Logger:         quietLogger(),
	})

	// Cancel once the first video has been looked up, so the spacing before
	// the second one is what observes it.
	go func() {
		for len(fetcher.calls()) == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()

	if testee.pass(ctx) {
		t.Fatal("pass() = true, want false once the spacing observed the cancel")
	}
	if got := fetcher.calls(); len(got) != 1 {
		t.Fatalf("looked up %v, want the batch to stop after the first", got)
	}
}

// TestWorker_runLoopsUntilCancelled covers the loop body itself: a pass
// happens, and the poll interval is a cancellation point.
func TestWorker_runLoopsUntilCancelled(t *testing.T) {
	store := &fakeStore{pending: []videos.SponsorblockCandidate{{ID: "v1"}}}
	fetcher := &fakeFetcher{}
	testee := NewWorker(Deps{
		Fetcher: fetcher, Videos: store,
		PollInterval:   time.Hour, // only the cancel can end the wait
		BetweenLookups: -1,
		Logger:         quietLogger(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		testee.Run(ctx)
		close(done)
	}()

	// The first pass must complete before the loop parks on the interval.
	deadline := time.After(2 * time.Second)
	for len(store.written()) == 0 {
		select {
		case <-deadline:
			t.Fatal("first pass never stored anything")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}
