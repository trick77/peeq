package summarize

import (
	"context"
	"database/sql"
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
