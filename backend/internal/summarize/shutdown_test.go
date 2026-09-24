package summarize

import (
	"context"
	"testing"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/videos"
)

// cancellingCompleter is a chat model call interrupted by process shutdown: it
// cancels the worker's context from inside the call and returns ctx.Err().
type cancellingCompleter struct{ cancel context.CancelFunc }

func (c cancellingCompleter) Complete(ctx context.Context, _ []llm.Message) (string, error) {
	c.cancel()
	return "", ctx.Err()
}

// TestProcessOneShutdownMidSummaryLeavesJobRunning: a shutdown that lands in
// the middle of an analysis is not that video's summary failing. Nothing
// terminal is written — no summary_status=error the card would show, no burned
// attempt with its 15-minute/4-hour backoff, no Activity row — and the job is
// left 'running' for the boot-time orphan sweep to reclaim.
func TestProcessOneShutdownMidSummaryLeavesJobRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, w, rec := seedFailingVideoHarness(t, "v1", 1, cancellingCompleter{cancel: cancel})

	if _, err := w.processOne(ctx); err == nil {
		t.Fatal("processOne must report the interrupted call")
	}

	var state string
	var next *string
	if err := h.db.QueryRow(`SELECT state, next_attempt_at FROM summary_jobs WHERE video_id = 'v1'`).Scan(&state, &next); err != nil {
		t.Fatal(err)
	}
	if state != "running" {
		t.Fatalf("job state = %q, want running (left for the orphan sweep)", state)
	}
	if next != nil {
		t.Fatalf("next_attempt_at = %q; a shutdown must not schedule a backoff", *next)
	}
	v, err := h.videos.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if v.SummaryStatus == videos.SummaryError {
		t.Fatalf("summary_status = error (%q); a shutdown is not a failure", v.SummaryError)
	}
	if n := len(rec.events); n != 0 {
		t.Fatalf("%d activity rows written for a shutdown", n)
	}
}
