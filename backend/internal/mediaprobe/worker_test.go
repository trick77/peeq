package mediaprobe

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

// fakeStore records what the worker wrote and hands out candidates once, so a
// pass that re-lists (as Run does) does not loop on the same rows forever.
type fakeStore struct {
	mu         sync.Mutex
	batches    [][]videos.ProbeCandidate
	listErr    error
	setErr     error
	written    map[string]videos.ProbeResult
	writeOrder []string
	lastLimit  int
}

func newFakeStore(batches ...[]videos.ProbeCandidate) *fakeStore {
	return &fakeStore{batches: batches, written: map[string]videos.ProbeResult{}}
}

func (f *fakeStore) UnprobedDownloaded(limit int) ([]videos.ProbeCandidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastLimit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}
	if len(f.batches) == 0 {
		return nil, nil
	}
	batch := f.batches[0]
	f.batches = f.batches[1:]
	return batch, nil
}

func (f *fakeStore) SetProbed(id string, res videos.ProbeResult) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.written[id] = res
	f.writeOrder = append(f.writeOrder, id)
	return f.setErr
}

func (f *fakeStore) wrote(id string) (videos.ProbeResult, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, ok := f.written[id]
	return res, ok
}

// fakeProber answers per path, defaulting to an error for anything unlisted.
type fakeProber struct {
	byPath map[string]Info
	errs   map[string]error
	calls  []string
	mu     sync.Mutex
}

func (f *fakeProber) Probe(_ context.Context, path string) (Info, error) {
	f.mu.Lock()
	f.calls = append(f.calls, path)
	f.mu.Unlock()
	if err, ok := f.errs[path]; ok {
		return Info{}, err
	}
	if info, ok := f.byPath[path]; ok {
		return info, nil
	}
	return Info{}, errors.New("no such file")
}

func quietWorker(d Deps) *Worker {
	d.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewWorker(d)
}

func TestPass_storesWhatTheProbeFound(t *testing.T) {
	store := newFakeStore([]videos.ProbeCandidate{{ID: "v1", MediaPath: "/m/v1.mp4"}})
	prober := &fakeProber{byPath: map[string]Info{
		"/m/v1.mp4": {Container: "mp4", VideoCodec: "h264", VideoHeight: 1080, AudioCodec: "aac"},
	}}
	testee := quietWorker(Deps{Prober: prober, Videos: store})

	if !testee.pass(context.Background()) {
		t.Fatal("pass reported cancellation")
	}

	got, ok := store.wrote("v1")
	if !ok {
		t.Fatal("nothing written for v1")
	}
	want := videos.ProbeResult{Container: "mp4", VideoCodec: "h264", VideoHeight: 1080, AudioCodec: "aac"}
	if got != want {
		t.Errorf("wrote %+v, want %+v", got, want)
	}
}

// The failure path MUST still write. probed_at is stamped by SetProbed, and
// that stamp is the only thing stopping the sweep from re-probing a deleted
// or corrupt file on every single pass, forever.
func TestPass_recordsTheAttemptEvenWhenTheProbeFails(t *testing.T) {
	store := newFakeStore([]videos.ProbeCandidate{{ID: "gone", MediaPath: "/m/gone.mp4"}})
	prober := &fakeProber{errs: map[string]error{"/m/gone.mp4": errors.New("no such file")}}
	testee := quietWorker(Deps{Prober: prober, Videos: store})

	testee.pass(context.Background())

	got, ok := store.wrote("gone")
	if !ok {
		t.Fatal("failed probe wrote nothing; the sweep would retry this file forever")
	}
	if got != (videos.ProbeResult{}) {
		t.Errorf("wrote %+v, want a zero result", got)
	}
}

func TestPass_continuesPastOneBadFile(t *testing.T) {
	store := newFakeStore([]videos.ProbeCandidate{
		{ID: "bad", MediaPath: "/m/bad.mp4"},
		{ID: "good", MediaPath: "/m/good.mp4"},
	})
	prober := &fakeProber{
		errs:   map[string]error{"/m/bad.mp4": errors.New("boom")},
		byPath: map[string]Info{"/m/good.mp4": {Container: "mp4", VideoCodec: "vp9"}},
	}
	testee := quietWorker(Deps{Prober: prober, Videos: store})

	testee.pass(context.Background())

	if _, ok := store.wrote("good"); !ok {
		t.Error("a failing file stopped the batch")
	}
}

func TestPass_listErrorKeepsTheLoopAlive(t *testing.T) {
	store := newFakeStore()
	store.listErr = errors.New("db is busy")
	testee := quietWorker(Deps{Prober: &fakeProber{}, Videos: store})

	if !testee.pass(context.Background()) {
		t.Error("pass reported cancellation on a store error; the loop would exit for good")
	}
}

// A store write failure must not be fatal either: the next pass simply picks
// the row up again, which is the correct outcome for a transient DB error.
func TestPass_storeErrorIsSurvivable(t *testing.T) {
	store := newFakeStore([]videos.ProbeCandidate{{ID: "v1", MediaPath: "/m/v1.mp4"}})
	store.setErr = errors.New("db is locked")
	prober := &fakeProber{byPath: map[string]Info{"/m/v1.mp4": {Container: "mp4"}}}
	testee := quietWorker(Deps{Prober: prober, Videos: store})

	if !testee.pass(context.Background()) {
		t.Error("pass reported cancellation on a write error")
	}
}

func TestPass_stopsPromptlyWhenCancelled(t *testing.T) {
	store := newFakeStore([]videos.ProbeCandidate{
		{ID: "v1", MediaPath: "/m/v1.mp4"},
		{ID: "v2", MediaPath: "/m/v2.mp4"},
	})
	prober := &fakeProber{byPath: map[string]Info{
		"/m/v1.mp4": {Container: "mp4"},
		"/m/v2.mp4": {Container: "mp4"},
	}}
	testee := quietWorker(Deps{Prober: prober, Videos: store})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if testee.pass(ctx) {
		t.Error("pass reported success on a cancelled context")
	}
	if _, ok := store.wrote("v1"); ok {
		t.Error("worked through the batch despite cancellation")
	}
}

func TestRun_exitsOnContextCancel(t *testing.T) {
	store := newFakeStore()
	testee := quietWorker(Deps{
		Prober: &fakeProber{}, Videos: store,
		PollInterval: time.Hour,
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
		t.Fatal("Run did not return after cancellation")
	}
}

func TestNewWorker_appliesDefaults(t *testing.T) {
	testee := NewWorker(Deps{Prober: &fakeProber{}, Videos: newFakeStore()})

	if testee.d.PollInterval != pollInterval {
		t.Errorf("PollInterval = %v, want %v", testee.d.PollInterval, pollInterval)
	}
	if testee.d.BatchSize != batchSize {
		t.Errorf("BatchSize = %d, want %d", testee.d.BatchSize, batchSize)
	}
	if testee.d.Logger == nil {
		t.Error("Logger left nil")
	}
}

func TestPass_boundsTheBatch(t *testing.T) {
	store := newFakeStore(nil)
	testee := quietWorker(Deps{Prober: &fakeProber{}, Videos: store, BatchSize: 7})

	testee.pass(context.Background())

	if store.lastLimit != 7 {
		t.Errorf("claimed with limit %d, want 7", store.lastLimit)
	}
}

// cancelOnListStore cancels the run's context as it hands back a batch, so a
// test can drive Run to the exact point after pass() succeeded.
type cancelOnListStore struct {
	*fakeStore
	cancel context.CancelFunc
}

func (c *cancelOnListStore) UnprobedDownloaded(limit int) ([]videos.ProbeCandidate, error) {
	out, err := c.fakeStore.UnprobedDownloaded(limit)
	c.cancel()
	return out, err
}

// panicProber stands in for a prober that blows up on malformed output from
// the external binary.
type panicProber struct{}

func (panicProber) Probe(context.Context, string) (Info, error) {
	panic("ffprobe output was not what we assumed")
}

// A panic in the prober must not take the process down with it. It parses the
// output of an external binary, so the guard is the difference between one
// unprobeable file and a dead peeq.
func TestPass_survivesAProberPanic(t *testing.T) {
	store := newFakeStore([]videos.ProbeCandidate{{ID: "v1", MediaPath: "/m/v1.mp4"}})
	testee := quietWorker(Deps{Prober: panicProber{}, Videos: store})

	if !testee.pass(context.Background()) {
		t.Error("pass reported cancellation after a panic; the loop would exit for good")
	}
	// The guard swallows the panic before the write, so the row stays unprobed
	// and the next pass picks it up again.
	if _, ok := store.wrote("v1"); ok {
		t.Error("a panicking probe still wrote a result")
	}
}

// Run must return, not spin, when a batch is abandoned half-way: pass()
// reporting false is the loop's signal that the context is gone.
func TestRun_returnsWhenAPassIsCancelledMidBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := newFakeStore([]videos.ProbeCandidate{
		{ID: "v1", MediaPath: "/m/v1.mp4"},
		{ID: "v2", MediaPath: "/m/v2.mp4"},
	})
	// Cancelling as the batch is handed over means pass() probes nothing and
	// returns false on its first candidate check.
	testee := quietWorker(Deps{
		Prober:       &fakeProber{byPath: map[string]Info{"/m/v1.mp4": {Container: "mp4"}}},
		Videos:       &cancelOnListStore{fakeStore: store, cancel: cancel},
		PollInterval: time.Hour,
	})

	done := make(chan struct{})
	go func() {
		testee.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after a cancelled pass")
	}
	if _, ok := store.wrote("v1"); ok {
		t.Error("Run worked through the batch despite cancellation")
	}
}

// With nothing to do, Run parks in sched.Sleep. Cancellation there must end
// the loop rather than wait out a poll interval that may be minutes long.
func TestRun_returnsFromTheIdleSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// An empty batch, so pass() succeeds and Run reaches the sleep with the
	// context already cancelled.
	testee := quietWorker(Deps{
		Prober:       &fakeProber{},
		Videos:       &cancelOnListStore{fakeStore: newFakeStore(), cancel: cancel},
		PollInterval: time.Hour,
	})

	done := make(chan struct{})
	go func() {
		testee.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run sat out the poll interval instead of returning")
	}
}

func TestStoreResult_carriesEveryField(t *testing.T) {
	got := StoreResult(Info{Container: "mkv", VideoCodec: "av01", VideoHeight: 2160, AudioCodec: "opus"})

	want := videos.ProbeResult{Container: "mkv", VideoCodec: "av01", VideoHeight: 2160, AudioCodec: "opus"}
	if got != want {
		t.Errorf("StoreResult = %+v, want %+v", got, want)
	}
}
