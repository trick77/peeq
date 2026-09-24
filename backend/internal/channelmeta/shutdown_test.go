package channelmeta

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/activity"
)

// fakeRecorder collects Activity rows the worker would have written.
type fakeRecorder struct {
	mu  sync.Mutex
	evs []activity.Event
}

func (r *fakeRecorder) Record(e activity.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, e)
}

func (r *fakeRecorder) events() []activity.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]activity.Event(nil), r.evs...)
}

// TestWorker_shutdownMidRefreshDoesNotReschedule: a process shutdown that lands
// during a metadata refresh is not that channel failing. It must not be pushed
// a week out, not have a resolve attempt recorded against it, and not produce
// a warning Activity row. The next boot claims it again.
func TestWorker_shutdownMidRefreshDoesNotReschedule(t *testing.T) {
	s := newTestStore(t)
	seedDue(t, s, "UC1")
	before, err := s.Get("UC1")
	if err != nil || before == nil {
		t.Fatalf("seed: %v", err)
	}
	scheduledBefore := nextMetaRefreshAt(t, s, "UC1")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &fakeResolver{onCall: func(context.Context) { cancel() }}
	rec := &fakeRecorder{}
	w := newTestWorker(t, s, r, Deps{Activity: rec})
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after ctx cancel")
	}
	if len(r.calls()) != 1 {
		t.Fatalf("resolver calls = %d, want 1", len(r.calls()))
	}

	if got := nextMetaRefreshAt(t, s, "UC1"); got != scheduledBefore {
		t.Fatalf("next_meta_refresh_at moved %q -> %q on shutdown", scheduledBefore, got)
	}
	after, err := s.Get("UC1")
	if err != nil {
		t.Fatal(err)
	}
	if after.ResolvedAt != before.ResolvedAt {
		t.Fatalf("resolved_at moved %q -> %q; a shutdown is not a resolve attempt", before.ResolvedAt, after.ResolvedAt)
	}
	if n := len(rec.events()); n != 0 {
		t.Fatalf("%d activity rows written for a shutdown", n)
	}
}
