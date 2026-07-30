package summarize

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// countingCompleter answers every prompt and counts the round trips. Counting
// is the point: these tests are about how many LLM calls a video costs, not
// about what the model said.
type countingCompleter struct {
	reply string
	calls int
}

func (c *countingCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	c.calls++
	// The classify step wants a bare category id, and NormalizeCategory would
	// turn prose into "uncategorized" — which is also what a skipped classify
	// leaves behind. Answering it properly keeps the two distinguishable, so a
	// test asserting "no classify happened" is asserting something.
	if strings.Contains(m[0].Content, "category id") {
		return "ai", nil
	}
	return c.reply, nil
}

// writeVTT puts a minimal speech transcript at full. It has to read as speech:
// the worker routes music-only captions to no_transcript before any of the
// behaviour under test here can happen.
func writeVTT(t *testing.T, full string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vtt := "WEBVTT\n\n" +
		"00:00:00.000 --> 00:00:02.000\nHello there, welcome to the video.\n\n" +
		"00:00:02.000 --> 00:00:04.000\nToday we will talk about testing Go workers.\n"
	if err := os.WriteFile(full, []byte(vtt), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}
}

// writeInboxCaption puts a .vtt where the caption fetcher would have put it —
// under MediaDir/.summaries/<id>/ — and returns the MediaDir-relative path.
// Where the file lives is not incidental to these tests: it is half of what
// tells the worker this transcript was read to inform a decision rather than
// obtained by downloading.
func writeInboxCaption(t *testing.T, h *workerHarness, id string) string {
	t.Helper()
	rel := filepath.Join(ytdlp.SummaryDirName, id, id+".en.vtt")
	writeVTT(t, filepath.Join(h.mediaDir, rel))
	return rel
}

// TestInboxVideoStopsAfterTheSummary is the cost promise. A video peeq read to
// help decide whether to download it gets the prose and nothing else: no
// classify call, no embeddings, no key points. Every one of those is an
// investment in a video the library keeps, and this one may still be ignored.
func TestInboxVideoStopsAfterTheSummary(t *testing.T) {
	h := newWorkerHarness(t)
	rel := writeInboxCaption(t, h, "inbox1")

	if err := h.videos.Upsert(videos.Video{ID: "inbox1", URL: "https://youtu.be/inbox1"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := h.videos.SetSubtitle("inbox1", rel, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}
	if err := h.videos.SetStatus("inbox1", videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, err := h.jobs.Enqueue("inbox1"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// countingEmbedder would record any embedding call; failCompleter-style
	// counting on the completer tells us how many LLM round trips happened.
	completer := &countingCompleter{reply: "A summary of the video."}
	embedder := &countingEmbedder{dim: 1536}
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(completer), Embedder: embedder,
		MediaDir: h.mediaDir, EmbedModel: "test-model", EmbedDim: 1536,
	})
	if _, err := w.processOne(t.Context()); err != nil {
		t.Fatalf("processOne: %v", err)
	}

	v, err := h.videos.Get("inbox1")
	if err != nil || v == nil {
		t.Fatalf("get: %v", err)
	}
	if v.Summary == "" {
		t.Fatal("expected the prose summary to be written")
	}
	if v.SummaryStatus != videos.SummaryDone {
		t.Fatalf("summary_status = %q, want done", v.SummaryStatus)
	}
	if v.Category != videos.UncategorizedCategory {
		t.Fatalf("category = %q, want it left uncategorized (no classify call)", v.Category)
	}
	if v.EmbedModel != "" {
		t.Fatalf("embed_model = %q, want empty (no embedding)", v.EmbedModel)
	}
	// The column defaults to an empty JSON array, so "" and "[]" both mean
	// "nothing was written"; anything else means the deferred step ran.
	if v.KeyPoints != "" && v.KeyPoints != "[]" {
		t.Fatalf("key_points = %q, want empty (no key-points call)", v.KeyPoints)
	}
	if embedder.calls != 0 {
		t.Fatalf("embedder called %d times, want 0", embedder.calls)
	}
	// One call: the summary. Anything more means a deferred step ran anyway.
	if completer.calls != 1 {
		t.Fatalf("completer called %d times, want exactly 1 (the summary)", completer.calls)
	}
}

// TestDownloadingAnInboxVideoSkipsTheSummaryAndRunsTheRest is the other half of
// the promise, and the reason the feature is worth building: deciding to
// download must not pay for the expensive call a second time.
//
// It reproduces the real handover — the download repoints subtitle_path out of
// .summaries/ and into the media directory, and a fresh summary job is queued —
// then asserts the second pass spends nothing on prose and everything on the
// three steps the inbox pass deferred.
func TestDownloadingAnInboxVideoSkipsTheSummaryAndRunsTheRest(t *testing.T) {
	h := newWorkerHarness(t)
	inboxRel := writeInboxCaption(t, h, "inbox2")

	if err := h.videos.Upsert(videos.Video{ID: "inbox2", URL: "https://youtu.be/inbox2"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := h.videos.SetSubtitle("inbox2", inboxRel, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}
	if err := h.videos.SetStatus("inbox2", videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, err := h.jobs.Enqueue("inbox2"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	completer := &countingCompleter{reply: "A summary of the video."}
	embedder := &countingEmbedder{dim: 1536}
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(completer), Embedder: embedder,
		MediaDir: h.mediaDir, EmbedModel: "test-model", EmbedDim: 1536,
	})
	if _, err := w.processOne(t.Context()); err != nil {
		t.Fatalf("inbox pass: %v", err)
	}
	afterInbox := completer.calls

	// The download: captions now live with the media, and the status says so.
	downloadedRel := filepath.Join("UC123", "inbox2", "inbox2.en.vtt")
	writeVTT(t, filepath.Join(h.mediaDir, downloadedRel))
	if err := h.videos.SetSubtitle("inbox2", downloadedRel, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}
	if err := h.videos.SetStatus("inbox2", videos.StatusDownloaded, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, err := h.jobs.Enqueue("inbox2"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	completer.reply = `{"key_points":[{"ts":0,"text":"a point"}]}`
	if _, err := w.processOne(t.Context()); err != nil {
		t.Fatalf("post-download pass: %v", err)
	}

	v, err := h.videos.Get("inbox2")
	if err != nil || v == nil {
		t.Fatalf("get: %v", err)
	}
	if v.Summary != "A summary of the video." {
		t.Fatalf("summary = %q, want the one written from the inbox read", v.Summary)
	}
	if v.EmbedModel == "" {
		t.Fatal("expected the deferred embedding step to have run after download")
	}
	if v.KeyPoints == "" || v.KeyPoints == "[]" {
		t.Fatalf("key_points = %q, want the deferred step to have run after download", v.KeyPoints)
	}
	// Two calls in the second pass — classify and key points — and NOT a third
	// for the summary. That difference is the whole feature.
	if got := completer.calls - afterInbox; got != 2 {
		t.Fatalf("second pass made %d LLM calls, want 2 (classify + key points, no summary)", got)
	}
}

// TestOrdinaryNewVideoIsNotTreatedAsAnInboxRead guards the trap that 'new' is
// the videos.status column DEFAULT, and the state a CANCELLED download is
// returned to. A row that merely happens to be 'new' — with a transcript from a
// real download — must get the full pipeline, or cancelling a download would
// quietly cost that video its category, embeddings and key points forever.
func TestOrdinaryNewVideoIsNotTreatedAsAnInboxRead(t *testing.T) {
	h := newWorkerHarness(t)
	rel := filepath.Join("UC123", "v-cancelled", "v-cancelled.en.vtt")
	writeVTT(t, filepath.Join(h.mediaDir, rel))

	if err := h.videos.Upsert(videos.Video{ID: "v-cancelled", URL: "https://youtu.be/v-cancelled"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := h.videos.SetSubtitle("v-cancelled", rel, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}
	// Exactly what download.Worker.settleCanceled leaves behind.
	if err := h.videos.SetStatus("v-cancelled", videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, err := h.jobs.Enqueue("v-cancelled"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	completer := &countingCompleter{reply: `{"key_points":[{"ts":0,"text":"a point"}]}`}
	embedder := &countingEmbedder{dim: 1536}
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(completer), Embedder: embedder,
		MediaDir: h.mediaDir, EmbedModel: "test-model", EmbedDim: 1536,
	})
	if _, err := w.processOne(t.Context()); err != nil {
		t.Fatalf("processOne: %v", err)
	}

	v, err := h.videos.Get("v-cancelled")
	if err != nil || v == nil {
		t.Fatalf("get: %v", err)
	}
	if v.EmbedModel == "" {
		t.Fatal("a 'new' video whose transcript came from a download must still be embedded")
	}
	if v.KeyPoints == "" || v.KeyPoints == "[]" {
		t.Fatalf("key_points = %q; a 'new' video whose transcript came from a download must still get them", v.KeyPoints)
	}
}

// TestNextUnclassifiedSkipsInboxVideos pins the other place the deferred
// classify call could sneak back in. The idle sweep asks only for "has a
// summary and is uncategorized", which an inbox video satisfies exactly — so
// without its status guard the sweep would spend a classify call on every video
// in the Inbox, and spend it again on every one ever declined after any
// category-reset migration.
func TestNextUnclassifiedSkipsInboxVideos(t *testing.T) {
	h := newWorkerHarness(t)

	if err := h.videos.Upsert(videos.Video{ID: "inbox3", URL: "https://youtu.be/inbox3", Title: "An inbox video"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := h.videos.SetSummaryText("inbox3", "A summary."); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	if err := h.videos.SetStatus("inbox3", videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}

	got, err := h.videos.NextUnclassified(nil)
	if err != nil {
		t.Fatalf("next unclassified: %v", err)
	}
	if got != nil {
		t.Fatalf("sweep offered %q; inbox videos must be left uncategorized", got.ID)
	}

	// The same row, once downloaded, IS the sweep's business.
	if err := h.videos.SetStatus("inbox3", videos.StatusDownloaded, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	got, err = h.videos.NextUnclassified(nil)
	if err != nil {
		t.Fatalf("next unclassified: %v", err)
	}
	if got == nil || got.ID != "inbox3" {
		t.Fatal("a downloaded video with a summary and no category must still be swept")
	}
}
