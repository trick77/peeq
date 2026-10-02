package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/videos"
)

// seedGaveUp puts a video in the state the caption fetcher leaves behind when
// its ladder runs out: a pending ledger row that is settled and carries the
// last failure, and a videos row at no_transcript with no transcript.
func seedGaveUp(t *testing.T, h *pendingTestHarness, id string) {
	t.Helper()
	if err := h.ledger.Insert(channelvideos.Entry{
		VideoID: id, ChannelID: "UC1", Title: "A video",
		URL: "https://www.youtube.com/watch?v=" + id, State: channelvideos.StatePending,
	}); err != nil {
		t.Fatalf("insert ledger row: %v", err)
	}
	if err := h.videos.Upsert(videos.Video{ID: id, URL: "https://youtu.be/" + id}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := h.videos.SetSummaryStatus(id, videos.SummaryNoTranscript, ""); err != nil {
		t.Fatalf("set summary status: %v", err)
	}
	if err := h.ledger.SetCaptionLastError(id, "yt-dlp exploded"); err != nil {
		t.Fatalf("set last error: %v", err)
	}
	if err := h.ledger.MarkCaptionSettled(id); err != nil {
		t.Fatalf("settle: %v", err)
	}
}

// TestRetryCaptions_putsTheVideoBackOnTheLadder: settled used to be forever.
// The handler makes no YouTube call of its own — it makes the row due, and the
// fetcher reaches it through the Runner's gates like any other.
func TestRetryCaptions_putsTheVideoBackOnTheLadder(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	seedGaveUp(t, h, "p1")

	if c, err := h.ledger.NextCaptionCandidate(); err != nil || c != nil {
		t.Fatalf("a settled video is a candidate before the retry: %+v (err %v)", c, err)
	}

	if rr := postJSON(t, h, "/api/pending/p1/retry-captions", nil); rr.Code != http.StatusOK {
		t.Fatalf("retry status = %d, body = %s", rr.Code, rr.Body.String())
	}

	c, err := h.ledger.NextCaptionCandidate()
	if err != nil || c == nil || c.VideoID != "p1" {
		t.Fatalf("candidate after the retry = %+v (err %v), want p1", c, err)
	}
	if c.Attempts != 0 {
		t.Fatalf("attempts = %d, want a fresh ladder", c.Attempts)
	}
	v, err := h.videos.Get("p1")
	if err != nil || v == nil {
		t.Fatalf("get video: %v", err)
	}
	// 'pending' with no transcript is what the card reads as "Waiting for
	// captions"; left at no_transcript it would keep saying it had given up.
	if v.SummaryStatus != videos.SummaryPending {
		t.Fatalf("summary_status = %q, want pending", v.SummaryStatus)
	}
	if got, err := h.ledger.CaptionLastError("p1"); err != nil || got != "" {
		t.Fatalf("caption_last_error = %q (err %v), want it cleared", got, err)
	}
}

// TestRetryCaptions_refusals: every state in which a retry would either do
// nothing or undo something.
func TestRetryCaptions_refusals(t *testing.T) {
	t.Run("unknown video", func(t *testing.T) {
		h := newPendingTestServer(t)
		if rr := postJSON(t, h, "/api/pending/nope/retry-captions", nil); rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rr.Code)
		}
	})
	t.Run("already decided", func(t *testing.T) {
		h := newPendingTestServer(t)
		h.seedChannel("UC1")
		seedGaveUp(t, h, "p1")
		if err := h.ledger.SetState("p1", channelvideos.StateIgnored); err != nil {
			t.Fatalf("set state: %v", err)
		}
		if rr := postJSON(t, h, "/api/pending/p1/retry-captions", nil); rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rr.Code)
		}
	})
	// Captions that were fetched and judged music-only: fetching them again
	// returns the same file, and resetting the status would discard the verdict.
	t.Run("has a transcript", func(t *testing.T) {
		h := newPendingTestServer(t)
		h.seedChannel("UC1")
		seedGaveUp(t, h, "p1")
		if err := h.videos.SetTranscript("p1", videos.TranscriptSourceCaption, "WEBVTT\n"); err != nil {
			t.Fatalf("set transcript: %v", err)
		}
		if rr := postJSON(t, h, "/api/pending/p1/retry-captions", nil); rr.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", rr.Code)
		}
		if v, _ := h.videos.Get("p1"); v == nil || v.SummaryStatus != videos.SummaryNoTranscript {
			t.Fatalf("a refused retry changed the video: %+v", v)
		}
	})
	t.Run("still on the ladder", func(t *testing.T) {
		h := newPendingTestServer(t)
		h.seedChannel("UC1")
		seedGaveUp(t, h, "p1")
		if err := h.videos.SetSummaryStatus("p1", videos.SummaryPending, ""); err != nil {
			t.Fatalf("set summary status: %v", err)
		}
		if rr := postJSON(t, h, "/api/pending/p1/retry-captions", nil); rr.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", rr.Code)
		}
	})
	// The fetcher skips an opted-out channel, so the retried row would wait
	// for captions that are never fetched.
	t.Run("channel opted out", func(t *testing.T) {
		h := newPendingTestServer(t)
		h.seedChannel("UC1")
		seedGaveUp(t, h, "p1")
		if _, ok, err := h.channels.SetAutoSummary("UC1", false); err != nil || !ok {
			t.Fatalf("opt out: ok=%v err=%v", ok, err)
		}
		if rr := postJSON(t, h, "/api/pending/p1/retry-captions", nil); rr.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409", rr.Code)
		}
	})
}

// TestRetryCaptions_notConfigured_503: no ledger, no pending API.
func TestRetryCaptions_notConfigured_503(t *testing.T) {
	h := New(testDeps(t))
	if rr := postJSON(t, h, "/api/pending/p1/retry-captions", nil); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

// TestGetVideo_carriesTheCaptionError: the page that says captions could not be
// fetched is the page that has to say why.
func TestGetVideo_carriesTheCaptionError(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	seedGaveUp(t, h, "p1")

	var got struct {
		CaptionError string `json:"caption_error"`
	}
	if err := json.Unmarshal([]byte(getJSON(t, h, "/api/videos/p1")), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.CaptionError != "yt-dlp exploded" {
		t.Fatalf("caption_error = %q, want the stored failure", got.CaptionError)
	}
}
