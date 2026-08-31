package summarize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/summaryjobs"
	"time"

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

// findStep returns the stage-done record for one step, or nil. The message
// carries the stage ("stage 2/4 done"), so match on its shape plus the step.
func findStep(recs []map[string]any, step string) map[string]any {
	return findStageRec(recs, step, "done")
}

// findStageRec finds a stage line for one step by its trailing verb
// ("started", "done", "skipped").
func findStageRec(recs []map[string]any, step, verb string) map[string]any {
	for _, r := range recs {
		msg, _ := r["msg"].(string)
		if strings.HasPrefix(msg, "summarize worker: stage ") && strings.HasSuffix(msg, " "+verb) && r["step"] == step {
			return r
		}
	}
	return nil
}

// recStage pulls "2/4" out of a "summarize worker: stage 2/4 done" message.
func recStage(rec map[string]any) string {
	msg, _ := rec["msg"].(string)
	fields := strings.Fields(msg)
	if len(fields) < 4 {
		return ""
	}
	return fields[3]
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
	// Accounted mirrors what the real client counts when the endpoint sends a
	// usage object; without it the totals log no token fields at all. Cost is
	// booked here too, for the same reason: the real client prices each call as
	// it returns, so a fake that skipped it would leave the worker's persisted
	// spend at zero and the assertions on it vacuous.
	llm.TotalsFrom(ctx).Add(llm.Usage{
		Requests: 1, Accounted: 1,
		PromptTokens: 1000, CompletionTokens: 200, ReasoningTokens: 120, TotalTokens: 1200,
		// 1000 uncached in at 75 + 200 out at 250 nanodollars. Cached stays at
		// zero deliberately: a REPORTED zero has to survive to the log line, and
		// the assertion on chat_tokens_cached below is what holds it there.
		CostNanoUSD:    125_000,
		InferenceNanos: int64(250 * time.Millisecond)})

	sys := m[0].Content
	switch {
	case strings.Contains(sys, "cohesive summary"):
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
	case strings.Contains(sys, "cohesive summary"):
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
		ID: id, URL: "https://youtu.be/" + id, Title: "A Test Video", ChannelName: "A Test Channel"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	seedTranscript(t, h, id, relPath)
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
		EmbedModel: "test-model", EmbedDim: 1536, Logger: log})
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
		"attempt": "1/3", "resumed": false} {
		if start[k] != want {
			t.Errorf("started.%s = %v, want %v", k, start[k], want)
		}
	}

	// Every stage announces itself before it runs and reports when it is done,
	// both numbered, so the log says where a video is while it is still there.
	for i, step := range pipelineStages {
		stage := strconv.Itoa(i+1) + "/4"
		start := findStageRec(recs, step, "started")
		if start == nil {
			t.Fatalf("stage %s (%s) never announced its start", stage, step)
		}
		if got := recStage(start); got != stage {
			t.Errorf("%s started as stage %s, want %s", step, got, stage)
		}
		rec := findStep(recs, step)
		if rec == nil {
			t.Fatalf("no stage-done record for %q", step)
		}
		if got := recStage(rec); got != stage {
			t.Errorf("%s finished as stage %s, want %s", step, got, stage)
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
	// Inference time is the sum of the calls' own durations, and wait_ms is
	// everything else — pacing, embedding, disk, SQLite.
	inference, _ := fin["chat_inference_ms"].(float64)
	if inference <= 0 {
		t.Errorf("finished record has no inference time: %v", fin)
	}
	// duration - inference, floored at zero. The fake bills more inference than
	// this in-memory run takes in wall time, so here it floors.
	wantWait := fin["duration_ms"].(float64) - inference
	if wantWait < 0 {
		wantWait = 0
	}
	if wait, _ := fin["wait_ms"].(float64); wait != wantWait {
		t.Errorf("wait_ms %v, want %v (duration %v - inference %v)", wait, wantWait, fin["duration_ms"], inference)
	}
	// A reported zero must show as a zero rather than vanish (the whole point
	// of the Reported flag).
	if fin["chat_tokens_cached"] != "0" {
		t.Errorf("reported cached zero missing: %v", fin)
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

	// The run's spend outlives its log line: the same totals are banked on the
	// video row, which is what the Details panel reads. Compared against the
	// logged figure rather than a literal so the two can never disagree — a
	// hardcoded expectation here would keep passing if the worker banked a
	// different run's numbers.
	v, err := h.videos.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	calls := int64(len(comp.steps))
	if v.ChatUsage.PromptTokens != 1000*calls || v.ChatUsage.CompletionTokens != 200*calls {
		t.Errorf("banked tokens = %+v, want %d calls' worth", v.ChatUsage, calls)
	}
	if v.ChatUsage.CostNanoUSD != 125_000*calls {
		t.Errorf("banked cost = %d, want %d", v.ChatUsage.CostNanoUSD, 125_000*calls)
	}
	if fin["chat_cost_nano_usd"].(float64) != float64(v.ChatUsage.CostNanoUSD) {
		t.Errorf("logged cost %v disagrees with the banked %d", fin["chat_cost_nano_usd"], v.ChatUsage.CostNanoUSD)
	}
}

// finished() is reachable twice for one run: a panic raised after the normal
// call unwinds into processOne's recover, which calls it again. It only logged
// before, so a second call was free; it writes now, and a second write would
// double the video's recorded cost.
func TestAnalysisRun_banksItsSpendOnlyOnce(t *testing.T) {
	h := newWorkerHarness(t)
	seedVideo(t, h, "v1")
	log, _ := captureLogger()

	video, err := h.videos.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	totals := &llm.Totals{}
	totals.Add(llm.Usage{Requests: 1, Accounted: 1, PromptTokens: 1000, CostNanoUSD: 75_000})
	run := &analysisRun{
		log: log, store: h.videos, totals: totals, video: video,
		job: &summaryjobs.Job{ID: 1, Attempts: 1, MaxAttempts: 3}, started: time.Now(),
	}

	run.finished("done")
	run.finished("panic")

	got, err := h.videos.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ChatUsage.CostNanoUSD != 75_000 || got.ChatUsage.PromptTokens != 1000 {
		t.Fatalf("banked %+v, want one run's worth — the second finished() wrote again", got.ChatUsage)
	}
}

// A video the worker never made a chat call for must stay unaccounted, not be
// stamped with a zero that reads as "this analysis was free".
func TestWorkerBanksNothingWhenNoCallWasAccounted(t *testing.T) {
	h := newWorkerHarness(t)
	seedVideo(t, h, "v1")
	log, _ := captureLogger()

	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(&fakeWorkerCompleter{}), Embedder: fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536, Logger: log})
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}

	v, err := h.videos.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if !v.ChatUsage.Empty() {
		t.Fatalf("unaccounted run banked %+v", v.ChatUsage)
	}
}

func TestWorkerLogsSkippedStepsOnResumedJob(t *testing.T) {
	h := newWorkerHarness(t)
	seedVideo(t, h, "v2")
	// A job resumed after the key-points step failed: summary, category and
	// embeddings are already stored, so summary and classify are skipped. Key
	// points re-runs — and embedding re-runs behind it, because the chapters
	// that call writes are what chapter chunks are built from, so the stored
	// index no longer matches the analysis however current its rev looked at
	// claim time.
	if err := h.videos.SetSummaryText("v2", "Existing summary."); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	if err := h.videos.SetCategory("v2", "ai"); err != nil {
		t.Fatalf("set category: %v", err)
	}
	if _, err := h.db.Exec(fmt.Sprintf(`UPDATE videos SET embed_model = 'test-model', embed_rev = %d, summary_status = 'done' WHERE id = 'v2'`, rag.ChunkRecipeRev)); err != nil {
		t.Fatalf("mark embedded: %v", err)
	}

	log, buf := captureLogger()
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(&usageCompleter{}), Embedder: fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536, Logger: log})
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}

	recs := buf.records(t)
	if start := findRec(recs, "summarize worker: analysis started"); start["resumed"] != true {
		t.Errorf("resumed = %v, want true", start["resumed"])
	}
	// A skipped stage keeps its own number, so the stages a resumed job does
	// run are still numbered where a reader expects them.
	for _, want := range []struct{ step, stage string }{
		{"summary", "1/4"}, {"classify", "2/4"}} {
		rec := findStageRec(recs, want.step, "skipped")
		if rec == nil {
			t.Fatalf("stage %s (%s) was not logged as skipped", want.stage, want.step)
		}
		if got := recStage(rec); got != want.stage {
			t.Errorf("%s skipped as stage %s, want %s", want.step, got, want.stage)
		}
	}
	kp := findStep(recs, "keypoints")
	if kp == nil {
		t.Fatal("key-points stage did not run on the resumed job")
	}
	if got := recStage(kp); got != "3/4" {
		t.Errorf("keypoints ran as stage %s, want 3/4", got)
	}
	// Embedding is NOT skipped here, however current embed_rev looked when the
	// job was claimed: the key-points call that just ran rewrote the chapters
	// chunk chunks are built from, and SetKeyPoints zeroed embed_rev in the same
	// statement. Skipping would leave an index that predates its own chapters.
	emb := findStep(recs, "embedding")
	if emb == nil {
		t.Fatal("embedding stage did not re-run after key points rewrote the chapters")
	}
	if got := recStage(emb); got != "4/4" {
		t.Errorf("embedding ran as stage %s, want 4/4", got)
	}
}

func TestWorkerLogsRetryAttemptWhenKeyPointsFail(t *testing.T) {
	h := newWorkerHarness(t)
	seedVideo(t, h, "v3")
	log, buf := captureLogger()
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(keypointsErrCompleter{}), Embedder: fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536, Logger: log})
	if _, err := w.processOne(context.Background()); err == nil {
		t.Fatal("expected processOne to surface the key-points failure")
	}

	recs := buf.records(t)
	rec := findRec(recs, "summarize worker: keypoints step failed")
	if rec == nil {
		t.Fatal("no retry record")
	}
	for k, want := range map[string]any{
		"attempt": "1/3", "will_retry": true,
		"title": "A Test Video", "channel": "A Test Channel"} {
		if rec[k] != want {
			t.Errorf("retry.%s = %v, want %v", k, rec[k], want)
		}
	}
	fin := findRec(recs, "summarize worker: analysis finished")
	if fin == nil || fin["outcome"] != "keypoints_failed" {
		t.Fatalf("finished record = %v", fin)
	}
	// The job has attempts left, so the terminal line must not read as final.
	if fin["will_retry"] != true {
		t.Errorf("finished.will_retry = %v, want true", fin["will_retry"])
	}
}

func TestWorkerLogsExhaustedRetryAsFinal(t *testing.T) {
	h := newWorkerHarness(t)
	seedVideo(t, h, "v3b")
	// Burn the attempts so this claim is the last one.
	if _, err := h.db.Exec(`UPDATE summary_jobs SET attempts = 2 WHERE video_id = 'v3b'`); err != nil {
		t.Fatalf("bump attempts: %v", err)
	}
	log, buf := captureLogger()
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(keypointsErrCompleter{}), Embedder: fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536, Logger: log})
	if _, err := w.processOne(context.Background()); err == nil {
		t.Fatal("expected the key-points failure to surface")
	}
	recs := buf.records(t)
	rec := findRec(recs, "summarize worker: keypoints step failed")
	if rec["will_retry"] != false {
		t.Errorf("retry.will_retry = %v, want false", rec["will_retry"])
	}
	// The last allowed attempt reads as such in one field.
	if rec["attempt"] != "3/3" {
		t.Errorf("retry.attempt = %v, want 3/3", rec["attempt"])
	}
	if fin := findRec(recs, "summarize worker: analysis finished"); fin["will_retry"] != false {
		t.Errorf("finished.will_retry = %v, want false", fin["will_retry"])
	}
}

// An inbox read succeeds under a name of its own — done_inbox — and the finish
// line must say so. The predicate compared against "done" alone, so the one
// outcome peeq produces most often for a fresh subscription video printed
// will_retry=true beside it: a finished summary, already rendering in the
// Player, logged as though the queue still owed it something.
func TestWorkerLogsAnInboxReadAsTerminal(t *testing.T) {
	h := newWorkerHarness(t)
	rel := writeInboxCaption(t, h, "inbox-log")
	if err := h.videos.Upsert(videos.Video{ID: "inbox-log", URL: "https://youtu.be/inbox-log"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedTranscript(t, h, "inbox-log", rel)
	if err := h.videos.SetStatus("inbox-log", videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, err := h.jobs.Enqueue("inbox-log"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	log, buf := captureLogger()
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(&countingCompleter{reply: "A summary of the video."}),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536, Logger: log})
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}

	fin := findRec(buf.records(t), "summarize worker: analysis finished")
	if fin == nil || fin["outcome"] != "done_inbox" {
		t.Fatalf("finished record = %v, want outcome done_inbox", fin)
	}
	// Attempts remain on the job, so only the outcome can make this false.
	if fin["will_retry"] != false {
		t.Errorf("finished.will_retry = %v, want false — done_inbox is terminal", fin["will_retry"])
	}
}

// An embedding failure is named as such and stays retryable. It used to log
// outcome=error — the same word a summary failure uses — because it went through
// failJob, which also marked the video summary_status='error' with a finished
// summary sitting on it. Both halves of that were wrong: what failed is the
// index, and it has attempts left.
func TestWorkerLogsEmbedFailureAsItsOwnRetryableOutcome(t *testing.T) {
	h := newWorkerHarness(t)
	seedVideo(t, h, "v4")
	log, buf := captureLogger()
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(&usageCompleter{}), Embedder: failingEmbedder{},
		EmbedModel: "test-model", EmbedDim: 1536, Logger: log})
	if _, err := w.processOne(context.Background()); err == nil {
		t.Fatal("expected processOne to surface the embed failure")
	}
	recs := buf.records(t)
	fin := findRec(recs, "summarize worker: analysis finished")
	if fin == nil || fin["outcome"] != "embedding_failed" {
		t.Fatalf("finished record = %v", fin)
	}
	if fin["will_retry"] != true {
		t.Errorf("finished.will_retry = %v, want true — two attempts remain", fin["will_retry"])
	}
	rec := findRec(recs, "summarize worker: embedding step failed")
	if rec == nil {
		t.Fatal("no embedding retry record")
	}
	if rec["attempt"] != "1/3" {
		t.Errorf("retry.attempt = %v, want 1/3", rec["attempt"])
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
		EmbedModel: "test-model", EmbedDim: 4, Logger: log})
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}
	recs := buf.records(t)
	rec := findRec(recs, "summarize worker: no transcript")
	if rec == nil {
		t.Fatal("no 'no transcript' record")
	}
	if rec["reason"] != "no transcript" || rec["title"] != "No Subs" {
		t.Errorf("no-transcript record = %v", rec)
	}
	// A video that is never analyzed must not announce an analysis: an import
	// of subtitle-less videos would otherwise log thousands of starts that
	// never end.
	if started := findRec(recs, "summarize worker: analysis started"); started != nil {
		t.Errorf("no-transcript video logged an analysis start: %v", started)
	}
	if fin := findRec(recs, "summarize worker: analysis finished"); fin != nil {
		t.Errorf("no-transcript video logged an analysis finish: %v", fin)
	}
}

func TestStageNumbering(t *testing.T) {
	for i, step := range pipelineStages {
		want := strconv.Itoa(i+1) + "/4"
		if got := stageOf(step); got != want {
			t.Errorf("stageOf(%q) = %q, want %q", step, got, want)
		}
	}
	// A stage that was added to the pipeline but never listed is named rather
	// than mis-numbered, and its message stays readable instead of collapsing
	// to "stage  done" with a hole in it.
	if got := stageOf("not-a-stage"); got != "" {
		t.Errorf("stageOf an unlisted stage = %q, want empty", got)
	}
	if got := stageMessage("not-a-stage", "done"); got != "summarize worker: stage not-a-stage done" {
		t.Errorf("stageMessage of an unlisted stage = %q", got)
	}
	if got := stageMessage("classify", "started"); got != "summarize worker: stage 2/4 started" {
		t.Errorf("stageMessage(classify) = %q", got)
	}
}

func TestAnalysisRunNilIsAnInertRun(t *testing.T) {
	// The failure paths that run before the analysis is announced — and the
	// panic recovery, which may fire before it exists — call these on a nil
	// run. Each must be a silent no-op rather than a second panic.
	var run *analysisRun
	if run.ident() != nil {
		t.Error("nil run returned identity attrs")
	}
	if run.stepElapsedMs() != 0 {
		t.Error("nil run returned a step duration")
	}
	run.skipped("summary", "n/a")
	run.finished("error")

	// step is the exception: it would have to invent a context, and an
	// invented one carries no cancellation, so its LLM calls would outlive a
	// shutdown. It panics instead of degrading quietly.
	defer func() {
		if recover() == nil {
			t.Error("step on a nil run did not panic")
		}
	}()
	run.step("summary")
}

// panicCompleter blows up inside the summary step, exercising processOne's
// recover.
type panicCompleter struct{}

func (panicCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	panic("boom")
}

func TestWorkerLogsTerminalLineOnPanic(t *testing.T) {
	h := newWorkerHarness(t)
	seedVideo(t, h, "v8")
	log, buf := captureLogger()
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(panicCompleter{}), Embedder: fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536, Logger: log})
	// The recover in processOne swallows the panic; the loop must survive it.
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}
	recs := buf.records(t)
	if findRec(recs, "summarize worker: recovered") == nil {
		t.Fatal("no recovered record")
	}
	fin := findRec(recs, "summarize worker: analysis finished")
	if fin == nil || fin["outcome"] != "panic" {
		t.Fatalf("finished record = %v", fin)
	}
	if fin["video_id"] != "v8" || fin["title"] != "A Test Video" {
		t.Errorf("panic finish lost identity: %v", fin)
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
		EmbedModel: "test-model", EmbedDim: 1536, Logger: log})
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
