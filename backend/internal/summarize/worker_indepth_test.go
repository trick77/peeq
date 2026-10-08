package summarize

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/rag"
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

// inDepthChunks lists the video's indexed in-depth sections as "start text".
func inDepthChunks(t *testing.T, h *workerHarness, id string) []string {
	t.Helper()
	rows, err := h.db.Query(`SELECT start_seconds, text FROM transcript_chunks WHERE video_id = ? AND kind = ? ORDER BY ordinal`, id, rag.KindInDepth)
	if err != nil {
		t.Fatalf("query chunks: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var start int
		var text string
		if err := rows.Scan(&start, &text); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, fmt.Sprintf("%d %s", start, text))
	}
	return out
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
	// Its sections are what Ask searches, so the same job indexes them.
	if got := inDepthChunks(t, h, "d1"); len(got) != 1 || got[0] != "0 A point\n\nBody." {
		t.Fatalf("indepth chunks = %q, want the one section at its stamp", got)
	}
}

// The in-depth text is an extra, so its failure must not cost the video its
// core analysis: the job carries on to key points and the index and finishes
// done, with the card simply absent.
func TestWorker_inDepthFailureIsBestEffort(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d2")
	c := &inDepthCompleter{failInDepth: 99}

	if _, err := newInDepthWorker(h, c).processOne(t.Context()); err != nil {
		t.Fatalf("processOne: %v, want the job to finish", err)
	}
	v, err := h.videos.Get("d2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.SummaryStatus != videos.SummaryDone || v.KeyPoints == "" || v.KeyPoints == "[]" || !v.Indexed() {
		t.Fatalf("status=%q key_points=%q indexed=%v, want the core analysis complete", v.SummaryStatus, v.KeyPoints, v.Indexed())
	}
	wantInDepth(t, h, "d2", "")
	if got := inDepthChunks(t, h, "d2"); len(got) != 0 {
		t.Fatalf("indepth chunks = %q, want none without an in-depth text", got)
	}
	var state string
	if err := h.db.QueryRow(`SELECT state FROM summary_jobs WHERE video_id = ?`, "d2").Scan(&state); err != nil || state != "done" {
		t.Fatalf("job state = %q, %v; want done", state, err)
	}
}

// Same on the inbox path: a channel that keeps its reads still gets them
// indexed when the in-depth call fails.
func TestWorker_inboxInDepthFailureStillIndexesAKeptRead(t *testing.T) {
	h := newWorkerHarness(t)
	rel := writeInboxCaption(t, h, "i2")
	if err := h.videos.Upsert(videos.Video{ID: "i2", URL: "https://youtu.be/i2"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedTranscript(t, h, "i2", rel)
	if err := h.videos.SetStatus("i2", videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	seedChannel(t, h, "UCkeep", "i2", true)
	if _, err := h.jobs.Enqueue("i2"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := newInDepthWorker(h, &inDepthCompleter{failInDepth: 99}).processOne(t.Context()); err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if indexed, err := h.rag.HasChunks(t.Context(), "i2"); err != nil || !indexed {
		t.Fatalf("indexed=%v (err %v), want the kept read indexed", indexed, err)
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
