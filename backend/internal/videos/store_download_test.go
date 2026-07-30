package videos

import (
	"testing"
)

// Tests for store_download.go: the download lifecycle (status, requested
// format, the successful-download write, and tombstoning).

func TestSetStatus_setsErrorMessage(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetStatus("v", "error", "video is private"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "error" {
		t.Fatalf("status = %q, want error", got.Status)
	}
	if got.ErrorMessage != "video is private" {
		t.Fatalf("error_message = %q, want %q", got.ErrorMessage, "video is private")
	}
}

func TestTombstone_clearsMediaPathSetsStatusKeepsRow(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetDownloaded("v", DownloadedResult{MediaPath: "/media/v.mp4", FilesizeBytes: 10}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	if err := s.Tombstone("v"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("row was deleted, want kept")
	}
	if got.Status != "tombstoned" {
		t.Fatalf("status = %q, want tombstoned", got.Status)
	}
	if got.MediaPath != "" {
		t.Fatalf("media_path = %q, want cleared", got.MediaPath)
	}
}

// TestTombstoneKeepsSubtitlePathAndSummary pins what a tombstone remembers:
// only media_path is cleared. subtitle_path stays because the .vtt stays — it
// is the only source a transcript can be re-chunked or re-embedded from, and
// the DTO derives has_subtitles from it, so clearing it also hid the transcript
// view on a video that still had one.
func TestTombstoneKeepsSubtitlePathAndSummary(t *testing.T) {
	s := New(openTestDB(t))
	const id = "vid1"
	if err := s.Upsert(Video{ID: id, URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetDownloaded(id, DownloadedResult{
		MediaPath:       "/media/vid1.mp4",
		FilesizeBytes:   10,
		SubtitleRelPath: "vid1.en.vtt",
	}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	if err := s.SetSummary(id, "a summary", `[]`, `[]`); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	if err := s.Tombstone(id); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	v, err := s.Get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.SubtitlePath != "vid1.en.vtt" {
		t.Errorf("subtitle_path = %q, want kept after tombstone", v.SubtitlePath)
	}
	if v.MediaPath != "" {
		t.Errorf("media_path = %q, want empty after tombstone", v.MediaPath)
	}
	if v.Summary != "a summary" {
		t.Errorf("summary = %q, want kept after tombstone", v.Summary)
	}
	if v.Status != "tombstoned" {
		t.Errorf("status = %q, want tombstoned", v.Status)
	}
}

func TestSetDownloaded_recordsResult(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// A prior error should be cleared by a successful download.
	if err := s.SetStatus("v", "error", "was rate limited"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if err := s.SetDownloaded("v", DownloadedResult{
		MediaPath:            "/media/chan/v/v.mp4",
		ThumbnailPath:        "/media/chan/v/v.jpg",
		FilesizeBytes:        123456,
		FormatUsed:           "bv*+ba",
		SponsorblockSegments: `[{"category":"sponsor"}]`,
	}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "downloaded" {
		t.Fatalf("status = %q, want downloaded", got.Status)
	}
	if got.MediaPath != "/media/chan/v/v.mp4" {
		t.Fatalf("media_path = %q", got.MediaPath)
	}
	if got.FilesizeBytes != 123456 {
		t.Fatalf("filesize = %d, want 123456", got.FilesizeBytes)
	}
	if got.FormatUsed != "bv*+ba" {
		t.Fatalf("format_used = %q", got.FormatUsed)
	}
	if got.SponsorblockSegments != `[{"category":"sponsor"}]` {
		t.Fatalf("sponsorblock = %q", got.SponsorblockSegments)
	}
	if got.ErrorMessage != "" {
		t.Fatalf("error_message = %q, want cleared", got.ErrorMessage)
	}
	if got.DownloadedAt == "" {
		t.Fatalf("downloaded_at not stamped")
	}
	if got.ThumbnailPath != "/media/chan/v/v.jpg" {
		t.Fatalf("thumbnail_path = %q", got.ThumbnailPath)
	}
}

// TestSweepCandidates_filtersByWatchedFavoriteStatusAndCutoff exercises the
// retention sweeper's underlying query directly: only a downloaded, watched,
// non-favorite video whose watched_at is strictly before cutoff comes back —
// there is nothing to reclaim from a video that has no file.
func TestSweepCandidates_filtersByWatchedFavoriteStatusAndCutoff(t *testing.T) {
	db := openTestDB(t)
	s := New(db)

	seed := func(id string, watched, favorite bool, watchedAt, status string) {
		t.Helper()
		if err := s.Upsert(Video{ID: id, URL: "u-" + id}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
		watchedInt := 0
		if watched {
			watchedInt = 1
		}
		favInt := 0
		if favorite {
			favInt = 1
		}
		_, err := db.Exec(
			`UPDATE videos SET watched = ?, favorite = ?, watched_at = ?, status = ? WHERE id = ?`,
			watchedInt, favInt, nullStr(watchedAt), status, id,
		)
		if err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	const cutoff = "2026-01-01 00:00:00"
	seed("old-eligible", true, false, "2025-01-01 00:00:00", "downloaded")   // before cutoff, watched, not fav -> candidate
	seed("old-favorite", true, true, "2025-01-01 00:00:00", "downloaded")    // favorite -> excluded
	seed("unwatched", false, false, "", "downloaded")                        // not watched -> excluded
	seed("old-tombstoned", true, false, "2025-01-01 00:00:00", "tombstoned") // already gone -> excluded
	seed("recent", true, false, "2026-06-01 00:00:00", "downloaded")         // after cutoff -> excluded
	// No file to reclaim, so not a candidate however long ago it was watched.
	// This is the re-download of a long-ago-watched video, mid-flight: the loose
	// status != 'tombstoned' form used to match it and flip it straight back to
	// 'tombstoned' while its job was still queued.
	seed("requeued", true, false, "2025-01-01 00:00:00", "queued")
	seed("failed", true, false, "2025-01-01 00:00:00", "error")

	got, err := s.SweepCandidates(cutoff)
	if err != nil {
		t.Fatalf("sweep candidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "old-eligible" {
		ids := make([]string, len(got))
		for i, v := range got {
			ids[i] = v.ID
		}
		t.Fatalf("sweep candidates = %v, want [old-eligible]", ids)
	}
}

// TestSetDownloaded_fillsPublishedAt asserts the download's own info.json
// supplies the release date for videos seeded from a metadata-poor flat
// channel listing — without it, everything peeq auto-downloads would sort by
// download date forever.
func TestSetDownloaded_fillsPublishedAt(t *testing.T) {
	// Given: a row seeded the way scan.Scheduler.enqueueAuto seeds one — no
	// release date.
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "auto", URL: "u"})

	// When: the download completes and reports one.
	if err := s.SetDownloaded("auto", DownloadedResult{MediaPath: "/m/auto.mp4", PublishedAt: "2025-04-09"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	// Then: the row carries it.
	got, err := s.Get("auto")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PublishedAt != "2025-04-09" {
		t.Fatalf("published_at = %q, want 2025-04-09", got.PublishedAt)
	}
}

// TestSetDownloaded_emptyPublishedAt_keepsExisting asserts a re-download of a
// video whose release date is already known (the manual-add path fetches it
// up front) never blanks it out when yt-dlp reports no upload_date.
func TestSetDownloaded_emptyPublishedAt_keepsExisting(t *testing.T) {
	// Given: a row that already knows its release date.
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "known", URL: "u", PublishedAt: "2025-04-09"})

	// When: a download completes without one.
	if err := s.SetDownloaded("known", DownloadedResult{MediaPath: "/m/known.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	// Then: the stored date survives.
	got, err := s.Get("known")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PublishedAt != "2025-04-09" {
		t.Fatalf("published_at = %q, want it preserved", got.PublishedAt)
	}
}

// TestSetDownloaded_storesYouTubeMetadata covers the columns migration 0009
// added: they arrive from the download's own info.json, and an empty value
// leaves what is stored rather than wiping it (a re-download whose extractor
// omitted tags must not erase the ones already there).
func TestSetDownloaded_storesYouTubeMetadata(t *testing.T) {
	// Given: a fresh row.
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v1", URL: "u"})

	// When: a download reports the full set.
	if err := s.SetDownloaded("v1", DownloadedResult{
		MediaPath: "/m/v1.mp4", Description: "desc", MediaType: "short",
		LiveStatus: "not_live", YTTags: `["physics","education"]`,
		YTCategories: `["Science & Technology"]`,
	}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	got, err := s.Get("v1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Description != "desc" || got.MediaType != "short" || got.LiveStatus != "not_live" {
		t.Fatalf("got %q/%q/%q, want desc/short/not_live", got.Description, got.MediaType, got.LiveStatus)
	}
	if got.YTTags != `["physics","education"]` || got.YTCategories != `["Science & Technology"]` {
		t.Fatalf("tags/categories = %q/%q", got.YTTags, got.YTCategories)
	}

	// Then: a later download reporting none of them keeps the stored values.
	if err := s.SetDownloaded("v1", DownloadedResult{MediaPath: "/m/v1.mp4"}); err != nil {
		t.Fatalf("re-download: %v", err)
	}
	got, err = s.Get("v1")
	if err != nil {
		t.Fatalf("get after re-download: %v", err)
	}
	if got.YTTags != `["physics","education"]` || got.Description != "desc" || got.MediaType != "short" {
		t.Fatalf("re-download wiped metadata: %+v", got)
	}
}

// TestSetDownloaded_stampsSponsorblockRefresh: yt-dlp already asked
// SponsorBlock during the download, so the backfill worker must not
// immediately ask again for a video whose segments just arrived.
func TestSetDownloaded_stampsSponsorblockRefresh(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v", URL: "u"})
	if err := s.SetDownloaded("v", DownloadedResult{
		MediaPath:            "/m/v.mp4",
		SponsorblockSegments: `[{"category":"sponsor","start_time":1,"end_time":2}]`,
	}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	claimed, err := s.ClaimSponsorblockStale(10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %+v, want a just-downloaded video not to be re-fetched", claimed)
	}
}

func TestTombstone_revokesShareLink(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	if err := s.Upsert(Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	// A live share link for the video...
	if _, err := db.Exec(`INSERT INTO share_links (token, video_id) VALUES (?, ?)`, "tok", "v1"); err != nil {
		t.Fatalf("seed share link: %v", err)
	}
	if err := s.Tombstone("v1"); err != nil {
		t.Fatalf("Tombstone: %v", err)
	}
	// ...must be gone once the video is tombstoned.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM share_links WHERE video_id = ?`, "v1").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("share link survived a tombstone (count=%d)", n)
	}
	// The video row itself stays, marked tombstoned.
	v, err := s.Get("v1")
	if err != nil || v == nil || v.Status != "tombstoned" {
		t.Fatalf("Get after tombstone = (%+v, %v), want status tombstoned", v, err)
	}
}

func TestTombstone_errorsWhenShareTableMissing(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	if err := s.Upsert(Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE share_links`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if err := s.Tombstone("v1"); err == nil {
		t.Fatal("Tombstone should surface the revoke-link failure")
	}
}
