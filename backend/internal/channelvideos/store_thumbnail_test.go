package channelvideos

import (
	"bytes"
	"testing"
)

// A cached inbox poster round-trips through the ledger row, and a re-fetch
// replaces rather than duplicating.
func TestSetThumbnail_roundTripsAndReplaces(t *testing.T) {
	st := newTestStore(t)
	seedPendingEntry(t, st, "UC1", "p1")

	if err := st.SetThumbnail("p1", "image/jpeg", []byte("first")); err != nil {
		t.Fatalf("set thumbnail: %v", err)
	}
	got, err := st.GetThumbnail("p1")
	if err != nil || got == nil {
		t.Fatalf("get thumbnail = %v, %v", got, err)
	}
	if got.Mime != "image/jpeg" || !bytes.Equal(got.Bytes, []byte("first")) {
		t.Fatalf("stored %q/%q, want image/jpeg/first", got.Mime, got.Bytes)
	}

	if err := st.SetThumbnail("p1", "image/webp", []byte("second")); err != nil {
		t.Fatalf("replace thumbnail: %v", err)
	}
	got, _ = st.GetThumbnail("p1")
	if got == nil || !bytes.Equal(got.Bytes, []byte("second")) {
		t.Fatalf("after replace = %+v, want the second image", got)
	}
}

// An item that has never been fetched reads as (nil, nil) — the ordinary state
// for a freshly scanned inbox card, and what makes the serve path fetch on
// demand rather than 404.
func TestGetThumbnail_missingIsNotAnError(t *testing.T) {
	st := newTestStore(t)
	seedPendingEntry(t, st, "UC1", "p1")

	got, err := st.GetThumbnail("p1")
	if err != nil {
		t.Fatalf("get thumbnail: %v", err)
	}
	if got != nil {
		t.Fatalf("got %+v, want nil", got)
	}
}

// DeleteThumbnail is the PRIMARY reclaim path: approve, queue and ignore all
// leave the ledger row in place with a new state, so the FK cascade never fires
// on those transitions and only this frees the space.
func TestDeleteThumbnail_reclaimsWhileTheRowSurvives(t *testing.T) {
	st := newTestStore(t)
	seedPendingEntry(t, st, "UC1", "p1")
	if err := st.SetThumbnail("p1", "image/jpeg", []byte("x")); err != nil {
		t.Fatalf("set thumbnail: %v", err)
	}
	if err := st.SetState("p1", StateIgnored); err != nil {
		t.Fatalf("ignore: %v", err)
	}

	// The state change alone leaves the poster behind — that is the leak this
	// delete exists to close.
	if got, err := st.GetThumbnail("p1"); err != nil || got == nil {
		t.Fatalf("poster vanished on a state change: %v, %v", got, err)
	}
	if err := st.DeleteThumbnail("p1"); err != nil {
		t.Fatalf("delete thumbnail: %v", err)
	}
	if got, err := st.GetThumbnail("p1"); err != nil || got != nil {
		t.Fatalf("poster survived the delete: %v, %v", got, err)
	}
}

// Deleting the channel takes the cached posters with it. This is what the
// cascade is for: .pending/<id>/ used to be orphaned by a channel delete
// exactly the way .channels/<id>/ always was.
func TestDeleteChannel_cascadesToPendingThumbnails(t *testing.T) {
	st := newTestStore(t)
	seedPendingEntry(t, st, "UC1", "p1")
	if err := st.SetThumbnail("p1", "image/jpeg", []byte("x")); err != nil {
		t.Fatalf("set thumbnail: %v", err)
	}

	if _, err := st.db.Exec(`DELETE FROM channels WHERE id = ?`, "UC1"); err != nil {
		t.Fatalf("delete channel: %v", err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM pending_thumbnails WHERE video_id = ?`, "p1").Scan(&n); err != nil {
		t.Fatalf("count thumbnails: %v", err)
	}
	if n != 0 {
		t.Fatalf("thumbnail rows after channel delete = %d, want 0 (cascade did not fire)", n)
	}
}

func seedPendingEntry(t *testing.T, st *Store, channelID, videoID string) {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM channels WHERE id = ?`, channelID).Scan(&n); err != nil {
		t.Fatalf("count channels: %v", err)
	}
	if n == 0 {
		seedChannel(t, st, channelID)
	}
	if err := st.Insert(Entry{
		VideoID: videoID, ChannelID: channelID, Title: "A video",
		URL: "https://www.youtube.com/watch?v=" + videoID, State: StatePending,
	}); err != nil {
		t.Fatalf("seed ledger row %s: %v", videoID, err)
	}
}

// The write guards. Each is a caller bug rather than an inbox item without a
// poster, so nothing is stored and the error says which.
func TestSetPendingThumbnail_guards(t *testing.T) {
	st := newTestStore(t)
	seedPendingEntry(t, st, "UC1", "p1")

	for _, tc := range []struct {
		name string
		id   string
		mime string
		data []byte
	}{
		{"empty id", "", "image/jpeg", []byte("x")},
		{"empty image", "p1", "image/jpeg", nil},
		{"empty mime", "p1", "", []byte("x")},
		{"oversize", "p1", "image/jpeg", make([]byte, MaxThumbnailBytes+1)},
	} {
		if err := st.SetThumbnail(tc.id, tc.mime, tc.data); err == nil {
			t.Errorf("%s accepted, want refused", tc.name)
		}
	}
	if got, err := st.GetThumbnail("p1"); err != nil || got != nil {
		t.Fatalf("something was stored anyway: %v, %v", got, err)
	}
}
