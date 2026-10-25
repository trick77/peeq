package summarize

// BACKFILL(in-depth): one-off fill of videos analysed before 0039. Remove
// (backfill_indepth.go, this file, videos/store_indepth_backfill*.go and the
// call in Worker.Run) once in_depth_retry has drained to 0.

import (
	"testing"
	"time"
)

// A video analysed before the in-depth step existed has no text and no retry
// row. The boot backfill queues it, and the retry path writes and indexes it.
func TestBackfillInDepth_queuesAVideoAnalysedBeforeTheStep(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "old")
	c := &inDepthCompleter{failInDepth: 1}
	clock := newRetryClock()
	w := newRetryWorker(h, c, clock, retryWorkerOpts{})
	mustProcess(t, w)
	// What a pre-0039 video looks like: analysed, indexed, nothing queued.
	if err := h.videos.ClearInDepthRetry("old"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if mustProcess(t, w) {
		t.Fatal("did work before the backfill")
	}

	w.backfillInDepth()
	w.backfillInDepth() // every boot runs it; the second finds it queued
	if !mustProcess(t, w) {
		t.Fatal("backfilled video not due at once")
	}
	wantInDepth(t, h, "old", inDepthReply)
	if got := inDepthChunks(t, h, "old"); len(got) != 1 {
		t.Fatalf("indepth chunks = %q, want the section indexed", got)
	}
	clock.advance(48 * time.Hour)
	if mustProcess(t, w) {
		t.Fatal("did work after the backfill drained")
	}
}

func TestBackfillInDepth_aStoreErrorIsLoggedNotFatal(t *testing.T) {
	h := newWorkerHarness(t)
	w := newRetryWorker(h, &inDepthCompleter{}, newRetryClock(), retryWorkerOpts{})
	if _, err := h.db.Exec(`DROP TABLE in_depth_retry`); err != nil {
		t.Fatalf("break: %v", err)
	}
	w.backfillInDepth()
}
