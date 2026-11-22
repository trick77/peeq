package captionrefetch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

type harness struct {
	db       *sql.DB
	videos   *videos.Store
	summary  *summaryjobs.Store
	mediaDir string
	events   *recorder
}

// newHarness builds a migrated database holding one downloaded video that has
// no transcript: the only state this service is ever started from.
func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	h := &harness{
		db: db, videos: videos.New(db), summary: summaryjobs.New(db),
		mediaDir: t.TempDir(), events: &recorder{},
	}
	if err := h.videos.Upsert(videos.Video{ID: "v1", URL: "https://youtu.be/v1", Title: "A video"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := h.videos.SetDownloaded("v1", videos.DownloadedResult{MediaPath: "UC1/v1/v1.mp4"}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
	}
	if err := h.videos.SetSummaryStatus("v1", videos.SummaryNoTranscript, ""); err != nil {
		t.Fatalf("seed no_transcript: %v", err)
	}
	return h
}

// recorder is an activity.Recorder that keeps what it was given.
type recorder struct {
	mu     sync.Mutex
	events []activity.Event
}

func (r *recorder) Record(e activity.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) all() []activity.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]activity.Event(nil), r.events...)
}

// fetcher is a scripted Fetcher answering every call the same way.
type fetcher struct {
	rel, lang string
	err       error
	// onCall, when set, runs inside the fetch with its ctx: a test changes the
	// world (or blocks) from there.
	onCall func(context.Context)

	mu        sync.Mutex
	calls     int
	prefer    string
	anyManual bool
}

func (f *fetcher) Subtitles(ctx context.Context, _, _, prefer string, anyManual bool) (string, string, error) {
	f.mu.Lock()
	f.calls++
	f.prefer, f.anyManual = prefer, anyManual
	f.mu.Unlock()
	if f.onCall != nil {
		f.onCall(ctx)
	}
	return f.rel, f.lang, f.err
}

// run starts a fetch for v1 and waits for it to settle.
func (h *harness) run(t *testing.T, f *fetcher) *Service {
	t.Helper()
	done := make(chan string, 1)
	s := New(Deps{
		Fetcher: f, Videos: h.videos, MediaDir: h.mediaDir,
		DefaultSubLang: "en", Activity: h.events, Background: t.Context(),
		OnDone: func(id string) { done <- id },
	})
	if !s.Start("v1", "https://youtu.be/v1") {
		t.Fatal("Start refused an idle video")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch did not settle")
	}
	return s
}

func writeCaption(t *testing.T, h *harness, rel string) string {
	t.Helper()
	full := filepath.Join(h.mediaDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte("WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nhallo\n"), 0o644); err != nil {
		t.Fatalf("write caption: %v", err)
	}
	return full
}

func (h *harness) activeJobs(t *testing.T) int {
	t.Helper()
	active, err := h.summary.ListActive()
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	return len(active)
}

// onlyEvent asserts exactly one Activity event and returns it.
func (h *harness) onlyEvent(t *testing.T) activity.Event {
	t.Helper()
	got := h.events.all()
	if len(got) != 1 {
		t.Fatalf("activity events = %+v, want exactly one", got)
	}
	if got[0].Kind != activity.KindDownload || got[0].SubjectID != "v1" || got[0].Subject != "A video" {
		t.Fatalf("event = %+v, want a download event about v1", got[0])
	}
	return got[0]
}

// assertUnchanged asserts the video is exactly as newHarness left it.
func (h *harness) assertUnchanged(t *testing.T) {
	t.Helper()
	v, err := h.videos.Get("v1")
	if err != nil || v == nil {
		t.Fatalf("get v1: %v, %v", v, err)
	}
	if v.HasTranscript {
		t.Fatal("a transcript was stored")
	}
	if v.SummaryStatus != videos.SummaryNoTranscript {
		t.Fatalf("summary_status = %q, want no_transcript", v.SummaryStatus)
	}
	if n := h.activeJobs(t); n != 0 {
		t.Fatalf("active summary jobs = %d, want 0", n)
	}
}

// TestFetchedCaptionsStoredAndReanalyzed is the happy path: the track lands as
// a download transcript (a library video), the spoken language is recorded,
// the file is gone and the analysis is queued like a Reprocess.
func TestFetchedCaptionsStoredAndReanalyzed(t *testing.T) {
	h := newHarness(t)
	rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.de.vtt")
	full := writeCaption(t, h, rel)
	f := &fetcher{rel: rel, lang: "de"}

	h.run(t, f)

	if f.prefer != "en" || !f.anyManual {
		t.Fatalf("Subtitles(prefer=%q, anyManual=%v), want en/true", f.prefer, f.anyManual)
	}
	tr, err := h.videos.GetTranscript("v1")
	if err != nil || tr == nil {
		t.Fatalf("transcript not stored: %v, %v", tr, err)
	}
	if tr.Source != videos.TranscriptSourceDownload {
		t.Fatalf("source = %q, want download", tr.Source)
	}
	v, _ := h.videos.Get("v1")
	if v.AudioLanguage != "de" {
		t.Fatalf("audio_language = %q, want de", v.AudioLanguage)
	}
	if v.SummaryStatus != videos.SummaryPending {
		t.Fatalf("summary_status = %q, want pending", v.SummaryStatus)
	}
	if n := h.activeJobs(t); n != 1 {
		t.Fatalf("active summary jobs = %d, want 1", n)
	}
	if _, err := os.Stat(full); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("caption file still on disk: %v", err)
	}
	if e := h.onlyEvent(t); e.Outcome != activity.OutcomeOK {
		t.Fatalf("outcome = %q, want ok", e.Outcome)
	}
}

// TestNoLanguageKeepsAudioLanguage: a track without a spoken language must not
// blank the one the download recorded.
func TestNoLanguageKeepsAudioLanguage(t *testing.T) {
	h := newHarness(t)
	if err := h.videos.SetAudioLanguage("v1", "fr"); err != nil {
		t.Fatalf("seed audio language: %v", err)
	}
	rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.fr.vtt")
	writeCaption(t, h, rel)

	h.run(t, &fetcher{rel: rel})

	v, _ := h.videos.Get("v1")
	if v.AudioLanguage != "fr" {
		t.Fatalf("audio_language = %q, want fr kept", v.AudioLanguage)
	}
	if !v.HasTranscript {
		t.Fatal("transcript not stored")
	}
}

// TestNoTrackLeavesVideo: YouTube has no captions; the video stays
// no_transcript and the user learns why from the Activity feed.
func TestNoTrackLeavesVideo(t *testing.T) {
	h := newHarness(t)

	h.run(t, &fetcher{})

	h.assertUnchanged(t)
	if e := h.onlyEvent(t); e.Outcome != activity.OutcomeWarn {
		t.Fatalf("outcome = %q, want warn", e.Outcome)
	}
}

// TestRefusedLeavesVideo: paused or cookie-less peeq never reached YouTube.
func TestRefusedLeavesVideo(t *testing.T) {
	h := newHarness(t)

	h.run(t, &fetcher{err: &ytdlp.RefusedError{Err: ytdlp.ErrNoCookie}})

	h.assertUnchanged(t)
	if e := h.onlyEvent(t); e.Outcome != activity.OutcomeWarn {
		t.Fatalf("outcome = %q, want warn", e.Outcome)
	}
}

// TestHostRefusedIsRefusal: YouTube refusing this server applies to every
// call, like a pause; not this video failing.
func TestHostRefusedIsRefusal(t *testing.T) {
	h := newHarness(t)

	h.run(t, &fetcher{err: fmt.Errorf("list captions: %w", ytdlp.ErrHostRefused)})

	h.assertUnchanged(t)
	if e := h.onlyEvent(t); e.Outcome != activity.OutcomeWarn || e.Summary != "caption fetch refused" {
		t.Fatalf("event = %+v, want a warn refusal", e)
	}
}

// TestRateLimitedSaysTryLater: a 429/5xx is YouTube saying "not now", not a
// verdict on the video.
func TestRateLimitedSaysTryLater(t *testing.T) {
	h := newHarness(t)

	h.run(t, &fetcher{err: &ytdlp.RetryableError{Reason: "429"}})

	h.assertUnchanged(t)
	e := h.onlyEvent(t)
	if e.Outcome != activity.OutcomeWarn {
		t.Fatalf("outcome = %q, want warn", e.Outcome)
	}
	if !strings.Contains(e.Detail, "try again later") {
		t.Fatalf("detail = %q, want retry-later wording", e.Detail)
	}
}

// TestFailureRecordedRedacted: a failed call leaves the video and records the
// failure, its text redacted on the way out of the logs.
func TestFailureRecordedRedacted(t *testing.T) {
	h := newHarness(t)

	h.run(t, &fetcher{err: errors.New("fetch https://www.youtube.com/watch?v=v1&token=secret123 failed")})

	h.assertUnchanged(t)
	e := h.onlyEvent(t)
	if e.Outcome != activity.OutcomeFail {
		t.Fatalf("outcome = %q, want fail", e.Outcome)
	}
	if e.Detail == "" {
		t.Fatal("failure detail empty")
	}
	if want := "secret123"; strings.Contains(e.Detail, want) {
		t.Fatalf("detail %q leaks %q", e.Detail, want)
	}
}

// TestShutdownRecordsNothing: a fetch cut short by shutdown is not the video's
// failure; nothing is written, nothing recorded.
func TestShutdownRecordsNothing(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan string, 1)
	f := &fetcher{err: context.Canceled, onCall: func(context.Context) { cancel() }}
	s := New(Deps{
		Fetcher: f, Videos: h.videos, MediaDir: h.mediaDir,
		Activity: h.events, Background: ctx,
		OnDone: func(id string) { done <- id },
	})
	if !s.Start("v1", "https://youtu.be/v1") {
		t.Fatal("Start refused")
	}
	<-done

	h.assertUnchanged(t)
	if got := h.events.all(); len(got) != 0 {
		t.Fatalf("events = %+v, want none on shutdown", got)
	}
}

// TestDownloadStartedDuringFetchKeepsRow: the fetch waited on the YouTube
// queue; a re-download queued meanwhile owns the transcript now.
func TestDownloadStartedDuringFetchKeepsRow(t *testing.T) {
	h := newHarness(t)
	rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.en.vtt")
	full := writeCaption(t, h, rel)
	f := &fetcher{rel: rel, lang: "en", onCall: func(context.Context) {
		if err := h.videos.SetStatus("v1", videos.StatusQueued, ""); err != nil {
			t.Errorf("queue v1: %v", err)
		}
	}}

	h.run(t, f)

	h.assertUnchanged(t)
	if _, err := os.Stat(full); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("caption file left behind: %v", err)
	}
	if got := h.events.all(); len(got) != 0 {
		t.Fatalf("events = %+v, want none for a superseded fetch", got)
	}
}

// TestSummaryQueuedDuringFetchAddsNoJob: an analysis queued while the fetch
// waited (a download that landed without captions) must not get a twin.
func TestSummaryQueuedDuringFetchAddsNoJob(t *testing.T) {
	h := newHarness(t)
	rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.en.vtt")
	writeCaption(t, h, rel)
	f := &fetcher{rel: rel, lang: "en", onCall: func(context.Context) {
		if _, err := h.summary.Enqueue("v1"); err != nil {
			t.Errorf("enqueue: %v", err)
		}
	}}

	h.run(t, f)

	if n := h.activeJobs(t); n != 1 {
		t.Fatalf("active summary jobs = %d, want the one queued meanwhile", n)
	}
	if v, _ := h.videos.Get("v1"); v.HasTranscript {
		t.Fatal("a transcript was stored")
	}
	if got := h.events.all(); len(got) != 0 {
		t.Fatalf("events = %+v, want none for a superseded fetch", got)
	}
}

// TestManualCategorySurvives: the user asked for captions, not a new category.
func TestManualCategorySurvives(t *testing.T) {
	h := newHarness(t)
	if err := h.videos.SetCategory("v1", "gaming"); err != nil {
		t.Fatalf("set category: %v", err)
	}
	rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.en.vtt")
	writeCaption(t, h, rel)

	h.run(t, &fetcher{rel: rel, lang: "en"})

	v, _ := h.videos.Get("v1")
	if !v.HasTranscript || v.Category != "gaming" {
		t.Fatalf("has_transcript=%v category=%q, want true/gaming", v.HasTranscript, v.Category)
	}
}

// TestSharedDirSurvives: .summaries/<id> is the Inbox caption worker's too.
// Only the file this fetch returned goes; anything else in it stays.
func TestSharedDirSurvives(t *testing.T) {
	h := newHarness(t)
	rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.en.vtt")
	full := writeCaption(t, h, rel)
	other := writeCaption(t, h, filepath.Join(ytdlp.SummaryDirName, "v1", "captions.info.json"))

	h.run(t, &fetcher{rel: rel, lang: "en"})

	if _, err := os.Stat(full); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fetched caption left behind: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another file in the shared dir was removed: %v", err)
	}
}

// TestEmptyDirRemoved: with only this fetch's file in it, the dir goes too.
func TestEmptyDirRemoved(t *testing.T) {
	h := newHarness(t)
	rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.en.vtt")
	full := writeCaption(t, h, rel)

	h.run(t, &fetcher{rel: rel, lang: "en"})

	if _, err := os.Stat(filepath.Dir(full)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty caption dir left behind: %v", err)
	}
}

// TestInFlightGuard: a second Start for the same video while one runs is
// refused; once it settles, the video can be fetched again.
func TestInFlightGuard(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	done := make(chan string, 2)
	f := &fetcher{onCall: func(context.Context) { <-release }}
	s := New(Deps{
		Fetcher: f, Videos: h.videos, MediaDir: h.mediaDir,
		Activity: h.events, Background: t.Context(),
		OnDone: func(id string) { done <- id },
	})
	if !s.Start("v1", "https://youtu.be/v1") {
		t.Fatal("first Start refused")
	}
	if s.Start("v1", "https://youtu.be/v1") {
		t.Fatal("second Start accepted while the first runs")
	}
	close(release)
	<-done
	if !s.Start("v1", "https://youtu.be/v1") {
		t.Fatal("Start refused after the first fetch settled")
	}
	<-done
	s.Wait()
}
