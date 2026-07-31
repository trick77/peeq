package videos

import (
	"bytes"
	"testing"
)

// A poster round-trips through the row it belongs to, and storing again
// replaces rather than duplicates — a re-download must not leave two posters or
// fail on the primary key.
func TestSetThumbnail_roundTripsAndReplaces(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")

	if err := s.SetThumbnail("v1", "image/jpeg", []byte("first")); err != nil {
		t.Fatalf("set thumbnail: %v", err)
	}
	got, err := s.GetThumbnail("v1")
	if err != nil || got == nil {
		t.Fatalf("get thumbnail = %v, %v", got, err)
	}
	if got.Mime != "image/jpeg" || !bytes.Equal(got.Bytes, []byte("first")) {
		t.Fatalf("stored %q/%q, want image/jpeg/first", got.Mime, got.Bytes)
	}
	if got.UpdatedAt == "" {
		t.Fatal("updated_at empty; conditional requests would lose Last-Modified")
	}

	if err := s.SetThumbnail("v1", "image/webp", []byte("second")); err != nil {
		t.Fatalf("replace thumbnail: %v", err)
	}
	got, err = s.GetThumbnail("v1")
	if err != nil || got == nil {
		t.Fatalf("get after replace = %v, %v", got, err)
	}
	if got.Mime != "image/webp" || !bytes.Equal(got.Bytes, []byte("second")) {
		t.Fatalf("after replace %q/%q, want image/webp/second", got.Mime, got.Bytes)
	}
}

// A video with no stored poster reads as (nil, nil), not an error: "no poster"
// is an ordinary state, and the handler turns it into a 404.
func TestGetThumbnail_missingIsNotAnError(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")

	got, err := s.GetThumbnail("v1")
	if err != nil {
		t.Fatalf("get thumbnail: %v", err)
	}
	if got != nil {
		t.Fatalf("got %+v, want nil for a video with no poster", got)
	}
}

// An oversized image is declined whole. Truncating would store a broken image
// that renders as a broken card, which is worse than the gradient placeholder.
func TestSetThumbnail_declinesOversized(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")

	if err := s.SetThumbnail("v1", "image/jpeg", make([]byte, MaxThumbnailBytes+1)); err == nil {
		t.Fatal("oversized thumbnail accepted, want refused")
	}
	got, err := s.GetThumbnail("v1")
	if err != nil {
		t.Fatalf("get thumbnail: %v", err)
	}
	if got != nil {
		t.Fatal("oversized thumbnail was stored anyway")
	}
}

// has_thumbnail rides on the row read, so every list, search and share DTO gets
// it without a second query — and it answers from the stored bytes, never from
// thumbnail_path.
func TestGet_hasThumbnailFollowsTheStoredBytes(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")

	v, err := s.Get("v1")
	if err != nil || v == nil {
		t.Fatalf("get video = %v, %v", v, err)
	}
	if v.HasThumbnail {
		t.Fatal("has_thumbnail true with no poster stored")
	}

	if err := s.SetThumbnail("v1", "image/jpeg", []byte("bytes")); err != nil {
		t.Fatalf("set thumbnail: %v", err)
	}
	v, err = s.Get("v1")
	if err != nil || v == nil {
		t.Fatalf("get video = %v, %v", v, err)
	}
	if !v.HasThumbnail {
		t.Fatal("has_thumbnail false with a poster stored")
	}
}

// The import worker's candidate query: a video is a candidate exactly while it
// has no stored poster, whatever its thumbnail_path says. A blanked pointer must
// NOT disqualify a row — that row is the whole reason the worker exists.
func TestThumbnaillessVideos_selectsByStoredBytesNotPath(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "withPoster")
	seedThumbVideo(t, s, "blankedPath")
	if err := s.SetThumbnail("withPoster", "image/jpeg", []byte("x")); err != nil {
		t.Fatalf("set thumbnail: %v", err)
	}

	got, err := s.ThumbnaillessVideos(10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "blankedPath" {
		t.Fatalf("candidates = %+v, want only blankedPath", got)
	}
}

// The poster goes with the row on a hard delete. The FK cascade does this (see
// store.Open's foreign_keys pragma); this pins that it actually fires, because a
// pragma that silently defaulted off would leave orphan blobs behind forever.
func TestDeleteVideo_cascadesToThumbnail(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")
	if err := s.SetThumbnail("v1", "image/jpeg", []byte("x")); err != nil {
		t.Fatalf("set thumbnail: %v", err)
	}

	if _, err := s.db.Exec(`DELETE FROM videos WHERE id = ?`, "v1"); err != nil {
		t.Fatalf("delete video: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM video_thumbnails WHERE video_id = ?`, "v1").Scan(&n); err != nil {
		t.Fatalf("count thumbnails: %v", err)
	}
	if n != 0 {
		t.Fatalf("thumbnail rows after video delete = %d, want 0 (cascade did not fire)", n)
	}
}

// The regression that motivated moving posters into the database: a
// metadata-only Upsert — a channel scan, the inbox caption fetcher, add-by-URL —
// used to blank thumbnail_path on rows it touched, orphaning a perfectly good
// image and leaving a tombstoned video with no way to ever get it back. Filling
// a hole is fine; punching one is not.
func TestUpsert_neverBlanksThumbnailPath(t *testing.T) {
	s := newTestStore(t)
	const path = "chan1/v1/v1.webp"
	if err := s.Upsert(Video{ID: "v1", URL: "u", ThumbnailPath: path}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := s.Upsert(Video{ID: "v1", URL: "u", Title: "seen by a scan"}); err != nil {
		t.Fatalf("metadata-only upsert: %v", err)
	}
	v, err := s.Get("v1")
	if err != nil || v == nil {
		t.Fatalf("get video = %v, %v", v, err)
	}
	if v.ThumbnailPath != path {
		t.Fatalf("thumbnail_path = %q, want kept as %q", v.ThumbnailPath, path)
	}

	// A caller that DOES have a poster to offer still wins — the guard must not
	// freeze the column at its first value.
	if err := s.Upsert(Video{ID: "v1", URL: "u", ThumbnailPath: "chan1/v1/v1.jpg"}); err != nil {
		t.Fatalf("upsert with a new path: %v", err)
	}
	v, err = s.Get("v1")
	if err != nil || v == nil {
		t.Fatalf("get video = %v, %v", v, err)
	}
	if v.ThumbnailPath != "chan1/v1/v1.jpg" {
		t.Fatalf("thumbnail_path = %q, want updated", v.ThumbnailPath)
	}
}

// seedThumbVideo inserts a minimal row for id — the shared seedVideo takes a
// whole Video, and every test here only needs the row to exist.
func seedThumbVideo(t *testing.T, s *Store, id string) {
	t.Helper()
	seedVideo(t, s, Video{ID: id, URL: "https://example.test/" + id})
}
