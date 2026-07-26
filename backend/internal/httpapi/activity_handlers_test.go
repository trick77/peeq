package httpapi

import (
	"database/sql"
	"encoding/json"
	"fmt"
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

// The search box is a server parameter, not a client filter: the page holds
// only what it has paged in, so filtering there would answer "nothing" for
// something the log plainly contains.
func TestActivity_listFiltersByQuery(t *testing.T) {
	deps, act, _, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	act.Record(activity.Event{Kind: activity.KindScan, Outcome: activity.OutcomeOK, Subject: "Veritasium", Summary: "3 new"})
	act.Record(activity.Event{Kind: activity.KindDownload, Outcome: activity.OutcomeOK, Subject: "A clip", Detail: "412 MiB"})

	for _, tc := range []struct {
		name  string
		query string
		want  string
	}{
		{"matches a subject", "?q=verita", "Veritasium"},
		{"matches a detail", "?q=412", "A clip"},
		{"ignores surrounding whitespace", "?q=%20%20clip%20%20", "A clip"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := getActivityJSON(t, h, cookie, "/api/activity"+tc.query)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
			}
			var resp activityListResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if len(resp.Events) != 1 || resp.Events[0].Subject != tc.want {
				t.Fatalf("events = %+v, want just %q", resp.Events, tc.want)
			}
		})
	}

	// A query of nothing but spaces is a user typing, not a filter.
	t.Run("blank query returns everything", func(t *testing.T) {
		rec := getActivityJSON(t, h, cookie, "/api/activity?q=%20")
		var resp activityListResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if len(resp.Events) != 2 {
			t.Fatalf("got %d events, want 2", len(resp.Events))
		}
	})
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

func TestActivity_upcomingProjectsScheduledOnly(t *testing.T) {
	deps, _, ch, jb, vids, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// A subscribed channel (scheduled scan) and a pending download, which the
	// projection must now ignore — Up next renders queued jobs from the client's
	// own live state, so projecting them here would print each one twice.
	if err := ch.Upsert(channels.Channel{ID: "UCx", Name: "Veritasium"}); err != nil {
		t.Fatal(err)
	}
	if err := ch.MarkAdded("UCx", "2026-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Subscribe("UCx", "2026-07-25 12:00:00"); err != nil {
		t.Fatal(err)
	}
	// Subscribe seeds next_meta_refresh_at with a RANDOM jitter, which can land
	// either side of the scan above and flip the order asserted below. Pin it
	// past the scan so this test asserts the ordering rule, not the dice.
	if err := ch.MarkMetaRefreshed("UCx", "2026-08-01 12:00:00"); err != nil {
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
	// Two items, both timed: the scheduled scan, then the metadata refresh pinned
	// after it above. The pending download contributes nothing.
	if len(resp.Items) != 2 {
		t.Fatalf("items = %+v, want the two scheduled rows only", resp.Items)
	}
	if resp.Items[0].Kind != activity.KindScan || resp.Items[0].Subject != "Veritasium" {
		t.Fatalf("first item = %+v, want the scheduled scan", resp.Items[0])
	}
	if resp.Items[1].Kind != activity.KindChannelMeta {
		t.Fatalf("second item = %+v, want the metadata refresh", resp.Items[1])
	}
	// Channel rows carry the id so the schedule can link them to the channel page.
	if resp.Items[0].SubjectID != "UCx" {
		t.Fatalf("scan subject_id = %q, want UCx", resp.Items[0].SubjectID)
	}
	if resp.Items[1].SubjectID != "UCx" {
		t.Fatalf("metadata subject_id = %q, want UCx", resp.Items[1].SubjectID)
	}
	// Every row is timed now, so nothing is approximate.
	for _, it := range resp.Items {
		if it.Approx || it.At == "" {
			t.Fatalf("item = %+v, want an exact timed row", it)
		}
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

// A backlog of queued work used to sort ahead of every timed row and eat the
// shared cap of 20, so the schedule section silently emptied exactly when peeq
// was busiest. Queued jobs are no longer projected at all, so a big backlog must
// leave the scheduled rows completely untouched.
func TestActivity_upcomingBacklogDoesNotCrowdOutTheSchedule(t *testing.T) {
	deps, _, ch, jb, vids, sj, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	if err := ch.Upsert(channels.Channel{ID: "UCx", Name: "Veritasium"}); err != nil {
		t.Fatal(err)
	}
	if err := ch.MarkAdded("UCx", "2026-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Subscribe("UCx", "2026-07-25 12:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := ch.MarkMetaRefreshed("UCx", "2026-08-01 12:00:00"); err != nil {
		t.Fatal(err)
	}
	// More queued jobs than the whole projection cap. Jobs FK-reference videos,
	// so each needs its row.
	for i := 0; i < upcomingCap+5; i++ {
		id := fmt.Sprintf("v%d", i)
		if err := vids.Upsert(videos.Video{ID: id, URL: "u"}); err != nil {
			t.Fatal(err)
		}
		if _, err := jb.Enqueue(id, 10); err != nil {
			t.Fatal(err)
		}
		if _, err := sj.Enqueue(id); err != nil {
			t.Fatal(err)
		}
	}

	rec := getActivityJSON(t, h, cookie, "/api/activity/upcoming")
	var resp upcomingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items = %+v, want only the scan and the metadata refresh", resp.Items)
	}
	for _, it := range resp.Items {
		if it.Kind == activity.KindDownload || it.Kind == activity.KindSummary {
			t.Fatalf("queued job leaked into the projection: %+v", it)
		}
	}
	if resp.Truncated != 0 {
		t.Fatalf("truncated = %d, want 0 — the backlog must not consume the cap", resp.Truncated)
	}
}
