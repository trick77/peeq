package channelvideos

import (
	"testing"

	"github.com/trick77/peeq/internal/videos"
)

// TestSetCaptionLastError_survivesSettling: the reason is read once the
// fetcher has given up, so settling must not take it away.
func TestSetCaptionLastError_survivesSettling(t *testing.T) {
	st := newTestStore(t)
	seedChannel(t, st, "UC1")
	if err := st.Insert(Entry{VideoID: "v1", ChannelID: "UC1", Title: "A video", URL: "https://youtu.be/v1", State: StatePending}); err != nil {
		t.Fatal(err)
	}

	if err := st.SetCaptionLastError("v1", "yt-dlp exploded"); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkCaptionSettled("v1"); err != nil {
		t.Fatal(err)
	}

	var got string
	if err := st.db.QueryRow(`SELECT caption_last_error FROM channel_videos WHERE video_id = 'v1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "yt-dlp exploded" {
		t.Fatalf("caption_last_error = %q after settling, want it kept", got)
	}
}

// TestSetCaptionLastError_errorsOnClosedDB: a lost write is reported, not
// swallowed — the worker logs it.
func TestSetCaptionLastError_errorsOnClosedDB(t *testing.T) {
	st := newTestStore(t)
	if err := st.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if err := st.SetCaptionLastError("v1", "x"); err == nil {
		t.Fatal("expected an error writing against a closed db")
	}
}

// TestCaptionLastError_noRowIsEmpty: a video that never touched the ledger has
// no outcome, which is not an error.
func TestCaptionLastError_noRowIsEmpty(t *testing.T) {
	st := newTestStore(t)
	if got, err := st.CaptionLastError("nope"); err != nil || got != "" {
		t.Fatalf("CaptionLastError = %q, %v; want empty, nil", got, err)
	}
}

// TestRetryCaptions_resetsOnlyAGivenUpRow: the ladder restarts for a settled
// no_transcript video with nothing fetched, and for nothing else.
func TestRetryCaptions_resetsOnlyAGivenUpRow(t *testing.T) {
	st := newTestStore(t)
	vs := videos.New(st.db)
	seedChannel(t, st, "UC1")
	if err := st.Insert(Entry{VideoID: "v1", ChannelID: "UC1", Title: "A video", URL: "https://youtu.be/v1", State: StatePending}); err != nil {
		t.Fatal(err)
	}
	if err := vs.Upsert(videos.Video{ID: "v1", URL: "https://youtu.be/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkCaptionSettled("v1"); err != nil {
		t.Fatal(err)
	}

	// Still 'pending': the fetcher has not given up, so there is nothing to retry.
	if ok, err := st.RetryCaptions("v1"); err != nil || ok {
		t.Fatalf("retry before giving up = %v, %v; want false, nil", ok, err)
	}

	if err := vs.SetSummaryStatus("v1", videos.SummaryNoTranscript, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCaptionLastError("v1", "yt-dlp exploded"); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.RetryCaptions("v1"); err != nil || !ok {
		t.Fatalf("retry = %v, %v; want true, nil", ok, err)
	}

	c, err := st.NextCaptionCandidate()
	if err != nil || c == nil || c.Attempts != 0 {
		t.Fatalf("candidate after retry = %+v, %v; want v1 at 0 attempts", c, err)
	}
	if got, err := st.CaptionLastError("v1"); err != nil || got != "" {
		t.Fatalf("caption_last_error = %q, %v; want cleared", got, err)
	}
	v, err := vs.Get("v1")
	if err != nil || v == nil || v.SummaryStatus != videos.SummaryPending {
		t.Fatalf("video after retry = %+v, %v; want summary_status pending", v, err)
	}
}

// TestRetryCaptions_errorsOnClosedDB: a store fault must not read as "nothing
// to retry", which the handler answers with a 409.
func TestRetryCaptions_errorsOnClosedDB(t *testing.T) {
	st := newTestStore(t)
	if err := st.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if _, err := st.RetryCaptions("v1"); err == nil {
		t.Fatal("expected an error retrying against a closed db")
	}
	if _, err := st.CaptionLastError("v1"); err == nil {
		t.Fatal("expected an error reading against a closed db")
	}
}
