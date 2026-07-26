package videos

import (
	"testing"
)

// Tests for store_sponsorblock.go: the segment-refresh claim and its writes.

// TestClaimSponsorblockStale_ordersNeverFetchedFirst covers the backfill claim
// order: a video that has never been looked up (empty
// sponsorblock_refreshed_at) has to come before one that was merely looked up
// a long time ago, since the first has no segments at all while the second
// only has slightly old ones.
func TestClaimSponsorblockStale_ordersNeverFetchedFirst(t *testing.T) {
	// Given: three downloaded videos — one fetched recently, one long ago,
	// one never.
	s := newTestStore(t)
	for _, id := range []string{"fresh", "old", "never"} {
		seedVideo(t, s, Video{ID: id, URL: "u"})
		if err := s.SetDownloaded(id, DownloadedResult{MediaPath: "/m/" + id + ".mp4"}); err != nil {
			t.Fatalf("set downloaded %s: %v", id, err)
		}
	}
	execTest(t, s, `UPDATE videos SET sponsorblock_refreshed_at = datetime('now','-90 days') WHERE id='old'`)
	execTest(t, s, `UPDATE videos SET sponsorblock_refreshed_at = '' WHERE id='never'`)

	// When: the worker claims a batch.
	got, err := s.ClaimSponsorblockStale(10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Then: the never-fetched one leads, the long-ago one follows, and the
	// freshly-stamped one is not due at all.
	if len(got) != 2 {
		t.Fatalf("claimed %+v, want the never-fetched and the stale one", got)
	}
	if got[0].ID != "never" || got[1].ID != "old" {
		t.Fatalf("claimed %+v, want never then old", got)
	}
}

// TestClaimSponsorblockStale_skipsUndownloadedAndRespectsLimit: only videos
// with media on disk are worth reading segments for, and the claim must stay
// bounded so a large library isn't pulled into memory at once.
func TestClaimSponsorblockStale_skipsUndownloadedAndRespectsLimit(t *testing.T) {
	// Given: two downloaded videos, one queued one, and one tombstoned one.
	s := newTestStore(t)
	for _, id := range []string{"d1", "d2", "queued", "gone"} {
		seedVideo(t, s, Video{ID: id, URL: "u"})
	}
	for _, id := range []string{"d1", "d2", "gone"} {
		if err := s.SetDownloaded(id, DownloadedResult{MediaPath: "/m/" + id + ".mp4"}); err != nil {
			t.Fatalf("set downloaded %s: %v", id, err)
		}
	}
	execTest(t, s, `UPDATE videos SET sponsorblock_refreshed_at = '' WHERE id IN ('d1','d2','queued','gone')`)
	if err := s.Tombstone("gone"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	// When/Then: the queued and tombstoned rows never appear, and the limit
	// holds.
	got, err := s.ClaimSponsorblockStale(1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("claimed %+v, want exactly the limit", got)
	}
	all, err := s.ClaimSponsorblockStale(10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("claimed %+v, want only the two downloaded videos", all)
	}
	for _, c := range all {
		if c.ID == "queued" || c.ID == "gone" {
			t.Fatalf("claimed %+v, want neither the queued nor the tombstoned video", all)
		}
	}
}

// TestClaimSponsorblockStale_carriesDuration: the client needs the duration to
// reject segments submitted against a different cut of the video, so the claim
// has to carry it rather than the worker looking it up again.
func TestClaimSponsorblockStale_carriesDuration(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v", URL: "u", DurationSeconds: 612})
	if err := s.SetDownloaded("v", DownloadedResult{MediaPath: "/m/v.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	execTest(t, s, `UPDATE videos SET sponsorblock_refreshed_at = '' WHERE id='v'`)

	got, err := s.ClaimSponsorblockStale(10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(got) != 1 || got[0].DurationSeconds != 612 {
		t.Fatalf("claimed %+v, want the duration carried through", got)
	}
}

// TestSetSponsorblockSegments_stampsEvenWhenEmpty: recording "this video has
// no segments" is what takes it out of the claim set. Without the stamp the
// worker would ask about the same video every minute forever.
func TestSetSponsorblockSegments_stampsEvenWhenEmpty(t *testing.T) {
	// Given: a downloaded video that has never been looked up.
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v", URL: "u"})
	if err := s.SetDownloaded("v", DownloadedResult{MediaPath: "/m/v.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	execTest(t, s, `UPDATE videos SET sponsorblock_refreshed_at = '' WHERE id='v'`)

	// When: the lookup comes back empty.
	if err := s.SetSponsorblockSegments("v", ""); err != nil {
		t.Fatalf("set segments: %v", err)
	}

	// Then: the column holds the documented empty-array shape, and the video
	// is no longer claimable.
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SponsorblockSegments != "[]" {
		t.Fatalf("segments = %q, want %q", got.SponsorblockSegments, "[]")
	}
	claimed, err := s.ClaimSponsorblockStale(10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %+v, want none after the stamp", claimed)
	}
}

// TestSetSponsorblockSegments_storesJSON is the populated case.
func TestSetSponsorblockSegments_storesJSON(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v", URL: "u"})
	segments := `[{"category":"sponsor","start_time":10,"end_time":25}]`
	if err := s.SetSponsorblockSegments("v", segments); err != nil {
		t.Fatalf("set segments: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SponsorblockSegments != segments {
		t.Fatalf("segments = %q, want %q", got.SponsorblockSegments, segments)
	}
}
