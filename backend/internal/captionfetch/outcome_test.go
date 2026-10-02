package captionfetch

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// lastError reads the stored outcome straight off the ledger row.
func lastError(t *testing.T, h *harness) string {
	t.Helper()
	var got string
	if err := h.db.QueryRow(
		`SELECT caption_last_error FROM channel_videos WHERE video_id = 'v1'`).Scan(&got); err != nil {
		t.Fatalf("read caption_last_error: %v", err)
	}
	return got
}

// TestFetchErrorIsRecordedOnTheRow: the reason a video was written off used to
// live only in the log, so a restart made five settled videos unexplainable.
// The text outlives settling — that is the moment it is needed.
func TestFetchErrorIsRecordedOnTheRow(t *testing.T) {
	h := newHarness(t)
	boom := errors.New("yt-dlp exploded: https://example.com/watch?v=v1&token=secret")
	f := &fetcher{errs: []error{boom, boom, boom, boom, boom}}
	w := h.worker(f)

	w.pass(context.Background())
	if got := lastError(t, h); got != "yt-dlp exploded: https://example.com/watch" {
		t.Fatalf("caption_last_error = %q, want the redacted fetch error", got)
	}

	for i := 1; i < channelvideos.CaptionMaxAttempts; i++ {
		mustBeDue(t, h)
		w.pass(context.Background())
	}
	if got := lastError(t, h); got == "" {
		t.Fatal("caption_last_error was cleared by settling; it must survive it")
	}
}

// TestCleanMissClearsAnOlderError: the column is the LAST outcome. An attempt
// that ran cleanly and found no track must not leave an earlier error standing
// as the reason.
func TestCleanMissClearsAnOlderError(t *testing.T) {
	h := newHarness(t)
	f := &fetcher{errs: []error{errors.New("yt-dlp exploded")}}
	w := h.worker(f)

	w.pass(context.Background())
	mustBeDue(t, h)
	w.pass(context.Background())

	if got := lastError(t, h); got != "" {
		t.Fatalf("caption_last_error = %q after a clean miss, want empty", got)
	}
}

// TestGatedFetchLeavesTheRecordedErrorAlone: a refusal is not an outcome for
// this video, so it neither writes nor clears.
func TestGatedFetchLeavesTheRecordedErrorAlone(t *testing.T) {
	h := newHarness(t)
	f := &fetcher{errs: []error{errors.New("yt-dlp exploded"), ytdlp.ErrPaused}}
	w := h.worker(f)

	w.pass(context.Background())
	mustBeDue(t, h)
	w.pass(context.Background())

	if got := lastError(t, h); got != "yt-dlp exploded" {
		t.Fatalf("caption_last_error = %q after a gated pass, want the earlier error kept", got)
	}
}

// TestLostCaptionIsRecordedAsAFailure: yt-dlp reported a caption that is not on
// disk. The call was clean, so an empty outcome would claim "no track found"
// for captions that were found and then lost.
func TestLostCaptionIsRecordedAsAFailure(t *testing.T) {
	h := newHarness(t)
	rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.en.vtt")
	f := &fetcher{results: []string{rel}} // no writeCaption: the file is absent

	h.worker(f).pass(context.Background())

	if got := lastError(t, h); !strings.Contains(got, "read caption") {
		t.Fatalf("caption_last_error = %q, want the read failure", got)
	}
}

// failingLanguage is a VideoStore whose SetAudioLanguage fails: the transcript
// is stored, the step after it is not.
type failingLanguage struct{ *videos.Store }

func (failingLanguage) SetAudioLanguage(string, string) error {
	return errors.New("language write failed")
}

// TestFailureAfterTheTranscriptIsRecorded: the attempt died after the fetch,
// and the row must not read as a clean one.
func TestFailureAfterTheTranscriptIsRecorded(t *testing.T) {
	h := newHarness(t)
	rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.en.vtt")
	writeCaption(t, h, rel)
	w := NewWorker(Deps{
		Fetcher: &fetcher{results: []string{rel}}, Ledger: h.ledger,
		Videos: failingLanguage{h.videos}, Summaries: h.summary, MediaDir: h.mediaDir,
	})

	w.pass(context.Background())

	if got := lastError(t, h); got != "language write failed" {
		t.Fatalf("caption_last_error = %q, want the failed step", got)
	}
}

// outcomeLossLedger fails the outcome write and nothing else.
type outcomeLossLedger struct{ *channelvideos.Store }

func (outcomeLossLedger) SetCaptionLastError(string, string) error {
	return errors.New("write failed")
}

// TestOutcomeWriteFailureDoesNotChangeTheVideo: recording the reason is
// best-effort. Losing it must not stop the ladder from settling, or the card
// would wait for captions forever.
func TestOutcomeWriteFailureDoesNotChangeTheVideo(t *testing.T) {
	h := newHarness(t)
	boom := errors.New("yt-dlp exploded")
	w := NewWorker(Deps{
		Fetcher: &fetcher{errs: []error{boom, boom, boom, boom, boom}},
		Ledger:  outcomeLossLedger{h.ledger},
		Videos:  h.videos, Summaries: h.summary, MediaDir: h.mediaDir,
	})

	for i := 0; i < channelvideos.CaptionMaxAttempts; i++ {
		w.pass(context.Background())
		mustBeDue(t, h)
	}

	v, err := h.videos.Get("v1")
	if err != nil || v == nil {
		t.Fatalf("get video: %v", err)
	}
	if v.SummaryStatus != videos.SummaryNoTranscript {
		t.Fatalf("summary_status = %q, want no_transcript", v.SummaryStatus)
	}
}

// TestCaptionsArrivingClearTheRecordedError: a video that got its captions has
// no failure to report.
func TestCaptionsArrivingClearTheRecordedError(t *testing.T) {
	h := newHarness(t)
	rel := filepath.Join(ytdlp.SummaryDirName, "v1", "v1.en.vtt")
	writeCaption(t, h, rel)
	f := &fetcher{results: []string{"", rel}, errs: []error{errors.New("yt-dlp exploded")}}
	w := h.worker(f)

	w.pass(context.Background())
	mustBeDue(t, h)
	w.pass(context.Background())

	if got := lastError(t, h); got != "" {
		t.Fatalf("caption_last_error = %q after captions arrived, want empty", got)
	}
}
