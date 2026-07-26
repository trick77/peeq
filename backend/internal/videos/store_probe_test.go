package videos

import (
	"testing"
)

// Tests for store_probe.go: the ffprobe backfill claim and its write.

func TestSetProbed_persistsAndStampsTheAttempt(t *testing.T) {
	testee := newTestStore(t)
	seedVideo(t, testee, Video{ID: "v", URL: "u", Title: "t", ChannelID: "c"})
	if err := testee.SetDownloaded("v", DownloadedResult{MediaPath: "/m/v.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	if err := testee.SetProbed("v", ProbeResult{
		Container: "mp4", VideoCodec: "h264", VideoHeight: 1080, AudioCodec: "aac",
	}); err != nil {
		t.Fatalf("SetProbed: %v", err)
	}

	got, err := testee.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.MediaContainer != "mp4" || got.VideoCodec != "h264" || got.VideoHeight != 1080 || got.AudioCodec != "aac" {
		t.Errorf("probe values not persisted: %+v", got)
	}
	if got.ProbedAt == "" {
		t.Error("probed_at not stamped")
	}
}

// A zero result is what the failure path writes. It must still stamp
// probed_at, or UnprobedDownloaded returns the same unreadable file forever.
func TestSetProbed_stampsEvenForAZeroResult(t *testing.T) {
	testee := newTestStore(t)
	seedVideo(t, testee, Video{ID: "v", URL: "u", Title: "t", ChannelID: "c"})
	if err := testee.SetDownloaded("v", DownloadedResult{MediaPath: "/m/v.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	if err := testee.SetProbed("v", ProbeResult{}); err != nil {
		t.Fatalf("SetProbed: %v", err)
	}

	got, err := testee.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ProbedAt == "" {
		t.Fatal("a failed probe left probed_at empty; the sweep would never converge")
	}

	left, err := testee.UnprobedDownloaded(10)
	if err != nil {
		t.Fatalf("UnprobedDownloaded: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("still claimable after a recorded attempt: %+v", left)
	}
}

func TestUnprobedDownloaded_onlyDownloadedFilesNeverProbed(t *testing.T) {
	testee := newTestStore(t)

	// Downloaded, never probed — the one row that should come back.
	seedVideo(t, testee, Video{ID: "want", URL: "u", Title: "t", ChannelID: "c"})
	if err := testee.SetDownloaded("want", DownloadedResult{MediaPath: "/m/want.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	// Downloaded and already probed.
	seedVideo(t, testee, Video{ID: "done", URL: "u", Title: "t", ChannelID: "c"})
	if err := testee.SetDownloaded("done", DownloadedResult{MediaPath: "/m/done.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	if err := testee.SetProbed("done", ProbeResult{Container: "mp4"}); err != nil {
		t.Fatalf("SetProbed: %v", err)
	}
	// Never downloaded: no file to probe.
	seedVideo(t, testee, Video{ID: "queued", URL: "u", Title: "t", ChannelID: "c", Status: "queued"})

	got, err := testee.UnprobedDownloaded(10)
	if err != nil {
		t.Fatalf("UnprobedDownloaded: %v", err)
	}
	if len(got) != 1 || got[0].ID != "want" {
		t.Fatalf("claimed %+v, want exactly [want]", got)
	}
	if got[0].MediaPath != "/m/want.mp4" {
		t.Errorf("MediaPath = %q, want /m/want.mp4", got[0].MediaPath)
	}
}

func TestUnprobedDownloaded_respectsTheLimit(t *testing.T) {
	testee := newTestStore(t)
	for _, id := range []string{"a", "b", "c"} {
		seedVideo(t, testee, Video{ID: id, URL: "u", Title: "t", ChannelID: "ch"})
		if err := testee.SetDownloaded(id, DownloadedResult{MediaPath: "/m/" + id + ".mp4"}); err != nil {
			t.Fatalf("set downloaded %s: %v", id, err)
		}
	}

	got, err := testee.UnprobedDownloaded(2)
	if err != nil {
		t.Fatalf("UnprobedDownloaded: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("claimed %d rows, want 2", len(got))
	}
}

// A write that never landed must be reported. The backfill worker logs the
// error and leaves the row unprobed for the next pass; a swallowed error would
// instead look like a successful attempt that wrote nothing.
func TestSetProbed_errorsOnClosedDB(t *testing.T) {
	testee := newTestStore(t)
	if err := testee.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if err := testee.SetProbed("v", ProbeResult{Container: "mp4"}); err == nil {
		t.Fatal("expected an error writing against a closed db")
	}
}

// An empty candidate list and a failed query must not look alike: the sweep
// treats "nothing to do" as done, so a masked error would strand the backlog.
func TestUnprobedDownloaded_errorsOnClosedDB(t *testing.T) {
	testee := newTestStore(t)
	if err := testee.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if _, err := testee.UnprobedDownloaded(10); err == nil {
		t.Fatal("expected an error listing against a closed db")
	}
}
