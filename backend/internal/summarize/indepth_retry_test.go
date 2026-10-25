package summarize

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/videos"
)

// retryClock is a settable WorkerDeps.Now.
type retryClock struct{ t time.Time }

func newRetryClock() *retryClock {
	return &retryClock{t: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)}
}
func (c *retryClock) now() time.Time                  { return c.t }
func (c *retryClock) advance(d time.Duration)         { c.t = c.t.Add(d) }
func (c *retryClock) after(d time.Duration) time.Time { return c.t.Add(d) }

// flakyEmbedder fails its first fail calls, then embeds like countingEmbedder.
type flakyEmbedder struct {
	countingEmbedder
	fail int
}

func (f *flakyEmbedder) EmbedBatched(ctx context.Context, inputs []string) ([][]float32, error) {
	if f.fail > 0 {
		f.fail--
		return nil, errors.New("embedder down")
	}
	return f.countingEmbedder.EmbedBatched(ctx, inputs)
}

type retryWorkerOpts struct {
	emb    Embedder
	phases *[]string
}

func newRetryWorker(h *workerHarness, c *inDepthCompleter, clock *retryClock, o retryWorkerOpts) *Worker {
	emb := o.emb
	if emb == nil {
		emb = &countingEmbedder{dim: 1536}
	}
	d := WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(c), Embedder: emb,
		EmbedModel: "test-model", EmbedDim: 1536,
		Now: clock.now,
	}
	if o.phases != nil {
		d.OnPhase = func(id, status, phase string) { *o.phases = append(*o.phases, id+" "+status+" "+phase) }
	}
	return NewWorker(d)
}

func mustProcess(t *testing.T, w *Worker) bool {
	t.Helper()
	did, err := w.processOne(t.Context())
	if err != nil {
		t.Fatalf("processOne: %v", err)
	}
	return did
}

func retryRow(t *testing.T, h *workerHarness, id string) (attempts int, nextAt, lastErr string, ok bool) {
	t.Helper()
	err := h.db.QueryRow(`SELECT attempts, next_at, last_error FROM in_depth_retry WHERE video_id = ?`, id).
		Scan(&attempts, &nextAt, &lastErr)
	if err != nil {
		return 0, "", "", false
	}
	return attempts, nextAt, lastErr, true
}

func sqlTime(t time.Time) string { return store.FormatTime(t) }

// The bug this exists for: a job whose in-depth call failed finished done and
// nothing ever asked again. Now the failure is queued with its cause, and an
// idle turn past the backoff writes the text, indexes its sections, tells an
// open page, and clears the queue. A restart in between changes nothing: the
// queue is in the database.
func TestInDepthRetry_aFailedJobStepIsRetriedUntilWritten(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	c := &inDepthCompleter{failInDepth: 1}
	clock := newRetryClock()

	mustProcess(t, newRetryWorker(h, c, clock, retryWorkerOpts{}))
	wantInDepth(t, h, "d1", "")
	attempts, nextAt, lastErr, ok := retryRow(t, h, "d1")
	if !ok || attempts != 1 || nextAt != sqlTime(clock.after(2*time.Minute)) || !strings.Contains(lastErr, "in-depth timeout") {
		t.Fatalf("retry row = %v attempts=%d next_at=%s last_error=%q; want queued, 1, +2m, the cause", ok, attempts, nextAt, lastErr)
	}

	// A fresh worker: the process restarted.
	var phases []string
	w := newRetryWorker(h, c, clock, retryWorkerOpts{phases: &phases})
	if mustProcess(t, w) {
		t.Fatal("retried before its backoff ran out")
	}
	clock.advance(2 * time.Minute)
	if !mustProcess(t, w) {
		t.Fatal("due retry did nothing")
	}
	wantInDepth(t, h, "d1", inDepthReply)
	if got := inDepthChunks(t, h, "d1"); len(got) != 1 {
		t.Fatalf("indepth chunks = %q, want the section indexed", got)
	}
	if _, _, _, ok := retryRow(t, h, "d1"); ok {
		t.Fatal("retry row left after success")
	}
	if len(phases) == 0 || phases[len(phases)-1] != "d1 done " {
		t.Fatalf("phases = %q, want a final done so an open page refetches", phases)
	}
	if mustProcess(t, w) {
		t.Fatal("did work with nothing queued")
	}
	if c.calls["indepth"] != 2 || c.calls["summary"] != 1 || c.calls["keypoints"] != 1 {
		t.Fatalf("calls = %v, want only the in-depth call repeated", c.calls)
	}
}

// Never gives up: the wait grows to a day and stays there, the row stays.
func TestInDepthRetry_backsOffAndNeverGivesUp(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	c := &inDepthCompleter{failInDepth: 1 << 30}
	clock := newRetryClock()
	w := newRetryWorker(h, c, clock, retryWorkerOpts{})
	mustProcess(t, w)

	for i, wait := range []time.Duration{2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour, 24 * time.Hour} {
		clock.advance(wait)
		if !mustProcess(t, w) {
			t.Fatalf("retry %d not due after %v", i+1, wait)
		}
	}
	attempts, nextAt, _, ok := retryRow(t, h, "d1")
	if !ok || attempts != 7 || nextAt != sqlTime(clock.after(24*time.Hour)) {
		t.Fatalf("retry row = %v attempts=%d next_at=%s; want kept, 7, +24h", ok, attempts, nextAt)
	}
	if c.calls["indepth"] != 7 {
		t.Fatalf("in-depth calls = %d, want 7", c.calls["indepth"])
	}
}

// Searchable: the retry adds only the new sections to an existing index. Every
// other chunk keeps its row and vector, so the embedder sees the section text
// alone and vec0 gains no dead chunks.
func TestInDepthRetry_indexesOnlyTheNewSections(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	c := &inDepthCompleter{failInDepth: 1}
	clock := newRetryClock()
	emb := &countingEmbedder{dim: 1536}
	w := newRetryWorker(h, c, clock, retryWorkerOpts{emb: emb})
	mustProcess(t, w)
	before := chunkIDs(t, h, "d1")

	emb.sent = nil
	clock.advance(2 * time.Minute)
	mustProcess(t, w)

	if len(emb.sent) != 1 || emb.sent["A point\n\nBody."] != 1 {
		t.Fatalf("embedded %v, want the one section only", emb.sent)
	}
	after := chunkIDs(t, h, "d1")
	for id := range before {
		if !after[id] {
			t.Fatalf("chunk %d replaced; want every existing chunk kept", id)
		}
	}
	if len(after) != len(before)+1 {
		t.Fatalf("%d chunks after, want %d", len(after), len(before)+1)
	}
}

func chunkIDs(t *testing.T, h *workerHarness, id string) map[int64]bool {
	t.Helper()
	rows, err := h.db.Query(`SELECT id FROM transcript_chunks WHERE video_id = ?`, id)
	if err != nil {
		t.Fatalf("query chunks: %v", err)
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[n] = true
	}
	return out
}

// The text is kept when only the index failed; the next turn embeds without
// paying for the chat call again.
func TestInDepthRetry_embedFailureRetriesOnlyTheEmbed(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	c := &inDepthCompleter{failInDepth: 1}
	clock := newRetryClock()
	emb := &flakyEmbedder{countingEmbedder: countingEmbedder{dim: 1536}}
	w := newRetryWorker(h, c, clock, retryWorkerOpts{emb: emb})
	mustProcess(t, w)

	emb.fail = 1
	clock.advance(2 * time.Minute)
	mustProcess(t, w)
	wantInDepth(t, h, "d1", inDepthReply)
	if attempts, _, lastErr, ok := retryRow(t, h, "d1"); !ok || attempts != 2 || !strings.Contains(lastErr, "embedder down") {
		t.Fatalf("retry row = %v attempts=%d last_error=%q; want kept, 2, the embed error", ok, attempts, lastErr)
	}

	clock.advance(10 * time.Minute)
	mustProcess(t, w)
	if c.calls["indepth"] != 2 {
		t.Fatalf("in-depth calls = %d, want no third call", c.calls["indepth"])
	}
	if got := inDepthChunks(t, h, "d1"); len(got) != 1 {
		t.Fatalf("indepth chunks = %q, want the section indexed", got)
	}
	if _, _, _, ok := retryRow(t, h, "d1"); ok {
		t.Fatal("retry row left after success")
	}
}

// Inbox reads: the text is written; an index is only touched where the
// channel keeps its reads, and never created where it does not.
func TestInDepthRetry_inboxReadIndexesOnlyAKeptRead(t *testing.T) {
	for _, keep := range []bool{false, true} {
		h := newWorkerHarness(t)
		rel := writeInboxCaption(t, h, "i1")
		if err := h.videos.Upsert(videos.Video{ID: "i1", URL: "https://youtu.be/i1"}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		seedTranscript(t, h, "i1", rel)
		if err := h.videos.SetStatus("i1", videos.StatusNew, ""); err != nil {
			t.Fatalf("set status: %v", err)
		}
		seedChannel(t, h, "UCx", "i1", keep)
		if _, err := h.jobs.Enqueue("i1"); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		c := &inDepthCompleter{failInDepth: 1}
		clock := newRetryClock()
		emb := &countingEmbedder{dim: 1536}
		w := newRetryWorker(h, c, clock, retryWorkerOpts{emb: emb})
		mustProcess(t, w)
		calls := emb.calls

		clock.advance(2 * time.Minute)
		mustProcess(t, w)
		wantInDepth(t, h, "i1", inDepthReply)
		chunks := inDepthChunks(t, h, "i1")
		if keep && len(chunks) != 1 {
			t.Fatalf("keep_reads: indepth chunks = %q, want the section indexed", chunks)
		}
		if !keep && (len(chunks) != 0 || emb.calls != calls) {
			t.Fatalf("no keep_reads: chunks=%q embed calls %d→%d, want no index", chunks, calls, emb.calls)
		}
		if _, _, _, ok := retryRow(t, h, "i1"); ok {
			t.Fatalf("keep=%v: retry row left after success", keep)
		}
	}
}

// A queued video that no longer has a summary to build on is dropped without
// a call: a re-analysis owns it now, and its own failure would queue it again.
func TestInDepthRetry_dropsAVideoWithNothingToBuildOn(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	c := &inDepthCompleter{failInDepth: 1}
	clock := newRetryClock()
	w := newRetryWorker(h, c, clock, retryWorkerOpts{})
	mustProcess(t, w)
	if err := h.videos.ClearSummary("d1"); err != nil {
		t.Fatalf("clear summary: %v", err)
	}

	clock.advance(2 * time.Minute)
	if mustProcess(t, w) {
		t.Fatal("reported work for a dropped retry")
	}
	if _, _, _, ok := retryRow(t, h, "d1"); ok {
		t.Fatal("retry row kept for a video without a summary")
	}
	if c.calls["indepth"] != 1 {
		t.Fatalf("in-depth calls = %d, want no retry call", c.calls["indepth"])
	}
}

// A real job wins over a due retry.
func TestInDepthRetry_queuedJobsGoFirst(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	c := &inDepthCompleter{failInDepth: 1}
	clock := newRetryClock()
	w := newRetryWorker(h, c, clock, retryWorkerOpts{})
	mustProcess(t, w)

	seedDownloaded(t, h, "d2")
	clock.advance(2 * time.Minute)
	mustProcess(t, w)
	if c.calls["summary"] != 2 || c.calls["indepth"] != 2 {
		t.Fatalf("calls = %v, want d2's job (its in-depth included) before d1's retry", c.calls)
	}
	if _, _, _, ok := retryRow(t, h, "d1"); !ok {
		t.Fatal("d1's retry ran before the queued job")
	}
	mustProcess(t, w)
	wantInDepth(t, h, "d1", inDepthReply)
}

// A queued video whose transcript is gone or holds nothing to summarize is
// dropped without a call: no retry could ever succeed on it.
func TestInDepthRetry_dropsAVideoWithNoUsableTranscript(t *testing.T) {
	for name, spoil := range map[string]string{
		"no transcript":    `DELETE FROM video_transcripts WHERE video_id = 'd1'`,
		"empty transcript": `UPDATE video_transcripts SET vtt = 'WEBVTT' || char(10) WHERE video_id = 'd1'`,
	} {
		t.Run(name, func(t *testing.T) {
			h := newWorkerHarness(t)
			seedDownloaded(t, h, "d1")
			c := &inDepthCompleter{failInDepth: 1}
			clock := newRetryClock()
			w := newRetryWorker(h, c, clock, retryWorkerOpts{})
			mustProcess(t, w)
			if _, err := h.db.Exec(spoil); err != nil {
				t.Fatalf("spoil: %v", err)
			}
			clock.advance(2 * time.Minute)
			if mustProcess(t, w) {
				t.Fatal("reported work for a dropped retry")
			}
			if _, _, _, ok := retryRow(t, h, "d1"); ok {
				t.Fatal("retry row kept")
			}
			if c.calls["indepth"] != 1 {
				t.Fatalf("in-depth calls = %d, want no retry call", c.calls["indepth"])
			}
		})
	}
}

// Nothing to add to the index: vectors from another model (the boot check owns
// that mismatch), a reply with no section to index, or sections already there.
// The text is still written where missing, and the row cleared.
func TestInDepthRetry_leavesTheIndexAloneWhenItHasNothingToAdd(t *testing.T) {
	t.Run("another model's index", func(t *testing.T) {
		h := newWorkerHarness(t)
		seedDownloaded(t, h, "d1")
		c := &inDepthCompleter{failInDepth: 1}
		clock := newRetryClock()
		mustProcess(t, newRetryWorker(h, c, clock, retryWorkerOpts{}))
		if _, err := h.db.Exec(`UPDATE videos SET embed_model = 'other-model' WHERE id = 'd1'`); err != nil {
			t.Fatalf("seed: %v", err)
		}
		emb := &countingEmbedder{dim: 1536}
		clock.advance(2 * time.Minute)
		mustProcess(t, newRetryWorker(h, c, clock, retryWorkerOpts{emb: emb}))
		wantInDepth(t, h, "d1", inDepthReply)
		if emb.calls != 0 {
			t.Fatalf("embed calls = %d, want none", emb.calls)
		}
	})
	t.Run("no section in the reply", func(t *testing.T) {
		h := newWorkerHarness(t)
		seedDownloaded(t, h, "d1")
		c := &inDepthCompleter{failInDepth: 1, reply: "Only a lead paragraph."}
		clock := newRetryClock()
		emb := &countingEmbedder{dim: 1536}
		w := newRetryWorker(h, c, clock, retryWorkerOpts{emb: emb})
		mustProcess(t, w)
		calls := emb.calls
		clock.advance(2 * time.Minute)
		mustProcess(t, w)
		wantInDepth(t, h, "d1", "Only a lead paragraph.")
		if emb.calls != calls {
			t.Fatalf("embed calls %d→%d, want none", calls, emb.calls)
		}
		if _, _, _, ok := retryRow(t, h, "d1"); ok {
			t.Fatal("retry row kept: a sectionless text must not retry forever")
		}
	})
	t.Run("sections already indexed", func(t *testing.T) {
		h := newWorkerHarness(t)
		seedDownloaded(t, h, "d1")
		c := &inDepthCompleter{}
		clock := newRetryClock()
		w := newRetryWorker(h, c, clock, retryWorkerOpts{})
		mustProcess(t, w)
		if err := h.videos.DeferInDepthRetry("d1", "", 1, clock.now()); err != nil {
			t.Fatalf("queue: %v", err)
		}
		if mustProcess(t, w) {
			t.Fatal("made a call for a video with nothing missing")
		}
		if _, _, _, ok := retryRow(t, h, "d1"); ok {
			t.Fatal("retry row kept")
		}
	})
}

// The database failing under the retry keeps the row: the next turn asks again.
func TestInDepthRetry_storeErrorsKeepTheRow(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	c := &inDepthCompleter{failInDepth: 1}
	clock := newRetryClock()
	w := newRetryWorker(h, c, clock, retryWorkerOpts{})
	mustProcess(t, w)
	if _, err := h.db.Exec(`ALTER TABLE video_in_depth RENAME TO video_in_depth_gone`); err != nil {
		t.Fatalf("break: %v", err)
	}
	clock.advance(2 * time.Minute)
	if mustProcess(t, w) {
		t.Fatal("reported work on a store error")
	}
	if attempts, _, lastErr, ok := retryRow(t, h, "d1"); !ok || attempts != 2 || lastErr == "" {
		t.Fatalf("retry row = %v attempts=%d last_error=%q; want kept, 2, the error", ok, attempts, lastErr)
	}
}

// hookCompleter runs hook on the in-depth call instead of answering it.
type hookCompleter struct {
	inDepthCompleter
	hook func(ctx context.Context) (string, error)
}

func (c *hookCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	if c.hook != nil && strings.Contains(promptText(m), "in-depth summary") {
		return c.hook(ctx)
	}
	return c.inDepthCompleter.Complete(ctx, m)
}

func newHookWorker(h *workerHarness, c *hookCompleter, clock *retryClock, emb Embedder) *Worker {
	if emb == nil {
		emb = &countingEmbedder{dim: 1536}
	}
	return NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(c), Embedder: emb,
		EmbedModel: "test-model", EmbedDim: 1536, Now: clock.now})
}

// A shutdown mid-call is not a failure: no attempt burned, no backoff added.
func TestInDepthRetry_shutdownMidCallKeepsTheRowAsItWas(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	clock := newRetryClock()
	c := &hookCompleter{inDepthCompleter: inDepthCompleter{failInDepth: 1}}
	w := newHookWorker(h, c, clock, nil)
	mustProcess(t, w)
	_, nextBefore, _, _ := retryRow(t, h, "d1")

	ctx, cancel := context.WithCancel(t.Context())
	c.hook = func(context.Context) (string, error) { cancel(); return "", ctx.Err() }
	clock.advance(2 * time.Minute)
	if _, err := w.processOne(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("processOne: %v, want the shutdown reported", err)
	}
	if attempts, nextAt, _, ok := retryRow(t, h, "d1"); !ok || attempts != 1 || nextAt != nextBefore {
		t.Fatalf("retry row = %v attempts=%d next_at=%s; want untouched (1, %s)", ok, attempts, nextAt, nextBefore)
	}
}

// A panic in the retry must not kill the worker: the job path recovers its
// own, and a persisted due row would otherwise crash every boot.
func TestInDepthRetry_aPanicIsRecoveredAndBackedOff(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	clock := newRetryClock()
	c := &hookCompleter{inDepthCompleter: inDepthCompleter{failInDepth: 1}}
	w := newHookWorker(h, c, clock, nil)
	mustProcess(t, w)

	c.hook = func(context.Context) (string, error) { panic("boom") }
	clock.advance(2 * time.Minute)
	mustProcess(t, w)
	attempts, nextAt, lastErr, ok := retryRow(t, h, "d1")
	if !ok || attempts != 2 || nextAt != sqlTime(clock.after(10*time.Minute)) || !strings.Contains(lastErr, "boom") {
		t.Fatalf("retry row = %v attempts=%d next_at=%s last_error=%q; want 2, +10m, the panic", ok, attempts, nextAt, lastErr)
	}
}

// A requeued job (its summary already stored) whose in-depth call fails again
// keeps the retry's count and backoff instead of starting over at attempt 1.
func TestInDepthRetry_aJobFailureKeepsTheCount(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	clock := newRetryClock()
	if err := h.videos.SetSummaryText("d1", "Overall prose summary."); err != nil {
		t.Fatalf("summary: %v", err)
	}
	if err := h.videos.DeferInDepthRetry("d1", "earlier", 4, clock.after(time.Hour)); err != nil {
		t.Fatalf("queue: %v", err)
	}
	mustProcess(t, newRetryWorker(h, &inDepthCompleter{failInDepth: 1}, clock, retryWorkerOpts{}))
	if attempts, nextAt, _, ok := retryRow(t, h, "d1"); !ok || attempts != 5 || nextAt != sqlTime(clock.after(24*time.Hour)) {
		t.Fatalf("retry row = %v attempts=%d next_at=%s; want 5, +24h", ok, attempts, nextAt)
	}
}

// hookEmbedder runs hook before embedding, once.
type hookEmbedder struct {
	countingEmbedder
	hook func()
}

func (e *hookEmbedder) EmbedBatched(ctx context.Context, inputs []string) ([][]float32, error) {
	if e.hook != nil {
		e.hook()
		e.hook = nil
	}
	return e.countingEmbedder.EmbedBatched(ctx, inputs)
}

// keep_reads switched off while the retry's embed is in flight: the switch-off
// purge has run, so the index the embed writes back must go again.
func TestInDepthRetry_keepReadsSwitchedOffMidEmbedDropsTheIndex(t *testing.T) {
	h := newWorkerHarness(t)
	rel := writeInboxCaption(t, h, "i1")
	if err := h.videos.Upsert(videos.Video{ID: "i1", URL: "https://youtu.be/i1"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedTranscript(t, h, "i1", rel)
	if err := h.videos.SetStatus("i1", videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	seedChannel(t, h, "UCx", "i1", true)
	if _, err := h.jobs.Enqueue("i1"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	clock := newRetryClock()
	emb := &hookEmbedder{countingEmbedder: countingEmbedder{dim: 1536}}
	w := newRetryWorker(h, &inDepthCompleter{failInDepth: 1}, clock, retryWorkerOpts{emb: emb})
	mustProcess(t, w)

	emb.hook = func() {
		if _, err := h.db.Exec(`UPDATE channels SET keep_reads = 0 WHERE id = 'UCx'`); err != nil {
			t.Errorf("switch off: %v", err)
		}
	}
	clock.advance(2 * time.Minute)
	mustProcess(t, w)
	if indexed, err := h.rag.HasChunks(t.Context(), "i1"); err != nil || indexed {
		t.Fatalf("indexed=%v (err %v), want the index dropped after the switch-off", indexed, err)
	}
	wantInDepth(t, h, "i1", inDepthReply)
}

// A reprocess clicked during the retry's chat call owns the video: the old
// analysis's text must not land (the new job would skip its own step for
// good), and the index must not be rebuilt from the stale row.
func TestInDepthRetry_reprocessDuringTheCallWins(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	clock := newRetryClock()
	c := &hookCompleter{inDepthCompleter: inDepthCompleter{failInDepth: 1}}
	emb := &countingEmbedder{dim: 1536}
	w := newHookWorker(h, c, clock, emb)
	mustProcess(t, w)
	calls := emb.calls

	c.hook = func(context.Context) (string, error) {
		if _, err := h.videos.ResetAndEnqueueSummary("d1"); err != nil {
			t.Errorf("reprocess: %v", err)
		}
		return inDepthReply, nil
	}
	clock.advance(2 * time.Minute)
	mustProcess(t, w)
	wantInDepth(t, h, "d1", "")
	if emb.calls != calls {
		t.Fatalf("embed calls %d→%d, want no index rebuilt from the stale row", calls, emb.calls)
	}
	if v, err := h.videos.Get("d1"); err != nil || v.EmbedRev != 0 {
		t.Fatalf("embed_rev = %d, %v; want the reprocess's 0 kept", v.EmbedRev, err)
	}
}

// A shutdown during the job's own in-depth call queues nothing: the orphaned
// job runs the step again at the next boot.
func TestInDepthRetry_shutdownDuringTheJobStepQueuesNothing(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	ctx, cancel := context.WithCancel(t.Context())
	c := &hookCompleter{hook: func(context.Context) (string, error) { cancel(); return "", ctx.Err() }}
	_, _ = newHookWorker(h, c, newRetryClock(), nil).processOne(ctx)
	if _, _, _, ok := retryRow(t, h, "d1"); ok {
		t.Fatal("a shutdown queued a retry")
	}
}

func TestInDepthRetry_dropsAnIgnoredRead(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	c := &inDepthCompleter{failInDepth: 1}
	clock := newRetryClock()
	w := newRetryWorker(h, c, clock, retryWorkerOpts{})
	mustProcess(t, w)
	seedChannel(t, h, "UCx", "d1", true)
	if _, err := h.db.Exec(`INSERT INTO channel_videos (video_id, channel_id, state) VALUES ('d1', 'UCx', 'ignored')`); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	clock.advance(2 * time.Minute)
	if mustProcess(t, w) {
		t.Fatal("made a call for an ignored read")
	}
	if _, _, _, ok := retryRow(t, h, "d1"); ok {
		t.Fatal("retry row kept for an ignored read")
	}
}

// A retry that cannot be re-queued keeps a past-due row; the worker holds
// retries off for the backoff instead of repeating the call every turn.
func TestInDepthRetry_aFailedRequeuePausesRetries(t *testing.T) {
	h := newWorkerHarness(t)
	seedDownloaded(t, h, "d1")
	c := &inDepthCompleter{failInDepth: 2}
	clock := newRetryClock()
	w := newRetryWorker(h, c, clock, retryWorkerOpts{})
	mustProcess(t, w)
	if _, err := h.db.Exec(`CREATE TRIGGER full_disk BEFORE UPDATE ON in_depth_retry BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	clock.advance(2 * time.Minute)
	mustProcess(t, w)
	if mustProcess(t, w) || c.calls["indepth"] != 2 {
		t.Fatalf("in-depth calls = %d, want the retry held off after the failed requeue", c.calls["indepth"])
	}
	if _, err := h.db.Exec(`DROP TRIGGER full_disk`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	clock.advance(10 * time.Minute)
	mustProcess(t, w)
	wantInDepth(t, h, "d1", inDepthReply)
}
