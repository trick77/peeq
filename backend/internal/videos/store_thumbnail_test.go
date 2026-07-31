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

func seedThumbVideo(t *testing.T, s *Store, id string) {
	t.Helper()
	seedVideo(t, s, Video{ID: id, URL: "https://example.test/" + id})
}

// The write guards, each of which is a caller bug rather than a video without a
// poster: nothing is stored and the error says which.
func TestSetThumbnail_guards(t *testing.T) {
	s := newTestStore(t)
	seedThumbVideo(t, s, "v1")

	for _, tc := range []struct {
		name string
		id   string
		mime string
		data []byte
	}{
		{"empty id", "", "image/jpeg", []byte("x")},
		{"empty image", "v1", "image/jpeg", nil},
		{"empty mime", "v1", "", []byte("x")},
	} {
		if err := s.SetThumbnail(tc.id, tc.mime, tc.data); err == nil {
			t.Errorf("%s accepted, want refused", tc.name)
		}
	}
	if got, err := s.GetThumbnail("v1"); err != nil || got != nil {
		t.Fatalf("something was stored anyway: %v, %v", got, err)
	}
}
