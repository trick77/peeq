package summarize

import (
	"context"
	"errors"
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

func (c *countingCompleter) Complete(_ context.Context, m []llm.Message) (string, error) {
	c.calls++
	// The classify step wants a bare category id, and NormalizeCategory would
	// turn prose into "uncategorized" — which is also what a skipped classify
	// leaves behind. Answering it properly keeps the two distinguishable. The
	// subcategory prompt is checked first: "subcategory ids" contains
	// "category id".
	switch {
	case strings.Contains(m[0].Content, "subcategory ids"):
		return `{"subcategories":["models"]}`, nil
	case strings.Contains(m[0].Content, "category id"):
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

// seedTranscript stores the .vtt at rel on the video's row, the way the
// download worker and the caption fetcher now do, and keeps writing
// subtitle_path so the tests still describe where the file came from.
//
// The source follows the path: a caption under .summaries/ was read to inform
// an inbox decision, anything else arrived with a download. That is exactly the
// rule migration 0027 turned from a path-prefix guess into a stored fact.
func seedTranscript(t *testing.T, h *workerHarness, id, rel string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.mediaDir, rel))
	if err != nil {
		t.Fatalf("read seeded vtt: %v", err)
	}
	source := videos.TranscriptSourceDownload
	if strings.HasPrefix(filepath.ToSlash(rel), ytdlp.SummaryDirName+"/") {
		source = videos.TranscriptSourceCaption
	}
	if err := h.videos.SetTranscript(id, source, string(data)); err != nil {
		t.Fatalf("set transcript: %v", err)
	}
	if err := h.videos.SetAudioLanguage(id, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}
}

// seedChannel writes a bare channels row with keep_reads set as given, and
// points the video at it. The flag reaches the worker through the join
// videoColumns already makes. A video with no channels row at all reads as on
// (0042), so a test about the off path seeds an off channel explicitly.
func seedChannel(t *testing.T, h *workerHarness, channelID, videoID string, keepReads bool) {
	t.Helper()
	if _, err := h.db.Exec(
		`INSERT INTO channels (id, keep_reads) VALUES (?, ?)
		 ON CONFLICT(id) DO UPDATE SET keep_reads = excluded.keep_reads`,
		channelID, keepReads); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE videos SET channel_id = ? WHERE id = ?`, channelID, videoID); err != nil {
		t.Fatalf("point video at channel: %v", err)
	}
}

// TestInboxVideoIsCategorizedButNotIndexed is the inbox read's cost line. A
// video peeq read to help decide whether to download it gets the prose (short
// and in depth) and its category and subcategories, so Search, MCP and the
// Inbox card can tell what it is about. Key points and, on a channel with
// keep_reads off, embeddings stay deferred: those are investments in a video
// the library keeps, and this one may still be ignored.
func TestInboxVideoIsCategorizedButNotIndexed(t *testing.T) {
	h := newWorkerHarness(t)
	rel := writeInboxCaption(t, h, "inbox1")

	if err := h.videos.Upsert(videos.Video{ID: "inbox1", URL: "https://youtu.be/inbox1"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedChannel(t, h, "UCoff", "inbox1", false)
	seedTranscript(t, h, "inbox1", rel)
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
		EmbedModel: "test-model", EmbedDim: 1536})
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
	if v.Category != "ai" {
		t.Fatalf("category = %q, want ai (inbox reads are classified)", v.Category)
	}
	subs, err := h.videos.SubcategoriesCtx(t.Context(), "inbox1")
	if err != nil {
		t.Fatalf("subcategories: %v", err)
	}
	if len(subs) != 1 || subs[0] != "ai/models" {
		t.Fatalf("subcategories = %v, want [ai/models]", subs)
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
	// Four calls: summary, classify, subcategories, in-depth. Anything more
	// means a deferred step ran anyway.
	if completer.calls != 4 {
		t.Fatalf("completer called %d times, want exactly 4 (summary + classify + subcategories + in-depth)", completer.calls)
	}
}

// failingClassifyCompleter fails the classify call and answers the rest.
type failingClassifyCompleter struct{ countingCompleter }

func (c *failingClassifyCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	if strings.Contains(m[0].Content, "category id") && !strings.Contains(m[0].Content, "subcategory ids") {
		c.calls++
		return "", errors.New("gate down")
	}
	return c.countingCompleter.Complete(ctx, m)
}

// statusAtClassify records the row's summary_status when the classify call
// arrives.
type statusAtClassify struct {
	countingCompleter
	read func() string
	seen []string
}

func (c *statusAtClassify) Complete(ctx context.Context, m []llm.Message) (string, error) {
	if strings.Contains(m[0].Content, "category id") && !strings.Contains(m[0].Content, "subcategory ids") {
		c.seen = append(c.seen, c.read())
	}
	return c.countingCompleter.Complete(ctx, m)
}

// The Inbox card offers the summary the moment it is saved: an inbox read is
// 'done' before the category call, never held at running behind a gate call.
func TestInboxVideo_isDoneBeforeItIsClassified(t *testing.T) {
	h := newWorkerHarness(t)
	rel := writeInboxCaption(t, h, "inbox-order")
	if err := h.videos.Upsert(videos.Video{ID: "inbox-order", URL: "https://youtu.be/inbox-order"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedChannel(t, h, "UCoff", "inbox-order", false)
	seedTranscript(t, h, "inbox-order", rel)
	if err := h.videos.SetStatus("inbox-order", videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, err := h.jobs.Enqueue("inbox-order"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	completer := &statusAtClassify{countingCompleter: countingCompleter{reply: "A summary of the video."}, read: func() string {
		v, err := h.videos.Get("inbox-order")
		if err != nil || v == nil {
			return "missing"
		}
		return v.SummaryStatus
	}}
	var phases []string
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(completer), Embedder: &countingEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536,
		OnPhase: func(_, status, phase string) { phases = append(phases, status+" "+phase) }})
	if _, err := w.processOne(t.Context()); err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if len(completer.seen) != 1 || completer.seen[0] != videos.SummaryDone {
		t.Fatalf("summary_status at classify = %v, want [done]", completer.seen)
	}
	for _, p := range phases {
		if strings.HasSuffix(p, PhaseClassifying) {
			t.Fatalf("phases = %v; an inbox read classifies after done, with no running phase", phases)
		}
	}
}

// A post-download pass whose summary is already 'done' (here: an inbox read
// whose classify failed) classifies without moving an open Player back to
// running.
func TestDownloadPassClassifiesWithoutRegressingTheSummary(t *testing.T) {
	h := newWorkerHarness(t)
	rel := writeInboxCaption(t, h, "inbox-dl")
	if err := h.videos.Upsert(videos.Video{ID: "inbox-dl", URL: "https://youtu.be/inbox-dl"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedChannel(t, h, "UCoff", "inbox-dl", false)
	seedTranscript(t, h, "inbox-dl", rel)
	if err := h.videos.SetStatus("inbox-dl", videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, err := h.jobs.Enqueue("inbox-dl"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	var phases []string
	deps := WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(&failingClassifyCompleter{countingCompleter{reply: "A summary of the video."}}),
		Embedder:   &countingEmbedder{dim: 1536}, EmbedModel: "test-model", EmbedDim: 1536,
		OnPhase: func(_, status, phase string) { phases = append(phases, status+" "+phase) }}
	if _, err := NewWorker(deps).processOne(t.Context()); err != nil {
		t.Fatalf("inbox pass: %v", err)
	}

	downloadedRel := filepath.Join("UC123", "inbox-dl", "inbox-dl.en.vtt")
	writeVTT(t, filepath.Join(h.mediaDir, downloadedRel))
	seedTranscript(t, h, "inbox-dl", downloadedRel)
	if err := h.videos.SetStatus("inbox-dl", videos.StatusDownloaded, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, err := h.jobs.Enqueue("inbox-dl"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	phases = nil
	deps.Summarizer = New(&countingCompleter{reply: `{"key_points":[{"ts":0,"text":"a point"}]}`})
	if _, err := NewWorker(deps).processOne(t.Context()); err != nil {
		t.Fatalf("download pass: %v", err)
	}
	for _, p := range phases {
		if strings.HasPrefix(p, videos.SummaryRunning+" ") {
			t.Fatalf("phases = %v; a done summary must not be emitted as running again", phases)
		}
	}
	v, err := h.videos.Get("inbox-dl")
	if err != nil || v == nil || v.Category != "ai" {
		t.Fatalf("video = %+v (err %v), want the download pass to classify it", v, err)
	}
}

// A failed classify must not fail the inbox read: the summary the card shows
// is done, and the idle sweep picks the category up later.
func TestInboxVideo_classifyFailureLeavesTheReadDone(t *testing.T) {
	h := newWorkerHarness(t)
	rel := writeInboxCaption(t, h, "inbox-cf")
	if err := h.videos.Upsert(videos.Video{ID: "inbox-cf", URL: "https://youtu.be/inbox-cf"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedChannel(t, h, "UCoff", "inbox-cf", false)
	seedTranscript(t, h, "inbox-cf", rel)
	if err := h.videos.SetStatus("inbox-cf", videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, err := h.jobs.Enqueue("inbox-cf"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	completer := &failingClassifyCompleter{countingCompleter{reply: "A summary of the video."}}
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(completer), Embedder: &countingEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536})
	if _, err := w.processOne(t.Context()); err != nil {
		t.Fatalf("processOne: %v", err)
	}
	v, err := h.videos.Get("inbox-cf")
	if err != nil || v == nil {
		t.Fatalf("get: %v", err)
	}
	if v.SummaryStatus != videos.SummaryDone {
		t.Fatalf("summary_status = %q, want done despite the classify failure", v.SummaryStatus)
	}
	if v.Category != videos.UncategorizedCategory {
		t.Fatalf("category = %q, want uncategorized for the sweep", v.Category)
	}
	got, err := h.videos.NextUnclassified(nil)
	if err != nil || got == nil || got.ID != "inbox-cf" {
		t.Fatalf("sweep offered %v (err %v), want inbox-cf", got, err)
	}
}

// flippingCompleter turns the channel's keep_reads off during the summary
// call: the switch-off purge has already run by the time the embed step comes.
type flippingCompleter struct {
	countingCompleter
	flip func()
}

func (c *flippingCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	if c.flip != nil {
		c.flip()
		c.flip = nil
	}
	return c.countingCompleter.Complete(ctx, m)
}

// A switch-off while an inbox read is being summarized must not be undone by
// the embed step that follows: the worker read keep_reads before the purge.
func TestInboxVideo_switchOffMidJobIsNotIndexed(t *testing.T) {
	h := newWorkerHarness(t)
	rel := writeInboxCaption(t, h, "inbox-flip")
	if err := h.videos.Upsert(videos.Video{ID: "inbox-flip", URL: "https://youtu.be/inbox-flip"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedTranscript(t, h, "inbox-flip", rel)
	if err := h.videos.SetStatus("inbox-flip", videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	seedChannel(t, h, "UCflip", "inbox-flip", true)
	if _, err := h.jobs.Enqueue("inbox-flip"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	completer := &flippingCompleter{countingCompleter: countingCompleter{reply: "A summary of the video."}, flip: func() {
		if _, err := h.db.Exec(`UPDATE channels SET keep_reads = 0 WHERE id = 'UCflip'`); err != nil {
			t.Errorf("flip: %v", err)
		}
	}}
	embedder := &countingEmbedder{dim: 1536}
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(completer), Embedder: embedder,
		EmbedModel: "test-model", EmbedDim: 1536})
	if _, err := w.processOne(t.Context()); err != nil {
		t.Fatalf("processOne: %v", err)
	}
	if indexed, err := h.rag.HasChunks(t.Context(), "inbox-flip"); err != nil || indexed {
		t.Fatalf("indexed = %v (err %v), want no chunks after a mid-job switch-off", indexed, err)
	}
}

// TestInboxVideoOnAKeepReadsChannelIsIndexed is what the channel setting buys.
// The same video on an opted-in channel is chunked and embedded as well as
// summarized and categorized, so it is findable in Search — and key points
// remain deferred.
func TestInboxVideoOnAKeepReadsChannelIsIndexed(t *testing.T) {
	h := newWorkerHarness(t)
	rel := writeInboxCaption(t, h, "inbox-keep")

	if err := h.videos.Upsert(videos.Video{ID: "inbox-keep", URL: "https://youtu.be/inbox-keep"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedTranscript(t, h, "inbox-keep", rel)
	if err := h.videos.SetStatus("inbox-keep", videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	seedChannel(t, h, "UCkeep", "inbox-keep", true)
	if _, err := h.jobs.Enqueue("inbox-keep"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	completer := &countingCompleter{reply: "A summary of the video."}
	embedder := &countingEmbedder{dim: 1536}
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(completer), Embedder: embedder,
		EmbedModel: "test-model", EmbedDim: 1536})
	if _, err := w.processOne(t.Context()); err != nil {
		t.Fatalf("processOne: %v", err)
	}

	v, err := h.videos.Get("inbox-keep")
	if err != nil || v == nil {
		t.Fatalf("get: %v", err)
	}
	if v.SummaryStatus != videos.SummaryDone {
		t.Fatalf("summary_status = %q, want done", v.SummaryStatus)
	}
	if v.EmbedModel == "" {
		t.Fatal("expected the read to be embedded on a keep_reads channel")
	}
	if indexed, herr := h.rag.HasChunks(t.Context(), "inbox-keep"); herr != nil || !indexed {
		t.Fatalf("no chunks written: indexed=%v (err %v)", indexed, herr)
	}
	if embedder.calls == 0 {
		t.Fatal("embedder never called")
	}
	// Categorized like every inbox read; key points stay deferred: keeping the
	// reading is not the same as treating the video as one the library holds.
	if v.Category != "ai" {
		t.Fatalf("category = %q, want ai", v.Category)
	}
	if v.KeyPoints != "" && v.KeyPoints != "[]" {
		t.Fatalf("key_points = %q, want them still deferred", v.KeyPoints)
	}
	if completer.calls != 4 {
		t.Fatalf("completer called %d times, want exactly 4 (summary + classify + subcategories + in-depth)", completer.calls)
	}
}

// TestDownloadingAnInboxVideoSkipsTheSummaryAndRunsTheRest is the other half of
// the promise, and the reason the feature is worth building: deciding to
// download must not pay for the expensive call a second time.
//
// It reproduces the real handover — the download repoints subtitle_path out of
// .summaries/ and into the media directory, and a fresh summary job is queued —
// then asserts the second pass spends nothing on prose or category and
// everything on the steps the inbox pass deferred.
func TestDownloadingAnInboxVideoSkipsTheSummaryAndRunsTheRest(t *testing.T) {
	h := newWorkerHarness(t)
	inboxRel := writeInboxCaption(t, h, "inbox2")

	if err := h.videos.Upsert(videos.Video{ID: "inbox2", URL: "https://youtu.be/inbox2"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedTranscript(t, h, "inbox2", inboxRel)
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
		EmbedModel: "test-model", EmbedDim: 1536})
	if _, err := w.processOne(t.Context()); err != nil {
		t.Fatalf("inbox pass: %v", err)
	}
	afterInbox := completer.calls

	// The download: captions now live with the media, and the status says so.
	downloadedRel := filepath.Join("UC123", "inbox2", "inbox2.en.vtt")
	writeVTT(t, filepath.Join(h.mediaDir, downloadedRel))
	seedTranscript(t, h, "inbox2", downloadedRel)
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
	// One call in the second pass — key points — and none for the summary or
	// the category the inbox read already paid for.
	if got := completer.calls - afterInbox; got != 1 {
		t.Fatalf("second pass made %d LLM calls, want 1 (key points only)", got)
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
	seedTranscript(t, h, "v-cancelled", rel)
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
		EmbedModel: "test-model", EmbedDim: 1536})
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

// TestIdleSweepsBackfillAnInboxRead is the backfill: an inbox read summarized
// before inbox reads were classified sits at 'new' with a summary, still
// 'uncategorized' and never subcategorized. With the queue empty, the idle
// turns classify it and then label it, with no migration.
func TestIdleSweepsBackfillAnInboxRead(t *testing.T) {
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

	completer := &countingCompleter{reply: "unused"}
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(completer), Embedder: &countingEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536})
	for range 4 {
		did, err := w.processOne(t.Context())
		if err != nil {
			t.Fatalf("processOne: %v", err)
		}
		if !did {
			break
		}
	}

	v, err := h.videos.Get("inbox3")
	if err != nil || v == nil {
		t.Fatalf("get: %v", err)
	}
	if v.Category != "ai" {
		t.Fatalf("category = %q, want the sweep to classify the inbox read", v.Category)
	}
	subs, err := h.videos.SubcategoriesCtx(t.Context(), "inbox3")
	if err != nil {
		t.Fatalf("subcategories: %v", err)
	}
	if len(subs) != 1 || subs[0] != "ai/models" {
		t.Fatalf("subcategories = %v, want [ai/models]", subs)
	}
	if completer.calls != 2 {
		t.Fatalf("completer called %d times, want 2 (classify + subcategories)", completer.calls)
	}
}
