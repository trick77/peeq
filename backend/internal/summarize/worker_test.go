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

func (f failEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	f.t.Fatal("Embedder.Embed should not be called for a no-transcript video")
	return nil, nil
}

// fakeWorkerCompleter dispatches canned replies by prompt content, mirroring
// summarizer_test.go's fakeCompleter.
type fakeWorkerCompleter struct{}

func (fakeWorkerCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	if len(m) > 0 {
		sys := m[0].Content
		if strings.Contains(sys, "Combine these section summaries") {
			return "Overall prose summary.", nil
		}
		if strings.Contains(sys, "JSON") {
			return `{"key_points":[{"ts":0,"text":"a point"}]}`, nil
		}
	}
	return "chunk summary", nil
}

// failingEmbedder always errors: used to prove processOne surfaces a
// non-nil error (via failJob) when embedding fails, instead of silently
// swallowing it (regression test for the silent summarize-worker error).
type failingEmbedder struct{}

func (failingEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	return nil, errors.New("boom")
}

// fakeWorkerEmbedder returns a dim-length vector per input.
type fakeWorkerEmbedder struct{ dim int }

func (f fakeWorkerEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
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
		db:       db,
		videos:   videos.New(db),
		jobs:     summaryjobs.New(db),
		rag:      rag.NewStore(db),
		mediaDir: t.TempDir(),
	}
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
		MediaDir:   h.mediaDir,
		EmbedModel: "test-model",
		EmbedDim:   4,
	})

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
	if err := h.videos.SetSubtitle("v2", relPath, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}
	if _, err := h.jobs.Enqueue("v2"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(fakeWorkerCompleter{}),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		MediaDir:   h.mediaDir,
		EmbedModel: "test-model",
		EmbedDim:   1536,
	})

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
	if err := h.videos.SetSubtitle("v4", relPath, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}
	if _, err := h.jobs.Enqueue("v4"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(fakeWorkerCompleter{}),
		Embedder:   failingEmbedder{},
		MediaDir:   h.mediaDir,
		EmbedModel: "test-model",
		EmbedDim:   1536,
	})

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
	if v.SummaryStatus != "error" {
		t.Errorf("summary_status = %q, want error", v.SummaryStatus)
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
	if err := h.videos.SetSubtitle("v5", relPath, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}
	if _, err := h.jobs.Enqueue("v5"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(fakeWorkerCompleter{}),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		MediaDir:   h.mediaDir,
		EmbedModel: "test-model",
		EmbedDim:   1536,
	})

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
	if err := h.videos.SetSubtitle("v3", relPath, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}
	if _, err := h.jobs.Enqueue("v3"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	w := NewWorker(WorkerDeps{
		Jobs:       h.jobs,
		Videos:     h.videos,
		Rag:        h.rag,
		Summarizer: New(fakeWorkerCompleter{}),
		Embedder:   fakeWorkerEmbedder{dim: 1536},
		MediaDir:   h.mediaDir,
		EmbedModel: "test-model",
		EmbedDim:   1536,
	})

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
