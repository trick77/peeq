package videos

import "testing"

// A transcript round-trips verbatim. Byte-for-byte matters: the <track>
// element, the browser-side parser and the user-facing .vtt download all read
// what comes back out.
func TestSetTranscript_roundTripsVerbatim(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")
	const vtt = "WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nHello there.\n"

	if err := s.SetTranscript("v1", TranscriptSourceDownload, vtt); err != nil {
		t.Fatalf("set transcript: %v", err)
	}
	got, err := s.GetTranscript("v1")
	if err != nil || got == nil {
		t.Fatalf("get transcript = %v, %v", got, err)
	}
	if got.VTT != vtt {
		t.Fatalf("stored %q, want it verbatim", got.VTT)
	}
	if got.Source != TranscriptSourceDownload {
		t.Fatalf("source = %q, want download", got.Source)
	}
	if got.UpdatedAt == "" {
		t.Fatal("updated_at empty; the endpoint would lose Last-Modified")
	}
}

// The handover: a video read from the inbox and later downloaded shares one
// row, and the download's write must OVERWRITE the source. A conflict clause
// that kept the old value would strand a fully downloaded video on the
// truncated inbox pipeline — no category, no key points, no embeddings — with
// nothing to ever correct it.
func TestSetTranscript_downloadOverwritesACaptionRead(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")
	if err := s.SetTranscript("v1", TranscriptSourceCaption, "WEBVTT\ninbox"); err != nil {
		t.Fatalf("seed caption read: %v", err)
	}

	if err := s.SetTranscript("v1", TranscriptSourceDownload, "WEBVTT\ndownloaded"); err != nil {
		t.Fatalf("store download transcript: %v", err)
	}
	got, err := s.GetTranscript("v1")
	if err != nil || got == nil {
		t.Fatalf("get transcript = %v, %v", got, err)
	}
	if got.Source != TranscriptSourceDownload || got.VTT != "WEBVTT\ndownloaded" {
		t.Fatalf("after download: %+v, want the download's text and source", got)
	}
	if src, serr := s.TranscriptSource("v1"); serr != nil || src != TranscriptSourceDownload {
		t.Fatalf("TranscriptSource = %q, %v", src, serr)
	}
}

// A video with no transcript reads as (nil, nil) and an empty source: "nothing
// stored" is an ordinary state that the handlers turn into a 404 and the
// summarize worker into no_transcript.
func TestGetTranscript_missingIsNotAnError(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")

	got, err := s.GetTranscript("v1")
	if err != nil || got != nil {
		t.Fatalf("get transcript = %v, %v, want nil, nil", got, err)
	}
	src, err := s.TranscriptSource("v1")
	if err != nil || src != "" {
		t.Fatalf("TranscriptSource = %q, %v, want empty", src, err)
	}
}

// Guards: an unknown source and an oversized transcript are both refused, and
// neither leaves anything behind.
func TestSetTranscript_guards(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")

	if err := s.SetTranscript("v1", "guessed", "WEBVTT"); err == nil {
		t.Fatal("unknown source accepted, want refused")
	}
	if err := s.SetTranscript("v1", TranscriptSourceDownload, string(make([]byte, MaxTranscriptBytes+1))); err == nil {
		t.Fatal("oversized transcript accepted, want refused")
	}
	if err := s.SetTranscript("v1", TranscriptSourceDownload, ""); err == nil {
		t.Fatal("empty transcript accepted, want refused")
	}
	if got, err := s.GetTranscript("v1"); err != nil || got != nil {
		t.Fatalf("something was stored anyway: %v, %v", got, err)
	}
}

// has_subtitles rides the row read, so every list, search and share DTO gets it
// without a second query — and it follows the stored text, not subtitle_path.
func TestGet_hasTranscriptFollowsTheStoredText(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")
	// A path that names a file which does not exist must not make the flag lie.
	if err := s.SetAudioLanguage("v1", "en"); err != nil {
		t.Fatalf("set subtitle path: %v", err)
	}

	v, err := s.Get("v1")
	if err != nil || v == nil {
		t.Fatalf("get video = %v, %v", v, err)
	}
	if v.HasTranscript {
		t.Fatal("has_transcript true from a path column alone")
	}

	if err := s.SetTranscript("v1", TranscriptSourceDownload, "WEBVTT"); err != nil {
		t.Fatalf("set transcript: %v", err)
	}
	v, _ = s.Get("v1")
	if v == nil || !v.HasTranscript {
		t.Fatal("has_transcript false with text stored")
	}
}

// A tombstone keeps the transcript: that is what lets a swept video stay
// searchable and re-analysable with no file left on disk, and it is the
// guarantee #239 used to buy by sparing .vtt sidecars by name.
func TestTombstone_keepsTheTranscript(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")
	if err := s.SetDownloaded("v1", DownloadedResult{MediaPath: "chan1/v1/v1.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	if err := s.SetTranscript("v1", TranscriptSourceDownload, "WEBVTT"); err != nil {
		t.Fatalf("set transcript: %v", err)
	}

	if err := s.Tombstone("v1"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	got, err := s.GetTranscript("v1")
	if err != nil || got == nil {
		t.Fatalf("transcript gone after a tombstone: %v, %v", got, err)
	}
	v, err := s.Get("v1")
	if err != nil || v == nil || !v.HasTranscript {
		t.Fatalf("has_transcript false after a tombstone: %+v (err %v)", v, err)
	}
}

// The transcript goes with the row on a hard delete (the channel cascade), so
// nothing is left orphaned in the database the way files were on disk.
func TestDeleteVideo_cascadesToTranscript(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")
	if err := s.SetTranscript("v1", TranscriptSourceDownload, "WEBVTT"); err != nil {
		t.Fatalf("set transcript: %v", err)
	}

	if _, err := s.db.Exec(`DELETE FROM videos WHERE id = ?`, "v1"); err != nil {
		t.Fatalf("delete video: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM video_transcripts WHERE video_id = ?`, "v1").Scan(&n); err != nil {
		t.Fatalf("count transcripts: %v", err)
	}
	if n != 0 {
		t.Fatalf("transcript rows after video delete = %d, want 0 (cascade did not fire)", n)
	}
}

func TestSetTranscript_rejectsEmptyID(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetTranscript("", TranscriptSourceDownload, "WEBVTT"); err == nil {
		t.Fatal("empty id accepted, want refused")
	}
}
