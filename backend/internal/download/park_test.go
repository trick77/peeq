package download

import (
	"context"
	"sync"
	"testing"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// recorder captures Activity events so a test can assert on what the user is
// told, not just on what the database holds.
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

// gatedRunner fails every download with the given terminal reason.
func gatedRunner(reason string) *fakeRunner {
	return &fakeRunner{
		fn: func(context.Context, int, ytdlp.DownloadReq, func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return nil, &ytdlp.TerminalError{Reason: reason}
		},
	}
}

// parkHarness wires the worker with a real scan ledger and Activity recorder,
// and (when ledgerRow is true) seeds the channel + ledger row a scanned video
// would have — i.e. the state the Inbox's Download button leaves behind.
func parkHarness(t *testing.T, runner *fakeRunner, ledgerRow bool) (*harness, *channelvideos.Store, *recorder) {
	t.Helper()
	rec := &recorder{}
	var ledger *channelvideos.Store
	h := newHarness(t, runner, func(d *Deps) {
		d.Activity = rec
	})
	ledger = channelvideos.New(h.db)
	h.worker.deps.Ledger = ledger
	if ledgerRow {
		if err := h.channels.Upsert(channels.Channel{ID: "UC1", Name: "UC1"}); err != nil {
			t.Fatalf("seed channel: %v", err)
		}
		if err := ledger.Insert(channelvideos.Entry{
			VideoID: "vid", ChannelID: "UC1", Title: "Gated", State: "queued",
		}); err != nil {
			t.Fatalf("seed ledger: %v", err)
		}
	}
	return h, ledger, rec
}

// A members-only download must leave the video parked in the ledger — where a
// later scan can re-offer it — and NOT as a Library row whose re-download
// button can never succeed.
func TestWorker_membersOnly_parksInLedgerAndDropsTheDeadRow(t *testing.T) {
	h, ledger, rec := parkHarness(t, gatedRunner("members"), true /*ledgerRow*/)
	h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	// Waiting on the video row rather than the job row: discarding the video
	// cascades download_jobs away with it, so there is no job left to inspect
	// by the time this settles (see videos.Store.Discard).
	waitFor(t, "video row discarded", func() bool {
		v, err := h.videos.Get("vid")
		return err == nil && v == nil
	})

	row, err := ledger.Get("vid")
	if err != nil || row == nil {
		t.Fatalf("ledger row for vid missing: %v", err)
	}
	if row.State != channelvideos.StateUnavailable {
		t.Fatalf("ledger state = %q, want %q", row.State, channelvideos.StateUnavailable)
	}
	if row.UnavailableReason != "members" {
		t.Fatalf("reason = %q, want members", row.UnavailableReason)
	}
	if row.UnavailableAt == "" {
		t.Fatal("unavailable_at must be stamped so the scan can time its re-offer")
	}
	// The user still gets told, in words rather than in peeq's internal
	// vocabulary — and the row names the video, which means the Activity event
	// had to be written before the videos row was discarded.
	var found bool
	for _, e := range rec.all() {
		if e.Summary == "not available" {
			found = true
			if e.Detail != "members-only video" {
				t.Fatalf("detail = %q, want members-only video", e.Detail)
			}
			if e.Subject != "Gated" {
				t.Fatalf("subject = %q, want the video title", e.Subject)
			}
		}
	}
	if !found {
		t.Fatalf("no 'not available' activity row; got %+v", rec.all())
	}
}

// A hand-added video has no ledger row to remember it, so discarding its row
// would erase the only record that the user ever asked for it. It keeps the
// ordinary error row instead.
func TestWorker_membersOnly_keepsRowWhenNothingElseRemembersIt(t *testing.T) {
	h, _, _ := parkHarness(t, gatedRunner("members"), false /*ledgerRow*/)
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	waitFor(t, "job failed", func() bool { return h.jobState(t, id).State == "failed" })
	waitForVideoStatus(t, h, "vid", "error")

	v, err := h.videos.Get("vid")
	if err != nil || v == nil {
		t.Fatalf("video row must survive without a ledger row: %v", err)
	}
	if v.ErrorMessage == "" {
		t.Fatal("error_message not recorded")
	}
}

// A video that has downloaded before is never discarded, however it fails.
// Re-download is offered for tombstoned rows too, so a channel gating a
// previously-public video would otherwise turn one click into the loss of the
// watch history, summary and transcript that row still carries.
func TestWorker_membersOnly_neverDiscardsAVideoThatOnceDownloaded(t *testing.T) {
	h, ledger, _ := parkHarness(t, gatedRunner("members"), true /*ledgerRow*/)
	id := h.enqueue(t, "vid", 0)
	// Stamp the row the way a completed download would, then tombstone it —
	// exactly the state the re-download button acts on.
	if err := h.videos.SetDownloaded("vid", videos.DownloadedResult{
		MediaPath: "vid.mp4", FilesizeBytes: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.videos.Tombstone("vid"); err != nil {
		t.Fatal(err)
	}
	runWorker(t, h.worker)

	waitFor(t, "job failed", func() bool { return h.jobState(t, id).State == "failed" })
	waitForVideoStatus(t, h, "vid", "error")

	v, err := h.videos.Get("vid")
	if err != nil || v == nil {
		t.Fatalf("a previously-downloaded video must never be discarded: %v", err)
	}
	row, _ := ledger.Get("vid")
	if row.State == channelvideos.StateUnavailable {
		t.Fatal("it must not be parked either — the Library row is the record")
	}
}

// Exhausting the retries is NOT a gate: something transient kept going wrong,
// which is exactly what the Library's re-download button is for. Parking it
// would take that away.
func TestWorker_retriesExhausted_isNotParked(t *testing.T) {
	runner := &fakeRunner{
		fn: func(context.Context, int, ytdlp.DownloadReq, func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return nil, &ytdlp.RetryableError{Reason: "network"}
		},
	}
	h, ledger, _ := parkHarness(t, runner, true /*ledgerRow*/)
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	waitFor(t, "job failed", func() bool { return h.jobState(t, id).State == "failed" })
	waitForVideoStatus(t, h, "vid", "error")

	v, _ := h.videos.Get("vid")
	if v == nil {
		t.Fatal("a retryable failure must keep its video row")
	}
	row, _ := ledger.Get("vid")
	if row.State == channelvideos.StateUnavailable {
		t.Fatal("a retryable failure must not park the ledger row")
	}
}
