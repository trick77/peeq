package channels

import (
	"bytes"
	"testing"
)

// Artwork round-trips through the row it belongs to, and a refresh replaces
// rather than duplicating — a weekly re-fetch must not fail on the primary key
// or leave two copies.
func TestSetImage_roundTripsAndReplaces(t *testing.T) {
	s := newTestStore(t)
	seedImageChannel(t, s, "UC1")

	if err := s.SetImage("UC1", ImageAvatar, "image/jpeg", []byte("first")); err != nil {
		t.Fatalf("set avatar: %v", err)
	}
	got, err := s.GetImage("UC1", ImageAvatar)
	if err != nil || got == nil {
		t.Fatalf("get avatar = %v, %v", got, err)
	}
	if got.Mime != "image/jpeg" || !bytes.Equal(got.Bytes, []byte("first")) {
		t.Fatalf("stored %q/%q, want image/jpeg/first", got.Mime, got.Bytes)
	}

	if err := s.SetImage("UC1", ImageAvatar, "image/webp", []byte("second")); err != nil {
		t.Fatalf("replace avatar: %v", err)
	}
	got, err = s.GetImage("UC1", ImageAvatar)
	if err != nil || got == nil {
		t.Fatalf("get after replace = %v, %v", got, err)
	}
	if got.Mime != "image/webp" || !bytes.Equal(got.Bytes, []byte("second")) {
		t.Fatalf("after replace %q/%q, want image/webp/second", got.Mime, got.Bytes)
	}
}

// The two kinds are independent: storing a banner must not disturb the avatar,
// and asking for a kind that was never stored is (nil, nil) rather than an
// error — a channel with no banner is ordinary, not a fault.
func TestGetImage_kindsAreIndependent(t *testing.T) {
	s := newTestStore(t)
	seedImageChannel(t, s, "UC1")
	if err := s.SetImage("UC1", ImageAvatar, "image/jpeg", []byte("a")); err != nil {
		t.Fatalf("set avatar: %v", err)
	}

	banner, err := s.GetImage("UC1", ImageBanner)
	if err != nil {
		t.Fatalf("get banner: %v", err)
	}
	if banner != nil {
		t.Fatalf("banner = %+v, want nil", banner)
	}
	if avatar, aerr := s.GetImage("UC1", ImageAvatar); aerr != nil || avatar == nil {
		t.Fatalf("avatar = %v, %v — storing one kind disturbed the other", avatar, aerr)
	}
}

// An oversized image is declined whole rather than truncated: half an image
// renders as a broken one, which is worse than none.
func TestSetImage_declinesOversizedAndUnknownKind(t *testing.T) {
	s := newTestStore(t)
	seedImageChannel(t, s, "UC1")

	if err := s.SetImage("UC1", ImageAvatar, "image/jpeg", make([]byte, MaxImageBytes+1)); err == nil {
		t.Fatal("oversized image accepted, want refused")
	}
	if err := s.SetImage("UC1", "header", "image/jpeg", []byte("x")); err == nil {
		t.Fatal("unknown kind accepted, want refused")
	}
	if got, err := s.GetImage("UC1", ImageAvatar); err != nil || got != nil {
		t.Fatalf("something was stored anyway: %v, %v", got, err)
	}
}

// The import worker's candidate query: one row per channel/kind that has a
// recorded path but no stored bytes, and a kind stops being a candidate the
// moment its image is in.
func TestImagelessChannels_selectsRecordedPathsWithoutBytes(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Channel{
		ID: "UC1", Name: "Both", AvatarPath: ".channels/UC1/avatar.jpg",
		BannerPath: ".channels/UC1/banner.jpg",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// No paths recorded at all: nothing to look for, so not a candidate.
	if err := s.Upsert(Channel{ID: "UC2", Name: "Bare"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := s.ImagelessChannels(10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("candidates = %+v, want the two UC1 kinds", got)
	}
	for _, c := range got {
		if c.ChannelID != "UC1" || c.Path == "" {
			t.Fatalf("candidate %+v: want a UC1 kind with a recorded path", c)
		}
	}

	if err := s.SetImage("UC1", ImageAvatar, "image/jpeg", []byte("a")); err != nil {
		t.Fatalf("set avatar: %v", err)
	}
	got, err = s.ImagelessChannels(10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(got) != 1 || got[0].Kind != ImageBanner {
		t.Fatalf("candidates = %+v, want only the banner", got)
	}
}

// Artwork goes with the channel. Nothing ever deleted .channels/<id>/ from
// disk, so this cascade is the leak's replacement rather than a nicety.
func TestDeleteChannel_cascadesToImages(t *testing.T) {
	s := newTestStore(t)
	seedImageChannel(t, s, "UC1")
	if err := s.SetImage("UC1", ImageAvatar, "image/jpeg", []byte("a")); err != nil {
		t.Fatalf("set avatar: %v", err)
	}

	if _, err := s.db.Exec(`DELETE FROM channels WHERE id = ?`, "UC1"); err != nil {
		t.Fatalf("delete channel: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM channel_images WHERE channel_id = ?`, "UC1").Scan(&n); err != nil {
		t.Fatalf("count images: %v", err)
	}
	if n != 0 {
		t.Fatalf("image rows after channel delete = %d, want 0 (cascade did not fire)", n)
	}
}

// seedImageChannel inserts a bare channel row for id.
func seedImageChannel(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.Upsert(Channel{ID: id, Name: "A channel"}); err != nil {
		t.Fatalf("seed channel %s: %v", id, err)
	}
}
