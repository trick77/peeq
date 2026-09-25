package httpapi

import (
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/videos"
)

func TestVideoIndex_noStoreIsEmpty(t *testing.T) {
	s := &server{}
	index, err := s.videoIndex([]string{"a", "b"})
	if err != nil || len(index) != 0 {
		t.Fatalf("index = %v err=%v, want empty and nil", index, err)
	}
	s = &server{videos: videos.New(openTestDB(t))}
	index, err = s.videoIndex(nil)
	if err != nil || len(index) != 0 {
		t.Fatalf("no ids: index = %v err=%v, want empty and nil", index, err)
	}
}

// breakVideosReads makes every read of the videos table fail without
// touching the job tables that reference it (dropping the table would
// cascade the jobs away and the list would simply be empty).
func breakVideosReads(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`ALTER TABLE videos RENAME COLUMN title TO title_x`); err != nil {
		t.Fatalf("rename column: %v", err)
	}
}

// TestDownloads_listVideoIndexError_500 pins the one fault policy for the
// batched join: a list with every title blank and nothing saying why is a
// silent half-answer, so the read failure is a 500.
func TestDownloads_listVideoIndexError_500(t *testing.T) {
	logs := captureLogs(t)
	deps, db := downloadsTestDepsDB(t, &fakeDownloadsRunner{})
	if _, err := deps.Videos.EnqueueDownload("v1", 0); err == nil {
		t.Fatal("expected ErrNotFound for an unknown video")
	}
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Videos.EnqueueDownload("v1", 0); err != nil {
		t.Fatal(err)
	}
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	breakVideosReads(t, db)
	rec := doRequest(t, h, sessionCookie, http.MethodGet, "/api/downloads")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logs.String(), "video index") {
		t.Fatalf("log should carry the cause, got: %s", logs.String())
	}
}

func TestSummaries_listVideoIndexError_500(t *testing.T) {
	deps, sj, vids, db := summariesTestDeps(t)
	if err := vids.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sj.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	breakVideosReads(t, db)
	rec := getSummaries(t, h, sessionCookie)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}
