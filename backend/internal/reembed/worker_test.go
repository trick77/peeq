package reembed

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/embedjobs"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/videos"
)

const vtt = `WEBVTT

00:00:00.000 --> 00:00:09.000
opening remarks before anything else

00:00:10.000 --> 00:00:19.000
sodium losses during long efforts

00:00:20.000 --> 00:00:29.000
potassium and muscle contraction
`

// musicVTT is auto-captions for a music video: [Music] markers plus stray lyric
// fragments, which subtitles.IsNonSpeech recognizes as not-speech.
const musicVTT = `WEBVTT

00:00:00.000 --> 00:00:20.000
[Music] I play games with

00:00:20.000 --> 00:00:40.000
[Music] you yeah

00:00:40.000 --> 00:01:00.000
[Music]

00:01:00.000 --> 00:01:20.000
[Music] [Applause]

00:01:20.000 --> 00:01:40.000
[Music] oh
`

type fakeEmbedder struct {
	calls  int
	inputs []string
	err    error
}

func (f *fakeEmbedder) EmbedBatched(_ context.Context, inputs []string, _ time.Duration) ([][]float32, error) {
	f.calls++
	f.inputs = append(f.inputs, inputs...)
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(inputs))
	for i := range inputs {
		v := make([]float32, 1536)
		v[0] = 1
		out[i] = v
	}
	return out, nil
}

type harness struct {
	db       *sql.DB
	jobs     *embedjobs.Store
	videos   *videos.Store
	rag      *rag.Store
	embedder *fakeEmbedder
	mediaDir string
	w        *Worker
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	mediaDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(mediaDir, "ch", "v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaDir, "ch", "v1", "s.vtt"), []byte(vtt), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &harness{
		db: db, jobs: embedjobs.New(db), videos: videos.New(db),
		rag: rag.NewStore(db), embedder: &fakeEmbedder{}, mediaDir: mediaDir,
	}
	h.w = New(Deps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag, Embedder: h.embedder,
		MediaDir: mediaDir, EmbedModel: "test-model", EmbedDim: 1536,
	})
	return h
}

// seed inserts a video already indexed under the old recipe.
func (h *harness) seed(t *testing.T, id, chaptersJSON string) {
	t.Helper()
	_, err := h.db.Exec(`
		INSERT INTO videos (id, url, status, subtitle_path, summary, chapters, embed_model, embed_dim, embed_rev)
		VALUES (?, 'u', 'downloaded', 'ch/v1/s.vtt', 'the prose summary', ?, 'test-model', 1536, 0)`,
		id, chaptersJSON)
	if err != nil {
		t.Fatal(err)
	}
}

func (h *harness) chunkKinds(t *testing.T, videoID string) map[string]int {
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

func (h *harness) embedRev(t *testing.T, videoID string) int {
	t.Helper()
	var rev int
	if err := h.db.QueryRow(`SELECT embed_rev FROM videos WHERE id=?`, videoID).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	return rev
}

// The defining property of this package: a rebuild needs no chat client at all.
// Deps has no field that could hold one, so this is really a compile-time
// guarantee — the test pins it so a future field addition is a deliberate act
// rather than an accident.
func TestDepsHasNoChatClient(t *testing.T) {
	ty := reflect.TypeOf(Deps{})
	for i := range ty.NumField() {
		name := strings.ToLower(ty.Field(i).Name)
		if strings.Contains(name, "summarizer") || strings.Contains(name, "chat") || strings.Contains(name, "llm") {
			t.Errorf("Deps.%s looks like a chat dependency; re-embed must never make chat calls", ty.Field(i).Name)
		}
	}
}

func TestRebuildAddsChapterChunksAndMarksCurrent(t *testing.T) {
	h := newHarness(t)
	h.seed(t, "v1", `[{"ts":10,"title":"Sodium"},{"ts":20,"title":"Potassium"}]`)
	if _, err := h.jobs.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}

	if worked := h.w.processOne(context.Background()); !worked {
		t.Fatal("processOne reported no work")
	}
	kinds := h.chunkKinds(t, "v1")
	if kinds[rag.KindChapter] != 2 {
		t.Errorf("chapter chunks = %d, want 2 (kinds: %v)", kinds[rag.KindChapter], kinds)
	}
	if kinds[rag.KindSummary] != 1 {
		t.Errorf("summary chunks = %d, want 1", kinds[rag.KindSummary])
	}
	if kinds[rag.KindTranscript] == 0 {
		t.Error("transcript chunks missing")
	}
	if got := h.embedRev(t, "v1"); got != rag.ChunkRecipeRev {
		t.Errorf("embed_rev = %d, want %d", got, rag.ChunkRecipeRev)
	}
	// The summary and chapters came from the DB, the transcript from the VTT —
	// nothing was regenerated.
	if h.embedder.calls != 1 {
		t.Errorf("embedder calls = %d, want 1", h.embedder.calls)
	}
}

// A video with no chapters still has to finish and reach the current rev, or
// the boot sweep re-queues it on every restart forever.
func TestRebuildWithoutChaptersStillTerminates(t *testing.T) {
	h := newHarness(t)
	h.seed(t, "v1", `[]`)
	if _, err := h.jobs.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	h.w.processOne(context.Background())

	if kinds := h.chunkKinds(t, "v1"); kinds[rag.KindChapter] != 0 {
		t.Errorf("chapter chunks = %d, want 0", kinds[rag.KindChapter])
	}
	if got := h.embedRev(t, "v1"); got != rag.ChunkRecipeRev {
		t.Fatalf("embed_rev = %d, want %d — a chapterless video must not loop forever", got, rag.ChunkRecipeRev)
	}
	if n, _ := h.jobs.EnqueueStale(rag.ChunkRecipeRev); n != 0 {
		t.Errorf("sweep re-queued %d videos after a successful rebuild", n)
	}
}

func TestRebuildIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.seed(t, "v1", `[{"ts":10,"title":"Sodium"}]`)
	if _, err := h.jobs.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	h.w.processOne(context.Background())
	first := h.chunkKinds(t, "v1")

	// Force a second pass over the same video.
	if _, err := h.db.Exec(`UPDATE videos SET embed_rev=0 WHERE id='v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.jobs.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	h.w.processOne(context.Background())

	if second := h.chunkKinds(t, "v1"); !reflect.DeepEqual(first, second) {
		t.Errorf("chunk counts changed on re-run: %v then %v", first, second)
	}
}

func TestEmbedFailureRequeuesWithoutMarkingCurrent(t *testing.T) {
	h := newHarness(t)
	h.seed(t, "v1", `[]`)
	h.embedder.err = errors.New("embeddings endpoint down")
	if _, err := h.jobs.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	h.w.processOne(context.Background())

	if got := h.embedRev(t, "v1"); got != 0 {
		t.Errorf("embed_rev = %d after a failed embed, want 0 — a failure must not claim success", got)
	}
	// Requeued rather than abandoned: the endpoint being down is transient.
	job, err := h.jobs.ClaimNext()
	if err != nil || job == nil {
		t.Fatalf("job should be retryable after an embed failure: %+v, %v", job, err)
	}
}

// A missing subtitle file means the stored chunks describe a transcript nothing
// can reproduce. Leaving them would keep serving search results built from text
// that no longer exists.
func TestMissingSubtitleDropsTheStaleIndex(t *testing.T) {
	h := newHarness(t)
	h.seed(t, "v1", `[]`)
	if err := h.rag.ReplaceVideoChunks(context.Background(), "v1",
		rag.IndexMeta{Model: "test-model", Dim: 1536, Rev: 1},
		[]rag.ChunkRow{{Ordinal: 0, Text: "stale text", Kind: rag.KindTranscript}},
		[][]float32{make([]float32, 1536)}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.mediaDir, "ch", "v1", "s.vtt")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.jobs.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	h.w.processOne(context.Background())

	if n := h.chunkKinds(t, "v1"); len(n) != 0 {
		t.Errorf("stale chunks survived a missing subtitle file: %v", n)
	}
	if h.embedder.calls != 0 {
		t.Errorf("embedder called %d times for a video with no transcript", h.embedder.calls)
	}
	if next, _ := h.jobs.ClaimNext(); next != nil {
		t.Error("an unrebuildable video should finish, not retry three times")
	}
}

// Music-only captions are not speech. The summarize worker refuses to index
// them and deletes whatever chunks they left behind — but it never clears
// embed_model, so a video indexed before that rule existed still matches the
// backfill sweep. Rebuilding it here would resurrect exactly the index that was
// deliberately thrown away.
func TestMusicOnlyVideoIsNotReindexed(t *testing.T) {
	h := newHarness(t)
	h.seed(t, "v1", `[]`)
	if err := os.WriteFile(filepath.Join(h.mediaDir, "ch", "v1", "s.vtt"), []byte(musicVTT), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.rag.ReplaceVideoChunks(context.Background(), "v1",
		rag.IndexMeta{Model: "test-model", Dim: 1536, Rev: 1},
		[]rag.ChunkRow{{Ordinal: 0, Text: "stale text", Kind: rag.KindTranscript}},
		[][]float32{make([]float32, 1536)}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.jobs.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	h.w.processOne(context.Background())

	if h.embedder.calls != 0 {
		t.Errorf("embedder called %d times for a music-only video", h.embedder.calls)
	}
	if n := h.chunkKinds(t, "v1"); len(n) != 0 {
		t.Errorf("music-only video was indexed: %v", n)
	}
	if next, _ := h.jobs.ClaimNext(); next != nil {
		t.Error("a music-only video should finish, not retry three times")
	}
}

func TestTombstonedVideoIsSkippedNotRetried(t *testing.T) {
	h := newHarness(t)
	h.seed(t, "v1", `[]`)
	if _, err := h.db.Exec(`UPDATE videos SET status='tombstoned' WHERE id='v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.jobs.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	h.w.processOne(context.Background())

	if h.embedder.calls != 0 {
		t.Errorf("embedder called %d times for a tombstoned video", h.embedder.calls)
	}
	if next, _ := h.jobs.ClaimNext(); next != nil {
		t.Error("a tombstoned video should finish, not retry")
	}
}

func TestProcessOneReportsNoWorkOnEmptyQueue(t *testing.T) {
	h := newHarness(t)
	if worked := h.w.processOne(context.Background()); worked {
		t.Error("processOne should report no work when the queue is empty")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.w.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

// Run must actually drain the queue, not just idle — the backfill depends on it
// working through every enqueued video without further prompting.
func TestRunDrainsTheQueue(t *testing.T) {
	h := newHarness(t)
	for _, id := range []string{"a", "b", "c"} {
		h.seed(t, id, `[{"ts":10,"title":"Sodium"}]`)
		if _, err := h.jobs.Enqueue(id); err != nil {
			t.Fatal(err)
		}
	}
	// Tight pacing so the test does not wait out the production delays.
	h.w = New(Deps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag, Embedder: h.embedder,
		MediaDir: h.mediaDir, EmbedModel: "test-model", EmbedDim: 1536,
		PollInterval: time.Millisecond, VideoDelay: time.Millisecond, BatchDelay: time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { h.w.Run(ctx); close(done) }()

	deadline := time.After(10 * time.Second)
	for {
		n, err := h.jobs.PendingCount()
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("queue still has %d jobs", n)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done

	for _, id := range []string{"a", "b", "c"} {
		if got := h.embedRev(t, id); got != rag.ChunkRecipeRev {
			t.Errorf("%s embed_rev = %d, want %d", id, got, rag.ChunkRecipeRev)
		}
	}
}

// embed_jobs.video_id cascades on delete, so a job cannot outlive its video —
// the nil guard in rebuild is defensive, not reachable from here.
func TestDeletingAVideoRemovesItsQueuedJob(t *testing.T) {
	h := newHarness(t)
	h.seed(t, "v1", `[]`)
	if _, err := h.jobs.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`DELETE FROM videos WHERE id='v1'`); err != nil {
		t.Fatal(err)
	}
	if job, err := h.jobs.ClaimNext(); err != nil || job != nil {
		t.Fatalf("claim after video delete = %+v, %v; want nil, nil", job, err)
	}
}

// A subtitle path that escapes the media directory must be refused outright
// rather than read — the stored path is not trusted input.
func TestUnsafeSubtitlePathIsRejected(t *testing.T) {
	h := newHarness(t)
	h.seed(t, "v1", `[]`)
	if _, err := h.db.Exec(`UPDATE videos SET subtitle_path='../../etc/passwd' WHERE id='v1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.jobs.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	h.w.processOne(context.Background())

	if h.embedder.calls != 0 {
		t.Errorf("embedder called %d times for an unsafe path", h.embedder.calls)
	}
	if got := h.embedRev(t, "v1"); got != 0 {
		t.Errorf("embed_rev = %d, want 0 — nothing was rebuilt", got)
	}
}

// A video whose transcript parses to nothing has no index to hold; the stored
// chunks are dropped rather than left describing text that is no longer there.
func TestEmptyTranscriptDropsTheIndex(t *testing.T) {
	h := newHarness(t)
	h.seed(t, "v1", `[]`)
	if err := os.WriteFile(filepath.Join(h.mediaDir, "ch", "v1", "s.vtt"), []byte("WEBVTT\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.jobs.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	h.w.processOne(context.Background())

	if h.embedder.calls != 0 {
		t.Errorf("embedder called %d times for an empty transcript", h.embedder.calls)
	}
	if next, _ := h.jobs.ClaimNext(); next != nil {
		t.Error("an empty transcript should finish the job, not retry")
	}
}

func TestNewAppliesPacingDefaults(t *testing.T) {
	w := New(Deps{})
	if w.d.PollInterval <= 0 || w.d.VideoDelay <= 0 || w.d.BatchDelay <= 0 {
		t.Errorf("pacing defaults missing: %+v", w.d)
	}
	if w.d.Logger == nil {
		t.Error("logger default missing")
	}
}
