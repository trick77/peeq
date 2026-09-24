package download

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// persistRunner is a fake whose download "writes" a real media file under
// mediaDir on every call, and runs onCall first so a test can change the
// database between attempts.
func persistRunner(t *testing.T, mediaDir string, onCall func(call int)) *fakeRunner {
	t.Helper()
	return &fakeRunner{
		fn: func(_ context.Context, call int, req ytdlp.DownloadReq, _ func(ytdlp.Progress)) (*ytdlp.Result, error) {
			if onCall != nil {
				onCall(call)
			}
			dir := filepath.Join(mediaDir, "chan1", req.VideoID)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, req.VideoID+".mp4")
			if err := os.WriteFile(path, []byte("VIDEO"), 0o644); err != nil {
				t.Fatal(err)
			}
			return &ytdlp.Result{MediaPath: path, FormatUsed: "f"}, nil
		},
	}
}

// blockDownloadedWrite makes the video row's 'downloaded' update fail. On this
// path SetDownloaded is the only writer of media_path; SetStatus and the job
// tables are untouched by it, so the retry ladder still works.
func blockDownloadedWrite(t *testing.T, h *harness) {
	t.Helper()
	if _, err := h.db.Exec(`CREATE TRIGGER block_downloaded BEFORE UPDATE OF media_path ON videos
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
}

// TestSucceed_persistFailureRetriesThenErrors: the job's 'done' and the video's
// 'downloaded' land together or not at all. When they do not, the job takes the
// ordinary retry ladder — it is still 'running', nothing was committed — and the
// file just written is removed, since no row points at it. Once the ladder runs
// out the video is in 'error' with the cause, where the user can retry it. It
// used to be left in 'downloading' forever behind a job already marked 'done'.
func TestSucceed_persistFailureRetriesThenErrors(t *testing.T) {
	mediaDir := t.TempDir()
	rec := &recorder{}
	runner := persistRunner(t, mediaDir, nil)
	h := newHarness(t, runner, func(d *Deps) { d.Activity = rec; d.MediaDir = mediaDir })
	job := h.enqueue(t, "vid1", 0)
	if _, err := h.db.Exec(`UPDATE download_jobs SET max_attempts = 2 WHERE id = ?`, job); err != nil {
		t.Fatal(err)
	}
	blockDownloadedWrite(t, h)
	runWorker(t, h.worker)

	waitFor(t, "job failed", func() bool { return h.jobState(t, job).State == "failed" })
	waitForVideoStatus(t, h, "vid1", videos.StatusError)

	if c := runner.calls(); c != 2 {
		t.Fatalf("download attempts = %d, want the ladder's 2", c)
	}
	v, err := h.videos.Get("vid1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.ErrorMessage, "boom") {
		t.Fatalf("error_message = %q, want the cause of the failed save", v.ErrorMessage)
	}
	if _, err := os.Stat(filepath.Join(mediaDir, "chan1", "vid1", "vid1.mp4")); !os.IsNotExist(err) {
		t.Fatalf("media file left on disk with no row pointing at it (stat err = %v)", err)
	}
	evs := rec.all()
	if len(evs) != 1 || evs[0].Kind != activity.KindDownload || evs[0].Outcome != activity.OutcomeFail {
		t.Fatalf("activity = %+v, want one download failure row", evs)
	}
}

// TestSucceed_transientPersistFailureRecovers: a save that fails once (a busy
// database, say) costs one re-download and nothing else — the second attempt
// lands 'done' and 'downloaded' together.
func TestSucceed_transientPersistFailureRecovers(t *testing.T) {
	mediaDir := t.TempDir()
	var h *harness
	runner := persistRunner(t, mediaDir, func(call int) {
		if call == 1 {
			if _, err := h.db.Exec(`DROP TRIGGER block_downloaded`); err != nil {
				t.Fatal(err)
			}
		}
	})
	h = newHarness(t, runner, func(d *Deps) { d.MediaDir = mediaDir })
	job := h.enqueue(t, "vid1", 0)
	blockDownloadedWrite(t, h)
	runWorker(t, h.worker)

	waitFor(t, "job done", func() bool { return h.jobState(t, job).State == "done" })
	waitForVideoStatus(t, h, "vid1", videos.StatusDownloaded)

	if c := runner.calls(); c != 2 {
		t.Fatalf("download attempts = %d, want 2 (one failed save, one clean)", c)
	}
	v, err := h.videos.Get("vid1")
	if err != nil {
		t.Fatal(err)
	}
	if v.MediaPath == "" {
		t.Fatal("media_path empty after a successful second attempt")
	}
	if _, err := os.Stat(v.MediaPath); err != nil {
		t.Fatalf("media file missing: %v", err)
	}
}
