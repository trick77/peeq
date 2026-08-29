package summarize

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/videos"
)

// failCompleter fails the test if it is ever called: used to prove the
// no_transcript short-circuit never reaches the Summarizer.
type failCompleter struct{ t *testing.T }

func (f failCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	f.t.Fatal("Completer.Complete should not be called for a no-transcript video")
	return "", nil
}

// failEmbedder fails the test if it is ever called: used to prove the
// no_transcript short-circuit never reaches the Embedder.
type failEmbedder struct{ t *testing.T }

func (f failEmbedder) EmbedBatched(ctx context.Context, inputs []string, _ time.Duration) ([][]float32, error) {
	f.t.Fatal("Embedder.Embed should not be called for a no-transcript video")
	return nil, nil
}

// fakeWorkerCompleter dispatches canned replies by prompt content, mirroring
// summarizer_test.go's fakeCompleter.
type fakeWorkerCompleter struct{}

func (fakeWorkerCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	if len(m) > 0 {
		sys := m[0].Content
		if strings.Contains(sys, "cohesive summary") {
			return "Overall prose summary.", nil
		}
		if strings.Contains(sys, "category id") {
			return "ai", nil
		}
		if strings.Contains(sys, "JSON") {
			return `{"key_points":[{"ts":0,"text":"a point"}]}`, nil
		}
	}
	return "chunk summary", nil
}

// classifyErrCompleter answers summary/keypoints normally but errors on the
// classify call, proving a classify failure does NOT fail the job and leaves
// the category at its 'uncategorized' default.
type classifyErrCompleter struct{}

func (classifyErrCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	sys := m[0].Content
	switch {
	case strings.Contains(sys, "cohesive summary"):
		return "Overall prose summary.", nil
	case strings.Contains(sys, "category id"):
		return "", errors.New("classify boom")
	case strings.Contains(sys, "JSON"):
		return `{"key_points":[]}`, nil
	}
	return "chunk summary", nil
}

// failingEmbedder always errors: used to prove processOne surfaces a
// non-nil error (via failJob) when embedding fails, instead of silently
// swallowing it (regression test for the silent summarize-worker error).
type failingEmbedder struct{}

func (failingEmbedder) EmbedBatched(ctx context.Context, inputs []string, _ time.Duration) ([][]float32, error) {
	return nil, errors.New("boom")
}

// fakeWorkerEmbedder returns a dim-length vector per input.
type fakeWorkerEmbedder struct{ dim int }

func (f fakeWorkerEmbedder) EmbedBatched(ctx context.Context, inputs []string, _ time.Duration) ([][]float32, error) {
	out := make([][]float32, len(inputs))
	for i := range inputs {
		v := make([]float32, f.dim)
		v[0] = 1
		out[i] = v
	}
	return out, nil
}

type workerHarness struct {
	db       *sql.DB
	videos   *videos.Store
	jobs     *summaryjobs.Store
	rag      *rag.Store
	mediaDir string
}

func newWorkerHarness(t *testing.T) *workerHarness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return &workerHarness{
		db:     db,
		videos: videos.New(db),
		// No backoff ladder: these tests drive attempt 2 straight after attempt 1
		// and assert on what the retry does, not on how long it waits. The ladder
		// itself is covered in summaryjobs/store_test.go.
		jobs:     summaryjobs.NewWithBackoff(db, nil),
		rag:      rag.NewStore(db),
		mediaDir: t.TempDir()}
}

func TestWorkerNoTranscriptShortCircuits(t *testing.T) {
	h := newWorkerHarness(t)
	if err := h.videos.Upsert(videos.Video{ID: "v1", URL: "https://youtu.be/v1"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	jobID, err := h.jobs.Enqueue("v1")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(failCompleter{t: t}),
		Embedder:   failEmbedder{t: t},
		EmbedModel: "test-model",
		EmbedDim:   4})

	did, err := w.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if !did {
		t.Fatal("expected processOne to claim and process the job")
	}

	v, err := h.videos.Get("v1")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.SummaryStatus != "no_transcript" {
		t.Fatalf("expected summary_status=no_transcript, got %q", v.SummaryStatus)
	}

	var state string
	if err := h.db.QueryRow(`SELECT state FROM summary_jobs WHERE id = ?`, jobID).Scan(&state); err != nil {
		t.Fatalf("query job state: %v", err)
	}
	if state != "done" {
		t.Fatalf("expected job state=done, got %q", state)
	}
}

func TestWorkerHappyPathPersistsSummaryAndChunks(t *testing.T) {
	h := newWorkerHarness(t)

	relPath := "v2/captions.en.vtt"
	full := filepath.Join(h.mediaDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vtt := "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nHello there, welcome to the video.\n\n" +
		"00:00:02.000 --> 00:00:04.000\nToday we will talk about testing Go workers.\n"
	if err := os.WriteFile(full, []byte(vtt), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}

	if err := h.videos.Upsert(videos.Video{ID: "v2", URL: "https://youtu.be/v2"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	seedTranscript(t, h, "v2", relPath)
	if _, err := h.jobs.Enqueue("v2"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(fakeWorkerCompleter{}),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model",
		EmbedDim:   1536})

	did, err := w.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if !did {
		t.Fatal("expected processOne to claim and process the job")
	}

	v, err := h.videos.Get("v2")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.SummaryStatus != "done" {
		t.Fatalf("expected summary_status=done, got %q (err=%q)", v.SummaryStatus, v.SummaryError)
	}
	if v.Summary == "" {
		t.Fatal("expected non-empty summary")
	}

	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transcript_chunks WHERE video_id = ?`, "v2").Scan(&count); err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if count == 0 {
		t.Fatal("expected transcript_chunks rows to be inserted")
	}
}

// TestProcessOneSetsCategory asserts a happy-path processOne run classifies
// the video and persists the category returned by the model.
func TestProcessOneSetsCategory(t *testing.T) {
	h := newWorkerHarness(t)

	relPath := "v6/captions.en.vtt"
	full := filepath.Join(h.mediaDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vtt := "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nHello there, welcome to the video.\n\n" +
		"00:00:02.000 --> 00:00:04.000\nToday we will talk about testing Go workers.\n"
	if err := os.WriteFile(full, []byte(vtt), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}

	if err := h.videos.Upsert(videos.Video{ID: "v6", URL: "https://youtu.be/v6"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	seedTranscript(t, h, "v6", relPath)
	if _, err := h.jobs.Enqueue("v6"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(fakeWorkerCompleter{}),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model",
		EmbedDim:   1536})

	did, err := w.processOne(context.Background())
	if err != nil || !did {
		t.Fatalf("processOne did=%v err=%v", did, err)
	}

	v, err := h.videos.Get("v6")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.Category != "ai" {
		t.Fatalf("category = %q, want ai", v.Category)
	}
}

// TestClassifyErrorLeavesUncategorizedAndJobSucceeds asserts a classify
// failure is logged, not fatal: the summarize job still finishes done and
// the category stays at its 'uncategorized' default.
func TestClassifyErrorLeavesUncategorizedAndJobSucceeds(t *testing.T) {
	h := newWorkerHarness(t)

	relPath := "v7/captions.en.vtt"
	full := filepath.Join(h.mediaDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vtt := "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nHello there, welcome to the video.\n\n" +
		"00:00:02.000 --> 00:00:04.000\nToday we will talk about testing Go workers.\n"
	if err := os.WriteFile(full, []byte(vtt), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}

	if err := h.videos.Upsert(videos.Video{ID: "v7", URL: "https://youtu.be/v7"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	seedTranscript(t, h, "v7", relPath)
	if _, err := h.jobs.Enqueue("v7"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(classifyErrCompleter{}),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model",
		EmbedDim:   1536})

	did, err := w.processOne(context.Background())
	if err != nil || !did {
		t.Fatalf("processOne did=%v err=%v", did, err)
	}

	v, err := h.videos.Get("v7")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.SummaryStatus != "done" {
		t.Fatalf("summary_status = %q, want done (classify error must not fail the job)", v.SummaryStatus)
	}
	if v.Category != "uncategorized" {
		t.Fatalf("category = %q, want uncategorized", v.Category)
	}
}

// TestProcessOneReturnsErrorOnEmbedFailure is the regression test for the
// silent summarize-worker error (Item 8): failJob used to return
// Jobs.Fail's result, which is nil on the common path, so processOne
// returned (true, nil) even though the job failed and the worker's Run
// loop (which only logs when err != nil) stayed silent. failJob must now
// surface a non-nil error while leaving the DB outcome (job failed, video
// summary_status=error) unchanged.
func TestProcessOneReturnsErrorOnEmbedFailure(t *testing.T) {
	h := newWorkerHarness(t)

	relPath := "v4/captions.en.vtt"
	full := filepath.Join(h.mediaDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vtt := "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nHello there, welcome to the video.\n\n" +
		"00:00:02.000 --> 00:00:04.000\nToday we will talk about testing Go workers.\n"
	if err := os.WriteFile(full, []byte(vtt), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}

	if err := h.videos.Upsert(videos.Video{ID: "v4", URL: "https://youtu.be/v4"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	seedTranscript(t, h, "v4", relPath)
	if _, err := h.jobs.Enqueue("v4"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(fakeWorkerCompleter{}),
		Embedder:   failingEmbedder{},
		EmbedModel: "test-model",
		EmbedDim:   1536})

	did, err := w.processOne(context.Background())
	if !did {
		t.Fatal("did = false, want true")
	}
	if err == nil {
		t.Fatal("err = nil, want non-nil so Run logs the failure")
	}

	v, getErr := h.videos.Get("v4")
	if getErr != nil {
		t.Fatalf("get video: %v", getErr)
	}
	// The summary itself is finished and readable, so it keeps "done". Marking
	// it 'error' here — which this path used to do — made the Player print
	// "Summarization failed" directly above the summary it was rendering.
	if v.SummaryStatus != "done" {
		t.Errorf("summary_status = %q, want done — the summary succeeded, the index did not", v.SummaryStatus)
	}
	if v.Summary == "" {
		t.Error("summary was discarded on an embedding failure — it must be kept")
	}
	// What DID fail is reported here instead, and this is what the Player reads
	// to say the video is not searchable yet.
	if v.Indexed() {
		t.Error("Indexed() = true after the embedding failed")
	}
}

// fakeActivityRecorder captures the events failJob records. Satisfies the
// summarize.ActivityRecorder interface.
type fakeActivityRecorder struct{ events []activity.Event }

func (f *fakeActivityRecorder) Record(e activity.Event) { f.events = append(f.events, e) }

// summaryErrCompleter fails the very first LLM call — the prose summary — so a
// test can drive the failJob path rather than the post-summary requeue path.
type summaryErrCompleter struct{}

func (summaryErrCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	return "", errors.New("summary boom")
}

// seedFailingVideo enqueues a summarizable video and fails one step of its
// analysis, so processOne drives it through failJob or requeueJob. summarizer
// nil means "fail at the embedding step"; pass one to fail earlier instead.
// maxAttempts controls whether that failure is terminal (1) or a retry (>1). It
// returns the recorder wired onto the worker so the test can inspect what was
// recorded.
func seedFailingVideo(t *testing.T, id string, maxAttempts int, completer Completer) *fakeActivityRecorder {
	t.Helper()
	h := newWorkerHarness(t)

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
	if err := h.videos.Upsert(videos.Video{ID: id, URL: "https://youtu.be/" + id, Title: "Test " + id}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	seedTranscript(t, h, id, relPath)
	if _, err := h.jobs.Enqueue(id); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// max_attempts=1 makes the single failing attempt terminal; the default (3)
	// leaves attempts to spare, so the same failure requeues instead.
	if _, err := h.db.Exec(`UPDATE summary_jobs SET max_attempts = ? WHERE video_id = ?`, maxAttempts, id); err != nil {
		t.Fatalf("set max_attempts: %v", err)
	}

	if completer == nil {
		completer = fakeWorkerCompleter{}
	}
	rec := &fakeActivityRecorder{}
	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(completer),
		Embedder:   failingEmbedder{},
		EmbedModel: "test-model",
		EmbedDim:   1536,
		Activity:   rec})
	if _, err := w.processOne(context.Background()); err == nil {
		t.Fatal("processOne err = nil, want non-nil (the seeded failure)")
	}
	return rec
}

// A terminally failed job writes exactly one Activity row, naming the step that
// failed, and a merely-requeued one writes none (a retry is not news).
//
// The post-summary steps are the point. A job that dies at key points or
// embedding leaves summary_status='done' behind, drops off the active queue, and
// is skipped by the boot sweep — so the video reads as complete everywhere while
// its highlights or its search index are permanently missing. This row is the
// only trace such a video leaves outside the log. It is a warn rather than a
// fail because the summary really did succeed.
func TestTerminalFailureRecordsExactlyOneActivityRow(t *testing.T) {
	cases := []struct {
		name      string
		completer Completer
		outcome   string
		summary   string
	}{
		{"summary", summaryErrCompleter{}, activity.OutcomeFail, "summary failed"},
		{"keypoints", keypointsErrCompleter{}, activity.OutcomeWarn, "keypoints failed"},
		{"embedding", nil, activity.OutcomeWarn, "embedding failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name+" terminal records one row", func(t *testing.T) {
			rec := seedFailingVideo(t, "term-"+tc.name, 1, tc.completer)
			if len(rec.events) != 1 {
				t.Fatalf("recorded %d rows, want 1: %+v", len(rec.events), rec.events)
			}
			e := rec.events[0]
			if e.Kind != activity.KindSummary || e.Outcome != tc.outcome {
				t.Fatalf("event kind/outcome = %q/%q, want summary/%s", e.Kind, e.Outcome, tc.outcome)
			}
			if e.SubjectID != "term-"+tc.name || e.Summary != tc.summary {
				t.Fatalf("event = %+v, want subject term-%s summary %q", e, tc.name, tc.summary)
			}
			if e.Detail == "" {
				t.Error("event detail is empty — it carries the failing bound, which is the only place it is recorded")
			}
		})
		t.Run(tc.name+" requeue records nothing", func(t *testing.T) {
			rec := seedFailingVideo(t, "retry-"+tc.name, 3, tc.completer)
			if len(rec.events) != 0 {
				t.Fatalf("recorded %d rows on a retry, want 0: %+v", len(rec.events), rec.events)
			}
		})
	}
}

// TestProcessOneIndexesSummaryChunk asserts the video's overall summary is
// indexed as one extra transcript_chunks row with kind='summary' and
// start_seconds=0, so hybrid search also matches against summaries (spec §7).
func TestProcessOneIndexesSummaryChunk(t *testing.T) {
	h := newWorkerHarness(t)

	relPath := "v5/captions.en.vtt"
	full := filepath.Join(h.mediaDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vtt := "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nHello there, welcome to the video.\n\n" +
		"00:00:02.000 --> 00:00:04.000\nToday we will talk about testing Go workers.\n"
	if err := os.WriteFile(full, []byte(vtt), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}

	if err := h.videos.Upsert(videos.Video{ID: "v5", URL: "https://youtu.be/v5"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	seedTranscript(t, h, "v5", relPath)
	if _, err := h.jobs.Enqueue("v5"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(fakeWorkerCompleter{}),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model",
		EmbedDim:   1536})

	did, err := w.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if !did {
		t.Fatal("expected processOne to claim and process the job")
	}

	var kind string
	var start int
	err = h.db.QueryRow(
		`SELECT kind, start_seconds FROM transcript_chunks WHERE video_id='v5' AND kind='summary'`).Scan(&kind, &start)
	if err != nil {
		t.Fatalf("no summary chunk indexed: %v", err)
	}
	if start != 0 {
		t.Errorf("summary chunk start_seconds = %d, want 0", start)
	}
}

// contains reports whether s is present in list.
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// containsPrefix reports whether any element of list has prefix as a prefix.
func containsPrefix(list []string, prefix string) bool {
	for _, v := range list {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
}

// TestProcessOneEmitsPhaseEvents asserts a happy-path processOne run emits
// SSE phase events (via WorkerDeps.OnPhase) so the Player can show live
// summarize progress instead of a static "Summarizing…" label.
func TestProcessOneEmitsPhaseEvents(t *testing.T) {
	h := newWorkerHarness(t)

	relPath := "v1/captions.en.vtt"
	full := filepath.Join(h.mediaDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vtt := "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nHello there, welcome to the video.\n\n" +
		"00:00:02.000 --> 00:00:04.000\nToday we will talk about testing Go workers.\n"
	if err := os.WriteFile(full, []byte(vtt), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}

	if err := h.videos.Upsert(videos.Video{ID: "v1", URL: "https://youtu.be/v1"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	seedTranscript(t, h, "v1", relPath)
	if _, err := h.jobs.Enqueue("v1"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var events []string // "videoID:status:phase"
	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(fakeWorkerCompleter{}),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model",
		EmbedDim:   1536,
		OnPhase: func(id, status, phase string) {
			events = append(events, id+":"+status+":"+phase)
		}})

	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Must include a running/summarizing event for v1 and a terminal done event.
	if !containsPrefix(events, "v1:running:") {
		t.Errorf("no running event for v1: %v", events)
	}
	if !contains(events, "v1:done:") {
		t.Errorf("no done event for v1: %v", events)
	}
	// The key-points stage is signalled as done/keypoints: status "done" lets the
	// live Player fetch the ready summary before the fragile step finishes, while
	// phase "keypoints" advances the Queue meter to the final stage.
	if !contains(events, "v1:done:keypoints") {
		t.Errorf("no done/keypoints event for v1: %v", events)
	}
}

// vttCue renders one WebVTT cue block starting at startSeconds (2s long) with
// wordCount distinct words, so the transcript spans multiple rag.Chunk chunks
// and each cue is unambiguously identifiable by its words.
func vttCue(startSeconds, wordCount int, word string) string {
	start := fmtVTTTimestamp(startSeconds)
	end := fmtVTTTimestamp(startSeconds + 2)
	var b strings.Builder
	fmt.Fprintf(&b, "%s --> %s\n", start, end)
	for i := 0; i < wordCount; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s%d", word, i)
	}
	b.WriteString("\n\n")
	return b.String()
}

func fmtVTTTimestamp(totalSeconds int) string {
	h := totalSeconds / 3600
	m := (totalSeconds % 3600) / 60
	s := totalSeconds % 60
	return fmt.Sprintf("%02d:%02d:%02d.000", h, m, s)
}

// TestWorkerChunkTimestampsAreExactAndMonotonic is the regression test for the
// cueStartFor prefix-match bug: because rag.Chunk overlaps chunks by ~75
// tokens, every non-first chunk used to start mid-cue, the old prefix match
// against chunk text never matched, and start_seconds silently fell back to 0
// for most chunks. The word-offset mapping (chunk.WordOffset -> cumulative
// cue word count) fixes this exactly, independent of the overlap.
func TestWorkerChunkTimestampsAreExactAndMonotonic(t *testing.T) {
	h := newWorkerHarness(t)

	relPath := "v3/captions.en.vtt"
	full := filepath.Join(h.mediaDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var vtt strings.Builder
	vtt.WriteString("WEBVTT\n\n")
	cueStarts := []int{0, 30, 90, 150}
	for _, s := range cueStarts {
		vtt.WriteString(vttCue(s, 200, fmt.Sprintf("w%ds", s)))
	}
	if err := os.WriteFile(full, []byte(vtt.String()), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}

	if err := h.videos.Upsert(videos.Video{ID: "v3", URL: "https://youtu.be/v3"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	seedTranscript(t, h, "v3", relPath)
	if _, err := h.jobs.Enqueue("v3"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(fakeWorkerCompleter{}),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model",
		EmbedDim:   1536})

	did, err := w.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if !did {
		t.Fatal("expected processOne to claim and process the job")
	}

	// kind='transcript' excludes the trailing summary chunk (Task 6), which is
	// appended after all transcript chunks with start_seconds=0 by design and
	// would otherwise look like a monotonicity regression here.
	rows, err := h.db.Query(`SELECT ordinal, start_seconds FROM transcript_chunks WHERE video_id = ? AND kind = 'transcript' ORDER BY ordinal`, "v3")
	if err != nil {
		t.Fatalf("query chunks: %v", err)
	}
	defer rows.Close()

	var starts []int
	for rows.Next() {
		var ordinal, startSeconds int
		if err := rows.Scan(&ordinal, &startSeconds); err != nil {
			t.Fatalf("scan: %v", err)
		}
		starts = append(starts, startSeconds)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(starts) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(starts))
	}
	if starts[0] != cueStarts[0] {
		t.Fatalf("expected first chunk's start_seconds == first cue's start (%d), got %d", cueStarts[0], starts[0])
	}
	anyNonZero := false
	for i, s := range starts {
		if i > 0 && s < starts[i-1] {
			t.Fatalf("start_seconds not non-decreasing at ordinal %d: %d then %d", i, starts[i-1], s)
		}
		if s > 0 {
			anyNonZero = true
		}
	}
	if !anyNonZero {
		t.Fatal("expected at least one non-first chunk to have start_seconds > 0 (all zero means the old prefix-match bug regressed)")
	}
}

// keyPointsFailOnceCompleter answers the map/summary/classify calls normally,
// but fails the key-points call on its first invocation and succeeds after —
// modelling a flaky reasoning endpoint that eventually cooperates.
type keyPointsFailOnceCompleter struct{ kpCalls int }

func (c *keyPointsFailOnceCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	sys := m[0].Content
	switch {
	case strings.Contains(sys, "cohesive summary"):
		return "Overall prose summary.", nil
	case strings.Contains(sys, "category id"):
		return "ai", nil
	case strings.Contains(sys, "JSON"):
		c.kpCalls++
		if c.kpCalls == 1 {
			return "", errors.New("keypoints timeout")
		}
		return `{"key_points":[{"ts":0,"text":"a point"}]}`, nil
	}
	return "chunk summary", nil
}

// countingEmbedder records how many times Embed is called, to prove a resumed
// job does not re-embed.
type countingEmbedder struct {
	dim   int
	calls int
}

func (c *countingEmbedder) EmbedBatched(ctx context.Context, inputs []string, _ time.Duration) ([][]float32, error) {
	c.calls++
	out := make([][]float32, len(inputs))
	for i := range inputs {
		v := make([]float32, c.dim)
		v[0] = 1
		out[i] = v
	}
	return out, nil
}

// TestWorkerResumable_keyPointsFailureKeepsSummaryAndRetriesOnlyKeyPoints is the
// point of the split: a key-points timeout must not discard the summary or
// embeddings or regress the video's status, and the retry must re-run ONLY the
// key-points step.
func TestWorkerResumable_keyPointsFailureKeepsSummaryAndRetriesOnlyKeyPoints(t *testing.T) {
	h := newWorkerHarness(t)

	relPath := "v3/captions.en.vtt"
	full := filepath.Join(h.mediaDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vtt := "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nHello there, welcome to the video.\n\n" +
		"00:00:02.000 --> 00:00:04.000\nToday we will talk about testing Go workers.\n"
	if err := os.WriteFile(full, []byte(vtt), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}
	if err := h.videos.Upsert(videos.Video{ID: "v3", URL: "https://youtu.be/v3"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedTranscript(t, h, "v3", relPath)
	jobID, err := h.jobs.Enqueue("v3")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	completer := &keyPointsFailOnceCompleter{}
	embedder := &countingEmbedder{dim: 1536}
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(completer), Embedder: embedder,
		EmbedModel: "test-model", EmbedDim: 1536})

	// Attempt 1: key-points fails. Summary + embeddings persist, the video is
	// marked done, and the job requeues.
	if _, err := w.processOne(context.Background()); err == nil {
		t.Fatal("expected the key-points failure to surface")
	}
	v, err := h.videos.Get("v3")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.Summary == "" {
		t.Error("summary was discarded on a key-points failure — it must be kept")
	}
	if v.EmbedModel == "" {
		t.Error("embeddings were not persisted before the fragile step")
	}
	if v.SummaryStatus != "done" {
		t.Errorf("summary_status = %q, want done (summary+search usable despite key-points failing)", v.SummaryStatus)
	}
	// The reason classification moved ahead of key points: while it sat behind
	// them, a key-points failure meant the video was never classified at all,
	// which is how a whole backlog ended up summarized but 'uncategorized'.
	if v.Category != "ai" {
		t.Errorf("category = %q, want ai — classify must not sit behind the fragile key-points step", v.Category)
	}
	if embedder.calls != 1 {
		t.Errorf("embedder called %d times, want 1", embedder.calls)
	}
	var state string
	if err := h.db.QueryRow(`SELECT state FROM summary_jobs WHERE id = ?`, jobID).Scan(&state); err != nil {
		t.Fatalf("job state: %v", err)
	}
	if state != "pending" {
		t.Errorf("job state = %q, want pending (queued for retry)", state)
	}

	// Attempt 2: skips the summary; key-points reruns and succeeds, and
	// embedding runs a SECOND time behind it. That second pass is the whole
	// point of the retry: attempt 1's index was the best-effort fallback built
	// with no chapters, and the chapters key points has now written are exactly
	// what chapter chunks are built from. Skipping it here would strand the
	// video on a chapterless index that nothing else would ever repair.
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne retry: %v", err)
	}
	v, _ = h.videos.Get("v3")
	if !strings.Contains(v.KeyPoints, "a point") {
		t.Errorf("key points not set on retry: %q", v.KeyPoints)
	}
	if embedder.calls != 2 {
		t.Errorf("embedder called %d times, want 2 — the retry must reindex with the chapters key points just wrote", embedder.calls)
	}
	if v.EmbedRev != rag.ChunkRecipeRev {
		t.Errorf("embed_rev = %d, want %d — the retry's index must be marked current", v.EmbedRev, rag.ChunkRecipeRev)
	}
	if completer.kpCalls != 2 {
		t.Errorf("key-points calls = %d, want 2 (failed, then succeeded)", completer.kpCalls)
	}
	if err := h.db.QueryRow(`SELECT state FROM summary_jobs WHERE id = ?`, jobID).Scan(&state); err != nil {
		t.Fatalf("job state: %v", err)
	}
	if state != "done" {
		t.Errorf("job state after successful retry = %q, want done", state)
	}
}

// A process killed between marking a video downloaded and enqueueing its
// summary leaves the video with no job at all, and nothing else ever revisits
// it (taimport re-runs skip downloaded videos). Run's boot sweep must adopt it.
func TestWorkerRunBackfillsDownloadedVideosWithNoJob(t *testing.T) {
	h := newWorkerHarness(t)
	if err := h.videos.Upsert(videos.Video{ID: "v1", URL: "https://youtu.be/v1"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE videos SET status='downloaded' WHERE id='v1'`); err != nil {
		t.Fatalf("mark downloaded: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(failCompleter{t: t}),
		Embedder:   failEmbedder{t: t},
		EmbedModel: "test-model",
		EmbedDim:   4})

	// Cancelled up front: Run does its boot sweep, then returns before
	// claiming anything, so the assertion is about the sweep alone.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Run(ctx)

	var state string
	if err := h.db.QueryRow(`SELECT state FROM summary_jobs WHERE video_id='v1'`).Scan(&state); err != nil {
		t.Fatalf("no job backfilled for the downloaded video: %v", err)
	}
	if state != "pending" {
		t.Fatalf("backfilled job state=%q, want pending", state)
	}
}

// seedBacklogVideo makes a downloaded video that already has a summary but is
// still 'uncategorized' — exactly the rows left behind by the era when
// classification sat behind the key-points call.
func seedBacklogVideo(t *testing.T, h *workerHarness, id string) {
	t.Helper()
	if err := h.videos.Upsert(videos.Video{ID: id, URL: "https://youtu.be/" + id, Title: "A video"}); err != nil {
		t.Fatalf("upsert %s: %v", id, err)
	}
	if _, err := h.db.Exec(`UPDATE videos SET status='downloaded' WHERE id=?`, id); err != nil {
		t.Fatalf("mark %s downloaded: %v", id, err)
	}
	if err := h.videos.SetSummaryText(id, "Overall prose summary."); err != nil {
		t.Fatalf("set summary %s: %v", id, err)
	}
}

// TestProcessOneIdleSweepClassifiesBacklog: with the job queue empty, the
// worker spends the turn repairing an uncategorized video instead of idling,
// and reports it did work so the loop's pacing applies. Once the backlog is
// drained it must report did=false — returning true on an empty backlog would
// spin the Run loop, which skips its poll interval whenever a turn did work.
func TestProcessOneIdleSweepClassifiesBacklog(t *testing.T) {
	h := newWorkerHarness(t)
	seedBacklogVideo(t, h, "v-backlog")

	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(fakeWorkerCompleter{}), Embedder: fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536})

	did, err := w.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if !did {
		t.Fatal("idle sweep should report it did work, so VideoDelay pacing applies")
	}
	v, err := h.videos.Get("v-backlog")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.Category != "ai" {
		t.Fatalf("category = %q, want ai — the idle sweep must classify the backlog", v.Category)
	}

	// Backlog drained: nothing left to do.
	did, err = w.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne (drained): %v", err)
	}
	if did {
		t.Fatal("an empty backlog must report did=false, or the Run loop spins without its poll interval")
	}
}

// TestIdleSweepParksFailuresAndAdvances: one video whose classify call always
// errors must not starve the rest of the backlog — the worker parks it for the
// process lifetime and moves on to the next video.
func TestIdleSweepParksFailuresAndAdvances(t *testing.T) {
	h := newWorkerHarness(t)
	// v-b is newer, so it is picked first; its classify call is the failing one.
	seedBacklogVideo(t, h, "v-a")
	seedBacklogVideo(t, h, "v-b")
	if _, err := h.db.Exec(`UPDATE videos SET created_at='2026-07-01' WHERE id='v-a'`); err != nil {
		t.Fatalf("age v-a: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE videos SET created_at='2026-07-02' WHERE id='v-b'`); err != nil {
		t.Fatalf("age v-b: %v", err)
	}

	failFor := "Title: "
	_ = failFor
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
			// The classify user message carries the title; fail only v-b's.
			if strings.Contains(m[1].Content, "v-b video") {
				return "", errors.New("classify boom")
			}
			return "ai", nil
		})),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536})
	if _, err := h.db.Exec(`UPDATE videos SET title='v-b video' WHERE id='v-b'`); err != nil {
		t.Fatalf("title v-b: %v", err)
	}

	// Turn 1: v-b fails and is parked, but the turn still counts as work.
	did, err := w.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if !did {
		t.Fatal("a failed classify is still a turn's work")
	}

	// Turn 2: the sweep advances to v-a rather than retrying v-b forever.
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}
	a, _ := h.videos.Get("v-a")
	if a.Category != "ai" {
		t.Fatalf("v-a category = %q, want ai — a poison video must not starve the backlog", a.Category)
	}
	b, _ := h.videos.Get("v-b")
	if b.Category != "uncategorized" {
		t.Fatalf("v-b category = %q, want it left uncategorized", b.Category)
	}

	// Turn 3: both are accounted for, so the sweep is idle.
	did, err = w.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if did {
		t.Fatal("with the only remaining video parked, the sweep must report idle")
	}
}

// TestIdleSweepParksUnusableReply: the call succeeded but the reply named no
// category. Retrying the identical prompt every turn would just burn requests,
// so the video is parked for the process lifetime like an outright failure.
func TestIdleSweepParksUnusableReply(t *testing.T) {
	h := newWorkerHarness(t)
	seedBacklogVideo(t, h, "v-junk")

	calls := 0
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
			calls++
			return "I'm not sure about this one.", nil
		})),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536})

	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}
	v, _ := h.videos.Get("v-junk")
	if v.Category != "uncategorized" {
		t.Fatalf("category = %q, want uncategorized for an unusable reply", v.Category)
	}

	did, err := w.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if did {
		t.Fatal("the parked video must not be picked up again this process")
	}
	if calls != 1 {
		t.Fatalf("classify called %d times, want 1 — the same prompt must not be retried", calls)
	}
}

// blockCategoryWrites makes exactly the category UPDATE fail, leaving every
// other write on the videos row working. Closing the db would be blunter than
// the situation under test — it would break the job claim before the worker
// ever reached the category write.
func blockCategoryWrites(t *testing.T, h *workerHarness) {
	t.Helper()
	if _, err := h.db.Exec(`CREATE TRIGGER no_category BEFORE UPDATE OF category ON videos
		BEGIN SELECT RAISE(ABORT, 'category writes blocked'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
}

// TestIdleSweepSurvivesCategoryWriteFailure: the sweep must park a video whose
// category write fails, exactly as it parks a failed classify call. Without
// that, the video stays in the result set and the sweep retries it — and its
// doomed LLM call — on every single turn.
func TestIdleSweepSurvivesCategoryWriteFailure(t *testing.T) {
	h := newWorkerHarness(t)
	seedBacklogVideo(t, h, "v-writefail")
	blockCategoryWrites(t, h)

	calls := 0
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
			calls++
			return "ai", nil
		})),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536})

	did, err := w.processOne(context.Background())
	if err != nil {
		t.Fatalf("a category write failure must not surface as a worker error: %v", err)
	}
	if !did {
		t.Fatal("the attempt still counts as a turn's work")
	}

	did, err = w.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if did || calls != 1 {
		t.Fatalf("did=%v calls=%d — a video whose write fails must be parked, not retried", did, calls)
	}
}

// TestClassifyWriteFailureDoesNotFailTheJob: the category write is best-effort
// on a job whose summary is already stored. A failing write must be logged and
// stepped over, never turned into a job failure that would discard that work.
func TestClassifyWriteFailureDoesNotFailTheJob(t *testing.T) {
	h := newWorkerHarness(t)

	relPath := "v7/captions.en.vtt"
	full := filepath.Join(h.mediaDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vtt := "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nHello there, welcome to the video.\n"
	if err := os.WriteFile(full, []byte(vtt), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}
	if err := h.videos.Upsert(videos.Video{ID: "v7", URL: "https://youtu.be/v7"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedTranscript(t, h, "v7", relPath)
	if _, err := h.jobs.Enqueue("v7"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	blockCategoryWrites(t, h)

	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(fakeWorkerCompleter{}), Embedder: fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536})

	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("a category write failure must not surface as a job error: %v", err)
	}
	v, err := h.videos.Get("v7")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.Summary == "" || v.SummaryStatus != "done" {
		t.Fatalf("summary work was discarded: status=%q summary=%q", v.SummaryStatus, v.Summary)
	}
}

// TestClassifyDoesNotOverwriteAPickMadeDuringTheJob is the race the guarded
// write exists for. The worker reads the video row before the summary call,
// so its "is it still uncategorized?" test is against a stale snapshot — and
// on a self-hosted endpoint that call is slow enough for the user to open the
// Player and pick a category while it runs. The completer below writes the
// category mid-job to stand in for that user, which no amount of re-reading
// before the call could catch.
func TestClassifyDoesNotOverwriteAPickMadeDuringTheJob(t *testing.T) {
	h := newWorkerHarness(t)

	relPath := "v8/captions.en.vtt"
	full := filepath.Join(h.mediaDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vtt := "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nHello there, welcome to the video.\n"
	if err := os.WriteFile(full, []byte(vtt), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}
	if err := h.videos.Upsert(videos.Video{ID: "v8", URL: "https://youtu.be/v8"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedTranscript(t, h, "v8", relPath)
	if _, err := h.jobs.Enqueue("v8"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
			sys := m[0].Content
			if strings.Contains(sys, "cohesive summary") {
				// The user picks a category on the Player while the summary
				// call is still in flight.
				if err := h.videos.SetCategory("v8", "gaming"); err != nil {
					t.Errorf("simulate manual pick: %v", err)
				}
				return "Overall prose summary.", nil
			}
			if strings.Contains(sys, "category id") {
				return "ai", nil
			}
			if strings.Contains(sys, "JSON") {
				return `{"key_points":[]}`, nil
			}
			return "chunk summary", nil
		})),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536})

	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}

	v, err := h.videos.Get("v8")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.Category != "gaming" {
		t.Fatalf("category = %q, want gaming — the classifier overwrote a manual pick", v.Category)
	}
}

// TestIdleSweepDoesNotOverwriteAPickMadeDuringClassify is the same race on the
// backlog sweep: NextUnclassified selects the row, then the classify call is
// slow enough for the user to pick in the meantime.
func TestIdleSweepDoesNotOverwriteAPickMadeDuringClassify(t *testing.T) {
	h := newWorkerHarness(t)
	seedBacklogVideo(t, h, "v-raced")

	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
			if err := h.videos.SetCategory("v-raced", "gaming"); err != nil {
				t.Errorf("simulate manual pick: %v", err)
			}
			return "ai", nil
		})),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536})

	did, err := w.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if !did {
		t.Fatal("the sweep still spent a turn on this video")
	}
	v, _ := h.videos.Get("v-raced")
	if v.Category != "gaming" {
		t.Fatalf("category = %q, want gaming — the sweep overwrote a manual pick", v.Category)
	}
}

// TestWorkerMusicOnlyTranscriptDiscardsStaleAnalysis is the regression this
// whole path exists for: a music video whose auto-captions are [Music] markers
// plus stray lyric fragments once got a confident, entirely invented summary,
// which was also embedded into semantic search. Re-analysis must now settle it
// as no_transcript WITHOUT calling the LLM, and must take the old summary and
// its chunks with it — otherwise the hallucination survives in search, where
// the UI gives no sign it is still there.
func TestWorkerMusicOnlyTranscriptDiscardsStaleAnalysis(t *testing.T) {
	h := newWorkerHarness(t)

	relPath := "v9/captions.en.vtt"
	full := filepath.Join(h.mediaDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	frags := []string{
		"[Music] I play games with", "[Music] you yeah", "[Music] I'm give a",
		"[Music] back I give it", "[Music]", "[Music] you scar", "[Music] I get my",
		"[Music] [Applause]", "[Music] back", "[Music] oh"}
	for i, f := range frags {
		fmt.Fprintf(&b, "00:00:%02d.000 --> 00:00:%02d.000\n%s\n\n", i*3, i*3+3, f)
	}
	if err := os.WriteFile(full, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}

	if err := h.videos.Upsert(videos.Video{
		ID: "v9", URL: "https://youtu.be/v9", DurationSeconds: 200}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	seedTranscript(t, h, "v9", relPath)
	// Seed the state a previous, credulous run left behind.
	if err := h.videos.SetSummary("v9", "A thoughtful essay on trust.",
		`[{"ts":0,"title":"Intro","source":"mimo"}]`, `[{"ts":5,"text":"Invented."}]`); err != nil {
		t.Fatalf("seed summary: %v", err)
	}
	if err := h.rag.ReplaceVideoChunks(context.Background(), "v9", rag.IndexMeta{Model: "test-model", Dim: 1536, Rev: rag.ChunkRecipeRev},
		[]rag.ChunkRow{{Ordinal: 0, Text: "A thoughtful essay on trust.", Kind: "summary"}},
		[][]float32{make([]float32, 1536)}); err != nil {
		t.Fatalf("seed chunks: %v", err)
	}
	// Reproduce what handleReprocess actually does before enqueuing: it wipes
	// the summary row and leaves the chunks for the worker. Seeding the summary
	// and enqueuing directly would test a state the UI can never produce, and
	// would hide a worker that skips the chunk delete on a blank row.
	if err := h.videos.ClearSummary("v9"); err != nil {
		t.Fatalf("clear summary: %v", err)
	}
	if err := h.videos.SetSummaryStatus("v9", "pending", ""); err != nil {
		t.Fatalf("reset status: %v", err)
	}
	if _, err := h.jobs.Enqueue("v9"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// failCompleter/failEmbedder fail the test if they are called at all, which
	// is the assertion that no LLM tokens are spent on a music video.
	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(failCompleter{t: t}),
		Embedder:   failEmbedder{t: t},
		EmbedModel: "test-model",
		EmbedDim:   4})

	did, err := w.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if !did {
		t.Fatal("expected processOne to claim and process the job")
	}

	v, err := h.videos.Get("v9")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.SummaryStatus != "no_transcript" {
		t.Fatalf("expected summary_status=no_transcript, got %q", v.SummaryStatus)
	}
	if v.Summary != "" || v.Chapters != "" || v.KeyPoints != "" {
		t.Fatalf("expected the stale analysis cleared, got summary=%q chapters=%q key_points=%q",
			v.Summary, v.Chapters, v.KeyPoints)
	}

	// The assertion that matters: the embeddings must go even though the row the
	// worker read was already blank. Otherwise a phrase from the old summary
	// keeps returning this video in search, with nothing in the UI to show why.
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transcript_chunks WHERE video_id = ?`, "v9").Scan(&count); err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the stale chunks deleted, got %d", count)
	}
}

// TestWorkerNoSubtitleFileKeepsTheStoredSummary guards the other side of the
// cleanup: Tombstone() blanks subtitle_path, so a retention-swept video takes
// the "no subtitle file" path. That one must NOT discard the summary it was
// archived with — the whole point of keeping the row is the summary.
func TestWorkerNoSubtitleFileKeepsTheStoredSummary(t *testing.T) {
	h := newWorkerHarness(t)
	if err := h.videos.Upsert(videos.Video{ID: "v10", URL: "https://youtu.be/v10"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	if err := h.videos.SetSummary("v10", "The archived summary.", "", ""); err != nil {
		t.Fatalf("seed summary: %v", err)
	}
	if _, err := h.jobs.Enqueue("v10"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(failCompleter{t: t}),
		Embedder:   failEmbedder{t: t},
		EmbedModel: "test-model",
		EmbedDim:   4})
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}

	v, err := h.videos.Get("v10")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.Summary != "The archived summary." {
		t.Fatalf("expected the archived summary kept, got %q", v.Summary)
	}
}

// TestDiscardStaleAnalysisSurvivesWriteFailures asserts the cleanup stays
// best-effort. The paths that call it are terminal states, not errors, so a
// failing summary write (or a worker with no Rag store wired at all) must be
// logged and stepped over rather than turned into a job failure.
func TestDiscardStaleAnalysisSurvivesWriteFailures(t *testing.T) {
	h := newWorkerHarness(t)
	if err := h.videos.Upsert(videos.Video{ID: "v11", URL: "https://youtu.be/v11"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	if err := h.videos.SetSummary("v11", "stale", "", ""); err != nil {
		t.Fatalf("seed summary: %v", err)
	}
	if _, err := h.db.Exec(`CREATE TRIGGER no_summary BEFORE UPDATE OF summary ON videos
		BEGIN SELECT RAISE(ABORT, 'summary writes blocked'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	// Rag deliberately left nil: an embedding-less deployment must not panic here.
	w := NewWorker(WorkerDeps{Jobs: h.jobs, Videos: h.videos})
	w.discardStaleAnalysis(context.Background(), &videos.Video{ID: "v11", Summary: "stale"})

	v, err := h.videos.Get("v11")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.Summary != "stale" {
		t.Fatalf("expected the blocked write to leave the row alone, got %q", v.Summary)
	}
}

// TestDiscardStaleAnalysisSkipsTheRowWriteWhenNothingIsStored asserts the row
// write is skipped for an already-blank row — but the chunk delete still runs,
// because a blank summary column does not imply there are no chunks (see the
// handleReprocess note on discardStaleAnalysis).
func TestDiscardStaleAnalysisSkipsTheRowWriteWhenNothingIsStored(t *testing.T) {
	h := newWorkerHarness(t)
	if err := h.videos.Upsert(videos.Video{ID: "v12", URL: "https://youtu.be/v12"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	if err := h.rag.ReplaceVideoChunks(context.Background(), "v12", rag.IndexMeta{Model: "test-model", Dim: 1536, Rev: rag.ChunkRecipeRev},
		[]rag.ChunkRow{{Ordinal: 0, Text: "orphaned", Kind: "summary"}},
		[][]float32{make([]float32, 1536)}); err != nil {
		t.Fatalf("seed chunks: %v", err)
	}
	// Blocking the summary column proves the row write never happens: if it did,
	// the trigger would fire and the error would be logged instead of skipped.
	if _, err := h.db.Exec(`CREATE TRIGGER no_summary_v12 BEFORE UPDATE OF summary ON videos
		BEGIN SELECT RAISE(ABORT, 'summary writes blocked'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	w := NewWorker(WorkerDeps{Jobs: h.jobs, Videos: h.videos, Rag: h.rag})
	w.discardStaleAnalysis(context.Background(), &videos.Video{ID: "v12"})

	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM transcript_chunks WHERE video_id = ?`, "v12").Scan(&count); err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the orphaned chunks deleted even with a blank row, got %d", count)
	}
}

// TestWorkerEmptyTranscriptDiscardsStaleAnalysis covers the other caller: a
// subtitle file that parses to nothing at all (as opposed to music) gets the
// same cleanup as the non-speech case.
func TestWorkerEmptyTranscriptDiscardsStaleAnalysis(t *testing.T) {
	h := newWorkerHarness(t)

	relPath := "v13/captions.en.vtt"
	full := filepath.Join(h.mediaDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}
	if err := h.videos.Upsert(videos.Video{ID: "v13", URL: "https://youtu.be/v13"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	seedTranscript(t, h, "v13", relPath)
	if err := h.videos.SetSummary("v13", "stale text", "", ""); err != nil {
		t.Fatalf("seed summary: %v", err)
	}
	if _, err := h.jobs.Enqueue("v13"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(failCompleter{t: t}),
		Embedder:   failEmbedder{t: t},
		EmbedModel: "test-model",
		EmbedDim:   1536})
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}

	v, err := h.videos.Get("v13")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.SummaryStatus != "no_transcript" || v.Summary != "" {
		t.Fatalf("expected no_transcript with the stale summary cleared, got status=%q summary=%q",
			v.SummaryStatus, v.Summary)
	}
}

// chapterCompleter answers the key-points call with chapters, so the embedding
// step downstream has something to build chapter chunks from.
type chapterCompleter struct{}

func (chapterCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	if len(m) > 0 {
		sys := m[0].Content
		if strings.Contains(sys, "cohesive summary") {
			return "Overall prose summary.", nil
		}
		if strings.Contains(sys, "category id") {
			return "ai", nil
		}
		if strings.Contains(sys, "JSON") {
			return `{"chapters":[{"ts":0,"title":"Opening"},{"ts":2,"title":"Testing"}],` +
				`"key_points":[{"ts":0,"text":"a point"}]}`, nil
		}
	}
	return "chunk summary", nil
}

// keyPointsFailCompleter answers summary and classify but always fails the
// key-points call, which is the fragile step embedding now runs behind.
type keyPointsFailCompleter struct{}

func (keyPointsFailCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	if len(m) > 0 {
		sys := m[0].Content
		if strings.Contains(sys, "cohesive summary") {
			return "Overall prose summary.", nil
		}
		if strings.Contains(sys, "category id") {
			return "ai", nil
		}
		if strings.Contains(sys, "JSON") {
			return "", errors.New("key points endpoint exploded")
		}
	}
	return "chunk summary", nil
}

// seedChapterVideo writes a two-cue VTT and a video row pointing at it.
func seedChapterVideo(t *testing.T, h *workerHarness, id string) {
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
	if err := h.videos.Upsert(videos.Video{ID: id, URL: "https://youtu.be/" + id}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	seedTranscript(t, h, id, relPath)
	if _, err := h.jobs.Enqueue(id); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
}

func chunkKindCounts(t *testing.T, h *workerHarness, videoID string) map[string]int {
	t.Helper()
	rows, err := h.db.Query(`SELECT kind, COUNT(*) FROM transcript_chunks WHERE video_id=? GROUP BY kind`, videoID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			t.Fatal(err)
		}
		out[k] = n
	}
	return out
}

// Embedding moved after key points precisely so it can see the chapters that
// step writes. If it ran first, every video would be indexed as though it had
// no chapters at all.
func TestWorkerIndexesChaptersFromKeyPointsOutput(t *testing.T) {
	h := newWorkerHarness(t)
	seedChapterVideo(t, h, "vc1")

	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(chapterCompleter{}),
		Embedder:   fakeWorkerEmbedder{dim: 1536}, EmbedModel: "test-model", EmbedDim: 1536})
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}

	kinds := chunkKindCounts(t, h, "vc1")
	if kinds[rag.KindChapter] == 0 {
		t.Fatalf("no chapter chunks indexed: %v", kinds)
	}
	v, err := h.videos.Get("vc1")
	if err != nil {
		t.Fatal(err)
	}
	if v.EmbedRev != rag.ChunkRecipeRev {
		t.Errorf("embed_rev = %d, want %d", v.EmbedRev, rag.ChunkRecipeRev)
	}
}

// Embedding running behind the fragile step introduces a risk: a video whose
// key-points call keeps failing would never be indexed at all — unfindable,
// with nothing to say why. The fallback pass is what prevents that.
func TestWorkerStillIndexesWhenKeyPointsFails(t *testing.T) {
	h := newWorkerHarness(t)
	seedChapterVideo(t, h, "vc2")

	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(keyPointsFailCompleter{}),
		Embedder:   fakeWorkerEmbedder{dim: 1536}, EmbedModel: "test-model", EmbedDim: 1536})
	// A key-points failure requeues the job, which surfaces as an error from
	// processOne — that is the path under test, not a problem with it.
	if _, err := w.processOne(context.Background()); err == nil {
		t.Fatal("expected the key-points failure to be reported")
	}

	kinds := chunkKindCounts(t, h, "vc2")
	if kinds[rag.KindTranscript] == 0 {
		t.Fatalf("a key-points failure left the video unsearchable: %v", kinds)
	}
	// No chapters existed to index, so none should have been invented.
	if kinds[rag.KindChapter] != 0 {
		t.Errorf("chapter chunks = %d, want 0", kinds[rag.KindChapter])
	}
}

// SetKeyPoints zeroes embed_rev in the same statement that writes chapters, so
// a video indexed by the fallback above is re-indexed properly once key points
// eventually succeeds.
func TestSetKeyPointsInvalidatesTheIndex(t *testing.T) {
	h := newWorkerHarness(t)
	if err := h.videos.Upsert(videos.Video{ID: "vk", URL: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE videos SET embed_rev=? WHERE id='vk'`, rag.ChunkRecipeRev); err != nil {
		t.Fatal(err)
	}
	if err := h.videos.SetKeyPoints("vk", `[{"ts":0,"title":"New"}]`, `[]`); err != nil {
		t.Fatal(err)
	}
	v, err := h.videos.Get("vk")
	if err != nil {
		t.Fatal(err)
	}
	if v.EmbedRev != 0 {
		t.Errorf("embed_rev = %d after writing chapters, want 0 — the stored index predates them", v.EmbedRev)
	}
}

// The embedding step runs AFTER summary_status is persisted and emitted as
// done, so it must not emit "running". Player.tsx sets its local status from
// any non-done event, which would replace the summary the reader is looking at
// with the "Summarizing" spinner until embedding finished — a summary that
// appears, vanishes, and comes back.
func TestEmbeddingEmitsDoneNotRunning(t *testing.T) {
	h := newWorkerHarness(t)
	seedChapterVideo(t, h, "ve1")

	var events []string
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(chapterCompleter{}),
		Embedder:   fakeWorkerEmbedder{dim: 1536}, EmbedModel: "test-model", EmbedDim: 1536,
		OnPhase: func(id, status, phase string) {
			events = append(events, id+":"+status+":"+phase)
		}})
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatal(err)
	}

	if !contains(events, "ve1:done:embedding") {
		t.Errorf("embedding did not emit a done status: %v", events)
	}
	if contains(events, "ve1:running:embedding") {
		t.Errorf("embedding emitted running after the summary was already done: %v", events)
	}
	// The phase still rides along, so the Queue meter reaches step 4/4.
	var last string
	for _, e := range events {
		last = e
	}
	if last != "ve1:done:" {
		t.Errorf("last event = %q, want the terminal done with no phase", last)
	}
}

// Reprocess wipes the summary and clears embed_rev but does NOT delete chunks.
// embed_model is set once and never cleared, so gating the key-points-failure
// fallback on it alone would leave the OLD summary chunk indexed and served by
// search for as long as key points kept failing — with nothing left to repair
// it now the backfill is gone.
func TestFallbackReindexesAReprocessedVideoWithStaleRev(t *testing.T) {
	h := newWorkerHarness(t)
	seedChapterVideo(t, h, "vr1")

	// The state handleReprocess leaves behind: chunks from a previous run still
	// indexed, embed_model set, embed_rev cleared, summary wiped.
	if err := h.rag.ReplaceVideoChunks(context.Background(), "vr1",
		rag.IndexMeta{Model: "test-model", Dim: 1536, Rev: rag.ChunkRecipeRev},
		[]rag.ChunkRow{{Ordinal: 0, Text: "the OLD summary", Kind: rag.KindSummary}},
		[][]float32{make([]float32, 1536)}); err != nil {
		t.Fatal(err)
	}
	if err := h.videos.ClearEmbedRev("vr1"); err != nil {
		t.Fatal(err)
	}

	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(keyPointsFailCompleter{}),
		Embedder:   fakeWorkerEmbedder{dim: 1536}, EmbedModel: "test-model", EmbedDim: 1536})
	// Key points fails, so the fallback is the only thing that can re-index.
	if _, err := w.processOne(context.Background()); err == nil {
		t.Fatal("expected the key-points failure to be reported")
	}

	var stale int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM transcript_chunks WHERE video_id='vr1' AND text='the OLD summary'`,
	).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Error("the wiped summary is still indexed and would still be served by search")
	}
	if kinds := chunkKindCounts(t, h, "vr1"); kinds[rag.KindTranscript] == 0 {
		t.Errorf("the video was left unsearchable: %v", kinds)
	}
}

// sponsorSpyCompleter records every prompt the summarizer was given, so a test
// can assert on what the model READ rather than only on what it returned. It
// answers key points with a chapter and a point inside the sponsor read, which
// is the artifact the output backstop has to catch.
type sponsorSpyCompleter struct{ seen *[]string }

func (c sponsorSpyCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	for _, msg := range m {
		*c.seen = append(*c.seen, msg.Content)
	}
	if len(m) > 0 {
		sys := m[0].Content
		if strings.Contains(sys, "cohesive summary") {
			return "Overall prose summary.", nil
		}
		if strings.Contains(sys, "category id") {
			return "ai", nil
		}
		if strings.Contains(sys, "JSON") {
			return `{"chapters":[{"ts":8,"title":"The sponsor offer"},{"ts":20,"title":"Real topic"}],` +
				`"key_points":[{"ts":8,"text":"use code PEEQ"},{"ts":20,"text":"a real point"}]}`, nil
		}
	}
	return "chunk summary", nil
}

// A sponsor read must not reach the summarizer at all, and anything the model
// still times inside one must not be persisted. Both halves are checked here
// because they fail differently: the first cleans the prose summary, which no
// output filter could reach; the second catches a timestamp the model inferred
// rather than read.
func TestWorkerWithholdsSponsorSegmentsFromTheSummarizer(t *testing.T) {
	h := newWorkerHarness(t)

	relPath := "v9/captions.en.vtt"
	full := filepath.Join(h.mediaDir, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vtt := "WEBVTT\n\n" +
		"00:00:00.000 --> 00:00:04.000\nWelcome back to the channel today.\n\n" +
		"00:00:06.000 --> 00:00:10.000\nThis episode is sponsored by AcmeVPN use code PEEQ.\n\n" +
		"00:00:12.000 --> 00:00:16.000\nAcmeVPN keeps your traffic private and fast.\n\n" +
		"00:00:20.000 --> 00:00:24.000\nAnyway the actual topic is Go worker testing.\n"
	if err := os.WriteFile(full, []byte(vtt), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}

	if err := h.videos.Upsert(videos.Video{ID: "v9", URL: "https://youtu.be/v9"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	if err := h.videos.SetSponsorblockSegments("v9",
		`[{"category":"sponsor","start_time":5,"end_time":18}]`); err != nil {
		t.Fatalf("set segments: %v", err)
	}
	seedTranscript(t, h, "v9", relPath)
	if _, err := h.jobs.Enqueue("v9"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	var seen []string
	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(sponsorSpyCompleter{seen: &seen}),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model",
		EmbedDim:   1536})

	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("processOne: %v", err)
	}

	prompts := strings.Join(seen, "\n")
	if strings.Contains(prompts, "AcmeVPN") || strings.Contains(prompts, "code PEEQ") {
		t.Error("the sponsor read reached the summarizer; it must be stripped from the input")
	}
	if !strings.Contains(prompts, "actual topic is Go worker testing") {
		t.Fatal("real content was stripped too — the filter is too wide")
	}

	v, err := h.videos.Get("v9")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if strings.Contains(v.Chapters, "sponsor offer") || strings.Contains(v.KeyPoints, "code PEEQ") {
		t.Errorf("an artifact inside the sponsor read was persisted:\nchapters=%s\nkey_points=%s",
			v.Chapters, v.KeyPoints)
	}
	if !strings.Contains(v.Chapters, "Real topic") || !strings.Contains(v.KeyPoints, "a real point") {
		t.Errorf("the artifacts outside the sponsor read were dropped:\nchapters=%s\nkey_points=%s",
			v.Chapters, v.KeyPoints)
	}
}
