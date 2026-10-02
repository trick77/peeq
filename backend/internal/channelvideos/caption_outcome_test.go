package channelvideos

import "testing"

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
