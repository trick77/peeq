package summarize

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/llm"
)

// unparsableKeyPointsCompleter answers the summary normally and the key-points
// call with prose instead of JSON.
type unparsableKeyPointsCompleter struct{}

func (unparsableKeyPointsCompleter) Complete(_ context.Context, m []llm.Message) (string, error) {
	if len(m) > 0 && strings.Contains(m[0].Content, "JSON") {
		return "Sure! Here are the key points in prose.", nil
	}
	if len(m) > 0 && strings.Contains(m[0].Content, "category id") {
		return "ai", nil
	}
	return "Overall prose summary.", nil
}

// TestKeyPointsUnparsableReply_isLoggedWithTheVideoAndDoesNotFailTheJob: an
// endpoint that ignores response_format costs the video its chapters and key
// points, not its summary. The drop is logged by the worker with the video's
// identity, which the Summarizer alone could not supply.
func TestKeyPointsUnparsableReply_isLoggedWithTheVideoAndDoesNotFailTheJob(t *testing.T) {
	h := newWorkerHarness(t)
	seedVideo(t, h, "v1")
	log, buf := captureLogger()
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(unparsableKeyPointsCompleter{}), Embedder: fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536, Logger: log})
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v — a non-JSON key-points reply must degrade, not fail", err)
	}
	var state string
	if err := h.db.QueryRow(`SELECT state FROM summary_jobs WHERE video_id = 'v1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "done" {
		t.Fatalf("job state = %q, want done", state)
	}
	rec := findRec(buf.records(t), "summarize worker: key points reply was not JSON; storing none")
	if rec == nil {
		t.Fatal("the drop was not logged")
	}
	if rec["video_id"] != "v1" {
		t.Fatalf("log line lacks the video: %v", rec)
	}
	if rec["level"] != "WARN" {
		t.Fatalf("level = %v, want WARN", rec["level"])
	}
}
