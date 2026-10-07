package summarize

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/videos"
)

const inDepthReply = "Lead.\n\n### A point [0:00]\n\nBody."

// inDepthCompleter answers every step, counts each, and fails the in-depth
// call as many times as failInDepth says before answering it.
type inDepthCompleter struct {
	failInDepth int
	calls       map[string]int
}

func (c *inDepthCompleter) Complete(_ context.Context, m []llm.Message) (string, error) {
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	sys := promptText(m)
	switch {
	case strings.Contains(sys, "cohesive summary"):
		c.calls["summary"]++
		return "Overall prose summary.", nil
	case strings.Contains(sys, "category id"):
		c.calls["classify"]++
		return "ai", nil
	case strings.Contains(sys, "JSON"):
		c.calls["keypoints"]++
		return `{"key_points":[{"ts":0,"text":"a point"}]}`, nil
	case strings.Contains(sys, "in-depth summary"):
		c.calls["indepth"]++
		if c.calls["indepth"] <= c.failInDepth {
			return "", errors.New("in-depth timeout")
		}
		return inDepthReply, nil
	}
	return "", errors.New("unexpected prompt")
}

func newInDepthWorker(h *workerHarness, c *inDepthCompleter) *Worker {
	return NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(c), Embedder: &countingEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536})
}

func seedDownloaded(t *testing.T, h *workerHarness, id string) {
	t.Helper()
	rel := filepath.Join(id, "captions.en.vtt")
	writeVTT(t, filepath.Join(h.mediaDir, rel))
	if err := h.videos.Upsert(videos.Video{ID: id, URL: "https://youtu.be/" + id}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedTranscript(t, h, id, rel)
	if _, err := h.jobs.Enqueue(id); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
}

func wantInDepth(t *testing.T, h *workerHarness, id, want string) {
	t.Helper()
	got, err := h.videos.InDepth(id)
	if err != nil {
		t.Fatalf("read in-depth: %v", err)
	}
	if got != want {
		t.Fatalf("in-depth = %q, want %q", got, want)
	}
}

func TestWorker_writesTheInDepthSummary(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	c := &inDepthCompleter{}

	if _, err := newInDepthWorker(h, c).processOne(t.Context()); err != nil {
		t.Fatalf("processOne: %v", err)
	}
	wantInDepth(t, h, "d1", inDepthReply)
	if c.calls["indepth"] != 1 || c.calls["keypoints"] != 1 {
		t.Fatalf("calls = %v, want one in-depth and one key-points call", c.calls)
	}
}

// A failed in-depth call requeues the job without touching the summary, which
// stays done and readable. The retry spends nothing on the summary again.
func TestWorker_inDepthFailureKeepsTheSummaryAndRetriesTheStep(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d2")
	c := &inDepthCompleter{failInDepth: 1}
	w := newInDepthWorker(h, c)

	if _, err := w.processOne(t.Context()); err == nil {
		t.Fatal("want the in-depth failure surfaced")
	}
	v, err := h.videos.Get("d2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.Summary == "" || v.SummaryStatus != videos.SummaryDone {
		t.Fatalf("summary=%q status=%q, want the summary kept and done", v.Summary, v.SummaryStatus)
	}
	wantInDepth(t, h, "d2", "")

	if _, err := w.processOne(t.Context()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	wantInDepth(t, h, "d2", inDepthReply)
	if c.calls["summary"] != 1 {
		t.Fatalf("summary calls = %d, want 1 (the retry must skip it)", c.calls["summary"])
	}
}

// Inbox reads get it too: the Inbox page offers it to settle a maybe. Once the
// video is downloaded, the second pass finds it stored and does not pay again.
func TestWorker_inboxReadGetsTheInDepthSummaryOnce(t *testing.T) {
	h := newWorkerHarness(t)
	rel := writeInboxCaption(t, h, "i1")
	if err := h.videos.Upsert(videos.Video{ID: "i1", URL: "https://youtu.be/i1"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedTranscript(t, h, "i1", rel)
	if err := h.videos.SetStatus("i1", videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, err := h.jobs.Enqueue("i1"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	c := &inDepthCompleter{}
	w := newInDepthWorker(h, c)
	if _, err := w.processOne(t.Context()); err != nil {
		t.Fatalf("inbox pass: %v", err)
	}
	wantInDepth(t, h, "i1", inDepthReply)
	if c.calls["keypoints"] != 0 || c.calls["classify"] != 0 {
		t.Fatalf("calls = %v, want key points and classify still deferred", c.calls)
	}

	// The download handover: the transcript becomes the downloaded one and a
	// fresh job is queued.
	downRel := filepath.Join("i1", "captions.en.vtt")
	writeVTT(t, filepath.Join(h.mediaDir, downRel))
	seedTranscript(t, h, "i1", downRel)
	if err := h.videos.SetStatus("i1", videos.StatusDownloaded, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, err := h.jobs.Enqueue("i1"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := w.processOne(t.Context()); err != nil {
		t.Fatalf("download pass: %v", err)
	}
	if c.calls["indepth"] != 1 || c.calls["summary"] != 1 {
		t.Fatalf("calls = %v, want the summary and the in-depth text paid for once", c.calls)
	}
}
