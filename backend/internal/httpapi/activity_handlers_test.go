package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/videos"
)

func activityTestDeps(t *testing.T) (Deps, *activity.Store, *channels.Store, *jobs.Store, *videos.Store, *summaryjobs.Store, *sql.DB) {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	act := activity.New(db)
	ch := channels.New(db)
	jb := jobs.New(db)
	vids := videos.New(db)
	sj := summaryjobs.New(db)
	return Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		Settings:       settings.New(db),
		Activity:       act,
		Channels:       ch,
		Jobs:           jb,
		Videos:         vids,
		SummaryList:    sj,
		DevAuthClaims: auth.Claims{
			Subject: "dev-tester", PreferredUsername: "dev",
			Email: "dev@example.local", Name: "Dev Tester",
		},
	}, act, ch, jb, vids, sj, db
}

func getActivityJSON(t *testing.T, h http.Handler, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestActivity_listReturnsNewestFirstWithRetainedMax(t *testing.T) {
	deps, act, _, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	act.Record(activity.Event{Kind: activity.KindScan, Outcome: activity.OutcomeOK, Subject: "Veritasium", Summary: "3 new"})
	act.Record(activity.Event{Kind: activity.KindDownload, Outcome: activity.OutcomeOK, Subject: "A clip"})

	rec := getActivityJSON(t, h, cookie, "/api/activity")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp activityListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Events) != 2 || resp.Events[0].Subject != "A clip" {
		t.Fatalf("events = %+v", resp.Events)
	}
	if resp.HasMore {
		t.Fatal("has_more should be false")
	}
	if resp.RetainedMax != 2000 {
		t.Fatalf("retained_max = %d, want 2000", resp.RetainedMax)
	}
}

func TestActivity_listEmptyIsArrayNotNull(t *testing.T) {
	deps, _, _, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := getActivityJSON(t, h, cookie, "/api/activity")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	// The events field must serialize as [] so the client can map over it.
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatal("invalid json")
	}
	var raw map[string]json.RawMessage
	json.Unmarshal(rec.Body.Bytes(), &raw)
	if string(raw["events"]) != "[]" {
		t.Fatalf("events = %s, want []", raw["events"])
	}
}

func TestActivity_list503WhenUnwired(t *testing.T) {
	deps, _, _, _, _, _, _ := activityTestDeps(t)
	deps.Activity = nil
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := getActivityJSON(t, h, cookie, "/api/activity")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestActivity_upcomingProjectsPendingAndScheduled(t *testing.T) {
	deps, _, ch, jb, vids, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// A subscribed channel (scheduled scan) and a pending download (ordered).
	if err := ch.Upsert(channels.Channel{ID: "UCx", Name: "Veritasium"}); err != nil {
		t.Fatal(err)
	}
	if err := ch.Track("UCx", "2026-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Subscribe("UCx", "2026-07-25 12:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := vids.Upsert(videos.Video{ID: "v1", URL: "u", Title: "Queued clip"}); err != nil {
		t.Fatal(err)
	}
	if _, err := jb.Enqueue("v1", 10); err != nil {
		t.Fatal(err)
	}

	rec := getActivityJSON(t, h, cookie, "/api/activity/upcoming")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp upcomingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// Three items: the pending download (ordered, no time → sorts first), then
	// the scheduled scan, then the metadata refresh (Subscribe seeds
	// next_meta_refresh_at a week out). Ordered-before-timed, then by time.
	if len(resp.Items) != 3 {
		t.Fatalf("items = %+v", resp.Items)
	}
	if resp.Items[0].Kind != activity.KindDownload || resp.Items[0].Subject != "Queued clip" {
		t.Fatalf("first item = %+v, want the pending download", resp.Items[0])
	}
	if resp.Items[1].Kind != activity.KindScan || resp.Items[1].Subject != "Veritasium" {
		t.Fatalf("second item = %+v, want the scheduled scan", resp.Items[1])
	}
	if resp.Items[2].Kind != activity.KindChannelMeta {
		t.Fatalf("third item = %+v, want the metadata refresh", resp.Items[2])
	}
}

func TestActivity_listHonoursBeforeAndLimit(t *testing.T) {
	deps, act, _, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	for i := 0; i < 5; i++ {
		act.Record(activity.Event{Kind: activity.KindScan, Outcome: activity.OutcomeOK})
	}
	// Newest page of 2 → has_more.
	rec := getActivityJSON(t, h, cookie, "/api/activity?limit=2")
	var first activityListResponse
	json.Unmarshal(rec.Body.Bytes(), &first)
	if len(first.Events) != 2 || !first.HasMore {
		t.Fatalf("first page: %d events, has_more=%v", len(first.Events), first.HasMore)
	}
	// Page back from the last id.
	before := first.Events[1].ID
	rec2 := getActivityJSON(t, h, cookie, "/api/activity?before="+strconv.FormatInt(before, 10)+"&limit=2")
	var second activityListResponse
	json.Unmarshal(rec2.Body.Bytes(), &second)
	if len(second.Events) != 2 || second.Events[0].ID >= before {
		t.Fatalf("second page not older than %d: %+v", before, second.Events)
	}
}

func TestActivity_listClampsLimit(t *testing.T) {
	deps, _, _, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	// An oversized limit must still 200 (clamped server-side, not rejected).
	rec := getActivityJSON(t, h, cookie, "/api/activity?limit=999")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestActivity_listStoreErrorIs500(t *testing.T) {
	deps, _, _, _, _, _, db := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	if _, err := db.Exec(`DROP TABLE activity_events`); err != nil {
		t.Fatal(err)
	}
	rec := getActivityJSON(t, h, cookie, "/api/activity")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestActivity_upcomingEmptyIsArray(t *testing.T) {
	deps, _, _, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	// No channels/jobs/summaries seeded → merged is nil and must serialize as [].
	rec := getActivityJSON(t, h, cookie, "/api/activity/upcoming")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var raw map[string]json.RawMessage
	json.Unmarshal(rec.Body.Bytes(), &raw)
	if string(raw["items"]) != "[]" {
		t.Fatalf("items = %s, want []", raw["items"])
	}
}

func TestActivity_upcomingFallsBackToVideoIDAndListsSummaries(t *testing.T) {
	deps, _, _, jb, vids, sj, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	// A pending download whose video row has no title → subject falls back to the
	// video id; and a pending summary → the summary lane of the projection. Both
	// jobs FK-reference videos, so the rows must exist (with empty titles).
	if err := vids.Upsert(videos.Video{ID: "no-title-vid", URL: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := vids.Upsert(videos.Video{ID: "summ-vid", URL: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, err := jb.Enqueue("no-title-vid", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := sj.Enqueue("summ-vid"); err != nil {
		t.Fatal(err)
	}
	rec := getActivityJSON(t, h, cookie, "/api/activity/upcoming")
	var resp upcomingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var sawDownloadFallback, sawSummary bool
	for _, it := range resp.Items {
		if it.Kind == activity.KindDownload && it.Subject == "no-title-vid" {
			sawDownloadFallback = true
		}
		if it.Kind == activity.KindSummary && it.Subject == "summ-vid" {
			sawSummary = true
		}
	}
	if !sawDownloadFallback || !sawSummary {
		t.Fatalf("items = %+v (download fallback=%v, summary=%v)", resp.Items, sawDownloadFallback, sawSummary)
	}
}
