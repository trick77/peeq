package download

import (
	"errors"
	"testing"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/videos"
)

// gateDetail is the only place peeq translates its internal reason vocabulary
// into words a person reads on the Activity feed, so every arm is worth
// pinning: a reason that falls through to the default silently degrades a
// specific explanation ("members-only video") into a vague one.
func TestGateDetail_wordsForEveryReason(t *testing.T) {
	cases := map[string]string{
		"members": "members-only video",
		"age":     "age-restricted video",
		"geo":     "not available in this region",
		"premium": "YouTube Premium video",
		"private": "private video",
		"deleted": "video was removed",
		// An unknown reason must still say something honest rather than leak
		// the raw token or render blank.
		"":       "not available to download",
		"gibber": "not available to download",
	}
	for reason, want := range cases {
		if got := gateDetail(reason); got != want {
			t.Errorf("gateDetail(%q) = %q, want %q", reason, got, want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "b", "c"); got != "b" {
		t.Errorf("got %q, want b", got)
	}
	if got := firstNonEmpty("a", "b"); got != "a" {
		t.Errorf("got %q, want a", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// videoID exists so the failure log can name a video even when the row could
// not be loaded — the case where a nil check at the call site is easy to
// forget.
func TestVideoID_nilIsEmptyNotAPanic(t *testing.T) {
	if got := videoID(nil); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := videoID(&videos.Video{ID: "abc"}); got != "abc" {
		t.Errorf("got %q, want abc", got)
	}
}

// brokenLedger fails whichever half the test is exercising.
type brokenLedger struct {
	getErr    error
	setErr    error
	row       *channelvideos.Entry
	discarded bool
}

func (b *brokenLedger) Get(string) (*channelvideos.Entry, error) {
	return b.row, b.getErr
}

func (b *brokenLedger) SetUnavailable(string, string) error {
	b.discarded = true
	return b.setErr
}

// A ledger that cannot be read or written must not take the parking path: the
// video row is the only remaining record, and discarding it on the strength of
// a failed lookup would destroy it for nothing. Both halves fall back to the
// ordinary error path instead.
func TestPark_ledgerFailuresFallBackToTheErrorPath(t *testing.T) {
	video := &videos.Video{ID: "vid", Title: "T"}

	t.Run("lookup fails", func(t *testing.T) {
		h := newHarness(t, &fakeRunner{}, func(d *Deps) {
			d.Ledger = &brokenLedger{getErr: errors.New("db is gone")}
		})
		if h.worker.park(video, "members") {
			t.Fatal("park must not claim a video it could not look up")
		}
	})

	t.Run("write fails", func(t *testing.T) {
		led := &brokenLedger{
			row:    &channelvideos.Entry{VideoID: "vid", Title: "T"},
			setErr: errors.New("db is gone"),
		}
		h := newHarness(t, &fakeRunner{}, func(d *Deps) { d.Ledger = led })
		if h.worker.park(video, "members") {
			t.Fatal("park must not claim a video it failed to park")
		}
	})
}

// No ledger wired at all (the default in tests, and any deployment that
// somehow skips it) is not an error — it just means there is nowhere to hand
// the video, so the plain error path runs.
func TestPark_withoutALedgerDoesNothing(t *testing.T) {
	h := newHarness(t, &fakeRunner{}, nil)
	if h.worker.park(&videos.Video{ID: "vid"}, "members") {
		t.Fatal("park must be a no-op with no ledger")
	}
	if h.worker.park(nil, "members") {
		t.Fatal("park must be a no-op with no video")
	}
}
