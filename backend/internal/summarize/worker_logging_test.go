package summarize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/videos"
)

// logBuf collects slog JSON records. The worker logs only from its own
// goroutine here, but the mutex keeps it safe if that ever changes.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) records(t *testing.T) []map[string]any {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(l.b.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

// findRec returns the first record with this msg, or nil.
func findRec(recs []map[string]any, msg string) map[string]any {
	for _, r := range recs {
		if r["msg"] == msg {
			return r
		}
	}
	return nil
}

// findStep returns the "step done" record for one step, or nil.
func findStep(recs []map[string]any, step string) map[string]any {
	for _, r := range recs {
		if r["msg"] == "summarize worker: step done" && r["step"] == step {
			return r
		}
	}
	return nil
}

func captureLogger() (*slog.Logger, *logBuf) {
	buf := &logBuf{}
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})), buf
}

// usageCompleter answers like fakeWorkerCompleter but also books token usage
// against whatever Totals the worker attached to the context — standing in for
// the real llm.Client, and thereby proving the worker's per-video accounting
// and per-step context both reach the Completer.
type usageCompleter struct {
	mu    sync.Mutex
	steps []string
}

func (u *usageCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	info := llm.CallFrom(ctx)
	u.mu.Lock()
	u.steps = append(u.steps, info.Step)
	u.mu.Unlock()
	info.Totals.Add(llm.Usage{Requests: 1, PromptTokens: 1000, CompletionTokens: 200, ReasoningTokens: 120, TotalTokens: 1200})

	sys := m[0].Content
	switch {
	case strings.Contains(sys, "Combine these section summaries"):
		return "Overall prose summary.", nil
	case strings.Contains(sys, "category id"):
		return "ai", nil
	case strings.Contains(sys, "JSON"):
		return `{"key_points":[{"ts":0,"text":"a point"}]}`, nil
	}
	return "chunk summary", nil
}

func (u *usageCompleter) sawStep(step string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, s := range u.steps {
		if s == step {
			return true
		}
	}
	return false
}

// keypointsErrCompleter succeeds up to the fragile last call, then fails it —
// the shape that makes the queue retry a job.
type keypointsErrCompleter struct{}

func (keypointsErrCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	sys := m[0].Content
	switch {
	case strings.Contains(sys, "Combine these section summaries"):
		return "Overall prose summary.", nil
	case strings.Contains(sys, "category id"):
		return "ai", nil
	case strings.Contains(sys, "JSON"):
		return "", errors.New("keypoints boom")
	}
	return "chunk summary", nil
}

// seedVideo writes a subtitle file, upserts a titled video with a channel, and
// enqueues its summary job.
func seedVideo(t *testing.T, h *workerHarness, id string) {
	t.Helper()
	relPath := id + "/captions.en.vtt"
	full := filepath.Join(h.mediaDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vtt := "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nHello there, welcome to the video.\n\n" +
		"00:00:02.000 --> 00:00:04.000\nToday we will talk about testing Go workers.\n"
	if err := os.WriteFile(full, []byte(vtt), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}
	if err := h.videos.Upsert(videos.Video{
		ID: id, URL: "https://youtu.be/" + id, Title: "A Test Video", ChannelName: "A Test Channel",
	}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	if err := h.videos.SetSubtitle(id, relPath, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}
	if _, err := h.jobs.Enqueue(id); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
}

func TestWorkerLogsStartStepsAndTotals(t *testing.T) {
	h := newWorkerHarness(t)
	seedVideo(t, h, "v1")
	log, buf := captureLogger()
	comp := &usageCompleter{}

	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(comp), Embedder: fakeWorkerEmbedder{dim: 1536},
		MediaDir: h.mediaDir, EmbedModel: "test-model", EmbedDim: 1536, Logger: log,
	})
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}

	recs := buf.records(t)

	start := findRec(recs, "summarize worker: analysis started")
	if start == nil {
		t.Fatal("no 'analysis started' record")
	}
	for k, want := range map[string]any{
		"video_id": "v1", "title": "A Test Video", "channel": "A Test Channel",
		"attempt": float64(1), "max_attempts": float64(3), "resumed": false,
	} {
		if start[k] != want {
			t.Errorf("started.%s = %v, want %v", k, start[k], want)
		}
	}

	for _, step := range []string{"summary", "classify", "embedding", "keypoints"} {
		rec := findStep(recs, step)
		if rec == nil {
			t.Fatalf("no 'step done' record for %q", step)
		}
		if _, ok := rec["duration_ms"]; !ok {
			t.Errorf("step %q has no duration_ms: %v", step, rec)
		}
		if rec["title"] != "A Test Video" || rec["channel"] != "A Test Channel" {
			t.Errorf("step %q missing identity: %v", step, rec)
		}
	}
	// The classify and keypoints steps each cost exactly one call, so their
	// per-step token delta must be that call's usage, not the running total.
	if got := findStep(recs, "classify")["chat_tokens_in"]; got != "1.0k" {
		t.Errorf("classify chat_tokens_in = %v, want 1.0k", got)
	}
	if got := findStep(recs, "classify")["category"]; got != "ai" {
		t.Errorf("classify category = %v, want ai", got)
	}
	// The embedding step spends no chat tokens — its cost is logged by the
	// embedding client instead.
	if _, ok := findStep(recs, "embedding")["chat_tokens_in"]; ok {
		t.Errorf("embedding step reported chat tokens: %v", findStep(recs, "embedding"))
	}

	fin := findRec(recs, "summarize worker: analysis finished")
	if fin == nil {
		t.Fatal("no 'analysis finished' record")
	}
	if fin["outcome"] != "done" || fin["video_id"] != "v1" || fin["title"] != "A Test Video" {
		t.Errorf("finished record = %v", fin)
	}
	if _, ok := fin["duration_ms"]; !ok {
		t.Errorf("finished record has no duration_ms: %v", fin)
	}
	// 3 chat calls (map, reduce, classify, keypoints => at least 4 here; the
	// map count follows chunking), each 1000 in / 200 out.
	if fin["chat_tokens_in"] == nil || fin["chat_tokens_out"] == nil || fin["chat_tokens_reasoning"] == nil {
		t.Errorf("finished record missing token totals: %v", fin)
	}
	if !comp.sawStep("summary") || !comp.sawStep("classify") || !comp.sawStep("keypoints") {
		t.Errorf("completer never saw per-step context: %v", comp.steps)
	}
}

func TestWorkerLogsSkippedStepsOnResumedJob(t *testing.T) {
	h := newWorkerHarness(t)
	seedVideo(t, h, "v2")
	// A job resumed after the key-points step failed: summary, category and
	// embeddings are already stored, so only key points re-runs.
	if err := h.videos.SetSummaryText("v2", "Existing summary."); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	if err := h.videos.SetCategory("v2", "ai"); err != nil {
		t.Fatalf("set category: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE videos SET embed_model = 'test-model', summary_status = 'done' WHERE id = 'v2'`); err != nil {
		t.Fatalf("mark embedded: %v", err)
	}

	log, buf := captureLogger()
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(&usageCompleter{}), Embedder: failEmbedder{t: t},
		MediaDir: h.mediaDir, EmbedModel: "test-model", EmbedDim: 1536, Logger: log,
	})
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}

	recs := buf.records(t)
	if start := findRec(recs, "summarize worker: analysis started"); start["resumed"] != true {
		t.Errorf("resumed = %v, want true", start["resumed"])
	}
	var skipped []string
	for _, r := range recs {
		if r["msg"] == "summarize worker: step skipped" {
			skipped = append(skipped, r["step"].(string))
		}
	}
	if len(skipped) != 3 {
		t.Fatalf("skipped steps = %v, want summary/classify/embedding", skipped)
	}
	if findStep(recs, "keypoints") == nil {
		t.Error("key-points step did not run on the resumed job")
	}
}

func TestWorkerLogsRetryAttemptWhenKeyPointsFail(t *testing.T) {
	h := newWorkerHarness(t)
	seedVideo(t, h, "v3")
	log, buf := captureLogger()
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(keypointsErrCompleter{}), Embedder: fakeWorkerEmbedder{dim: 1536},
		MediaDir: h.mediaDir, EmbedModel: "test-model", EmbedDim: 1536, Logger: log,
	})
	if _, err := w.processOne(context.Background()); err == nil {
		t.Fatal("expected processOne to surface the key-points failure")
	}

	recs := buf.records(t)
	rec := findRec(recs, "summarize worker: key-points step failed, will retry")
	if rec == nil {
		t.Fatal("no retry record")
	}
	for k, want := range map[string]any{
		"attempt": float64(1), "max_attempts": float64(3), "last": false,
		"title": "A Test Video", "channel": "A Test Channel",
	} {
		if rec[k] != want {
			t.Errorf("retry.%s = %v, want %v", k, rec[k], want)
		}
	}
	if fin := findRec(recs, "summarize worker: analysis finished"); fin == nil || fin["outcome"] != "keypoints_failed" {
		t.Errorf("finished record = %v", fin)
	}
}

func TestWorkerLogsEmbedFailureAsErrorOutcome(t *testing.T) {
	h := newWorkerHarness(t)
	seedVideo(t, h, "v4")
	log, buf := captureLogger()
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(&usageCompleter{}), Embedder: failingEmbedder{},
		MediaDir: h.mediaDir, EmbedModel: "test-model", EmbedDim: 1536, Logger: log,
	})
	if _, err := w.processOne(context.Background()); err == nil {
		t.Fatal("expected processOne to surface the embed failure")
	}
	fin := findRec(buf.records(t), "summarize worker: analysis finished")
	if fin == nil || fin["outcome"] != "error" {
		t.Fatalf("finished record = %v", fin)
	}
}

func TestWorkerLogsNoTranscriptReason(t *testing.T) {
	h := newWorkerHarness(t)
	if err := h.videos.Upsert(videos.Video{ID: "v5", URL: "https://youtu.be/v5", Title: "No Subs", ChannelName: "A Test Channel"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := h.jobs.Enqueue("v5"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	log, buf := captureLogger()
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(failCompleter{t: t}), Embedder: failEmbedder{t: t},
		MediaDir: h.mediaDir, EmbedModel: "test-model", EmbedDim: 4, Logger: log,
	})
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}
	rec := findRec(buf.records(t), "summarize worker: no transcript")
	if rec == nil {
		t.Fatal("no 'no transcript' record")
	}
	if rec["reason"] != "no subtitle file" || rec["title"] != "No Subs" {
		t.Errorf("no-transcript record = %v", rec)
	}
}

func TestWorkerLogsBacklogClassifyWithIdentityAndTokens(t *testing.T) {
	h := newWorkerHarness(t)
	// A downloaded, summarized, still-uncategorized video and no queued job:
	// the idle sweep path.
	if err := h.videos.Upsert(videos.Video{ID: "v6", URL: "https://youtu.be/v6", Title: "Backlog Video", ChannelName: "A Test Channel"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE videos SET status = 'downloaded', summary = 'Existing summary.', summary_status = 'done' WHERE id = 'v6'`); err != nil {
		t.Fatalf("prepare backlog video: %v", err)
	}

	log, buf := captureLogger()
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(&usageCompleter{}), Embedder: failEmbedder{t: t},
		MediaDir: h.mediaDir, EmbedModel: "test-model", EmbedDim: 1536, Logger: log,
	})
	did, err := w.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if !did {
		t.Fatal("expected the idle sweep to classify the backlog video")
	}
	rec := findRec(buf.records(t), "summarize worker: classified backlog video")
	if rec == nil {
		t.Fatal("no backlog classify record")
	}
	if rec["title"] != "Backlog Video" || rec["channel"] != "A Test Channel" || rec["category"] != "ai" {
		t.Errorf("backlog record = %v", rec)
	}
	if rec["chat_tokens_in"] != "1.0k" {
		t.Errorf("backlog chat_tokens_in = %v, want 1.0k", rec["chat_tokens_in"])
	}
	if _, ok := rec["duration_ms"]; !ok {
		t.Errorf("backlog record has no duration_ms: %v", rec)
	}
}
