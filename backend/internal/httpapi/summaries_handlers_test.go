package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/videos"
)

// summariesTestDeps builds Deps wired for the summaries API: dev auth plus a
// summaryjobs store (as SummaryList) and a videos store sharing one test DB.
// It also returns the backing *sql.DB so a test can drop the summary_jobs
// table to force a store-level error out of an otherwise normal flow.
func summariesTestDeps(t *testing.T) (Deps, *summaryjobs.Store, *videos.Store, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	sj := summaryjobs.New(db)
	vids := videos.New(db)
	return Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		Settings:       settings.New(db),
		Videos:         vids,
		SummaryList:    sj,
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}, sj, vids, db
}

func getSummaries(t *testing.T, h http.Handler, sessionCookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/summaries", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestSummaries_listJoinsTitleAndSkipsTerminal is the core Queue-lane flow:
// only pending/running jobs come back, joined with their video's title and
// channel for display.
func TestSummaries_listJoinsTitleAndSkipsTerminal(t *testing.T) {
	deps, sj, vids, _ := summariesTestDeps(t)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	if err := vids.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "First summary", ChannelName: "Chan One"}); err != nil {
		t.Fatal(err)
	}
	if err := vids.Upsert(videos.Video{ID: "v2", URL: "u2", Title: "Done summary", ChannelName: "Chan Two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := sj.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	id2, err := sj.Enqueue("v2")
	if err != nil {
		t.Fatal(err)
	}
	if err := sj.Finish(id2, "done", ""); err != nil {
		t.Fatal(err)
	}

	rec := getSummaries(t, h, sessionCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/summaries status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var items []summaryItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1 (only the in-flight job): %+v", len(items), items)
	}
	if items[0].VideoID != "v1" || items[0].Title != "First summary" || items[0].ChannelName != "Chan One" {
		t.Fatalf("item not joined with its video: %+v", items[0])
	}
	if items[0].State != "pending" {
		t.Fatalf("state = %q, want pending", items[0].State)
	}
}

// TestSummaries_listEmptyWhenUnwired mirrors handleDownloadsList: no store
// wired reports an empty queue (200 + []), never 503.
func TestSummaries_listEmptyWhenUnwired(t *testing.T) {
	deps, _, _, _ := summariesTestDeps(t)
	deps.SummaryList = nil
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	rec := getSummaries(t, h, sessionCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var items []summaryItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d items, want empty list", len(items))
	}
}

// TestSummaries_listStoreError covers the serverError branch: a wired store
// whose query fails must surface a 500, not a partial/empty 200.
func TestSummaries_listStoreError(t *testing.T) {
	deps, _, _, db := summariesTestDeps(t)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	// Drop the table out from under the store so ListActive's query errors.
	if _, err := db.Exec(`DROP TABLE summary_jobs`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	rec := getSummaries(t, h, sessionCookie)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
}
