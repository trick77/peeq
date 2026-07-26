package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/channels"
)

// seedSkippableChannel creates a subscribed channel with both schedule columns
// pinned to known instants, so a test can assert exactly what a skip moved.
func seedSkippableChannel(t *testing.T, ch *channels.Store, id, scanAt, metaAt string) {
	t.Helper()
	if err := ch.Upsert(channels.Channel{ID: id, Name: "Veritasium"}); err != nil {
		t.Fatal(err)
	}
	if err := ch.MarkAdded(id, "2026-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Subscribe(id, scanAt); err != nil {
		t.Fatal(err)
	}
	// Subscribe seeds next_meta_refresh_at with a random jitter; pin it.
	if err := ch.MarkMetaRefreshed(id, metaAt); err != nil {
		t.Fatal(err)
	}
}

func postSkip(t *testing.T, h http.Handler, cookie *http.Cookie, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(http.MethodPost, path, nil)
	} else {
		r = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	}
	r.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func decodeSkip(t *testing.T, rec *httptest.ResponseRecorder) skipResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp skipResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// The issue's central worry: a channel that is repeatedly skipped must not look
// freshly scanned, and must not later flood the inbox as though its back
// catalogue were new. Both properties come from what the skip does NOT write,
// so that is what this asserts — byte-identical baselined_at and
// last_scanned_at across the skip.
func TestSkipScan_movesTheScheduleAndNothingElse(t *testing.T) {
	deps, _, ch, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	seedSkippableChannel(t, ch, "UCx", "2026-07-26 09:00:00", "2026-08-01 12:00:00")
	// A real scan pass, so there is a baseline and a last-scanned instant to
	// preserve. Without this both are empty and the assertion proves nothing.
	if err := ch.MarkScanned("UCx", true, "2026-07-25 08:00:00", "2026-07-26 09:00:00", ""); err != nil {
		t.Fatal(err)
	}
	before, err := ch.GetSubscription("UCx")
	if err != nil {
		t.Fatal(err)
	}

	resp := decodeSkip(t, postSkip(t, h, cookie, "/api/channels/UCx/skip-scan", ""))
	if resp.Status != "skipped" {
		t.Fatalf("status = %q, want skipped", resp.Status)
	}
	if resp.PreviousAt != before.NextScanAt {
		t.Fatalf("previous_at = %q, want the instant that was stored (%q)", resp.PreviousAt, before.NextScanAt)
	}
	if resp.At <= before.NextScanAt {
		t.Fatalf("skip moved next_scan_at to %q, which is not later than %q", resp.At, before.NextScanAt)
	}

	after, err := ch.GetSubscription("UCx")
	if err != nil {
		t.Fatal(err)
	}
	if after.NextScanAt != resp.At {
		t.Fatalf("stored next_scan_at = %q, want the reported %q", after.NextScanAt, resp.At)
	}
	if after.BaselinedAt != before.BaselinedAt {
		t.Fatalf("baselined_at moved from %q to %q — a skipped channel would replay its back catalogue",
			before.BaselinedAt, after.BaselinedAt)
	}
	if after.LastScannedAt != before.LastScannedAt {
		t.Fatalf("last_scanned_at moved from %q to %q — a skipped channel must not look freshly scanned",
			before.LastScannedAt, after.LastScannedAt)
	}
}

// The projection is computed from the schedule columns on every request, so a
// skip must be visible to the very next GET. This is the whole feature: if the
// write did not reach what Up next reads, the row would come straight back.
func TestSkipScan_isVisibleInTheNextUpcomingResponse(t *testing.T) {
	deps, _, ch, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	seedSkippableChannel(t, ch, "UCx", "2026-07-26 09:00:00", "2026-08-01 12:00:00")

	resp := decodeSkip(t, postSkip(t, h, cookie, "/api/channels/UCx/skip-scan", ""))

	rec := getActivityJSON(t, h, cookie, "/api/activity/upcoming")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var up upcomingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &up); err != nil {
		t.Fatal(err)
	}
	var scanAt string
	for _, it := range up.Items {
		if it.Kind == "scan" && it.SubjectID == "UCx" {
			scanAt = it.At
		}
	}
	if scanAt == "" {
		t.Fatal("no scan row for UCx in the projection after a skip")
	}
	if scanAt != resp.At {
		t.Fatalf("projection still shows the scan at %q, want the skipped-to %q", scanAt, resp.At)
	}
	if scanAt == "2026-07-26 09:00:00" {
		t.Fatal("the skipped occurrence came straight back")
	}
}

// Undo is the same endpoint handed the instant the skip reported. It must
// restore that instant exactly — an approximate restore would drift the channel
// out of the rotation the jitter deliberately scattered it into.
func TestSkipScan_undoRestoresTheExactPreviousInstant(t *testing.T) {
	deps, _, ch, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	seedSkippableChannel(t, ch, "UCx", "2026-07-26 09:00:00", "2026-08-01 12:00:00")

	skipped := decodeSkip(t, postSkip(t, h, cookie, "/api/channels/UCx/skip-scan", ""))
	undone := decodeSkip(t, postSkip(t, h, cookie, "/api/channels/UCx/skip-scan",
		`{"at":"`+skipped.PreviousAt+`"}`))
	if undone.At != "2026-07-26 09:00:00" {
		t.Fatalf("undo landed on %q, want the original 2026-07-26 09:00:00", undone.At)
	}
	sub, err := ch.GetSubscription("UCx")
	if err != nil {
		t.Fatal(err)
	}
	if sub.NextScanAt != "2026-07-26 09:00:00" {
		t.Fatalf("stored next_scan_at = %q after undo, want the original", sub.NextScanAt)
	}
}

// A "Check now" that is then skipped is called off. Leaving the marker set
// would make a later automatic pass announce itself on Activity as the answer
// to a request the user withdrew.
func TestSkipScan_clearsAnOutstandingCheckNow(t *testing.T) {
	deps, _, ch, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	seedSkippableChannel(t, ch, "UCx", "2026-07-26 09:00:00", "2026-08-01 12:00:00")
	if err := ch.RequestScan("UCx", "2026-07-26 08:00:00"); err != nil {
		t.Fatal(err)
	}

	decodeSkip(t, postSkip(t, h, cookie, "/api/channels/UCx/skip-scan", ""))

	sub, err := ch.GetSubscription("UCx")
	if err != nil {
		t.Fatal(err)
	}
	if sub.ScanRequestedAt != "" {
		t.Fatalf("scan_requested_at = %q after a skip, want cleared", sub.ScanRequestedAt)
	}
}

// A skip must move the occurrence LATER, including when the occurrence is
// already further out than one fresh interval. Both cadences are jittered and
// neither due-soon query has a horizon, so Up next lists scans up to 27h out and
// refreshes up to 7.5 days out — measuring the new slot from now alone could
// land either of them earlier than where it already was, which is Skip making
// the thing happen sooner.
func TestSkip_neverMovesAnOccurrenceEarlier(t *testing.T) {
	deps, _, ch, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	// The far end of each jitter spread: 30h for the scan (max 27h), 8 days for
	// the refresh (max 7.5 days).
	scanAt := time.Now().UTC().Add(30 * time.Hour).Format(skipTimeLayout)
	metaAt := time.Now().UTC().Add(8 * 24 * time.Hour).Format(skipTimeLayout)
	seedSkippableChannel(t, ch, "UCx", scanAt, metaAt)

	scanResp := decodeSkip(t, postSkip(t, h, cookie, "/api/channels/UCx/skip-scan", ""))
	if scanResp.At <= scanAt {
		t.Fatalf("skip moved next_scan_at from %q to %q — a skip must not pull a scan earlier", scanAt, scanResp.At)
	}
	metaResp := decodeSkip(t, postSkip(t, h, cookie, "/api/channels/UCx/skip-meta", ""))
	if metaResp.At <= metaAt {
		t.Fatalf("skip moved next_meta_refresh_at from %q to %q — a skip must not pull a refresh earlier", metaAt, metaResp.At)
	}
}

func TestSkipMeta_movesOnlyTheRefreshColumn(t *testing.T) {
	deps, _, ch, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	seedSkippableChannel(t, ch, "UCx", "2026-07-26 09:00:00", "2026-08-01 12:00:00")

	resp := decodeSkip(t, postSkip(t, h, cookie, "/api/channels/UCx/skip-meta", ""))
	if resp.PreviousAt != "2026-08-01 12:00:00" {
		t.Fatalf("previous_at = %q, want the pinned refresh instant", resp.PreviousAt)
	}
	sub, err := ch.GetSubscription("UCx")
	if err != nil {
		t.Fatal(err)
	}
	if sub.NextMetaRefreshAt != resp.At {
		t.Fatalf("stored next_meta_refresh_at = %q, want %q", sub.NextMetaRefreshAt, resp.At)
	}
	if sub.NextScanAt != "2026-07-26 09:00:00" {
		t.Fatalf("skipping the refresh moved next_scan_at to %q — the two schedules are independent", sub.NextScanAt)
	}
}

// A channel with no rotation slot has nothing to skip, and writing one here
// would schedule a refresh rather than skip one.
func TestSkipMeta_rejectsAChannelWithNoScheduledRefresh(t *testing.T) {
	deps, _, ch, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	if err := ch.Upsert(channels.Channel{ID: "UCx", Name: "Veritasium"}); err != nil {
		t.Fatal(err)
	}
	if err := ch.MarkAdded("UCx", "2026-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := ch.Subscribe("UCx", "2026-07-26 09:00:00"); err != nil {
		t.Fatal(err)
	}
	// Clearing the slot Subscribe seeded is what makes this the NULL case.
	if _, err := ch.DB().Exec(`UPDATE subscriptions SET next_meta_refresh_at = NULL WHERE channel_id = ?`, "UCx"); err != nil {
		t.Fatal(err)
	}

	rec := postSkip(t, h, cookie, "/api/channels/UCx/skip-meta", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a channel with no refresh scheduled", rec.Code)
	}
}

func TestSkip_rejectsAnUnsubscribedChannel(t *testing.T) {
	deps, _, ch, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	if err := ch.Upsert(channels.Channel{ID: "UCx", Name: "Veritasium"}); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/api/channels/UCx/skip-scan", "/api/channels/UCx/skip-meta"} {
		if rec := postSkip(t, h, cookie, path, ""); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", path, rec.Code)
		}
	}
}

// The restore instant is written straight into a column the two background
// loops compare against datetime('now'). Text that is not in that form would
// not compare, and would quietly drop the channel out of its own rotation.
func TestSkip_rejectsAMalformedRestoreInstant(t *testing.T) {
	deps, _, ch, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	seedSkippableChannel(t, ch, "UCx", "2026-07-26 09:00:00", "2026-08-01 12:00:00")

	for _, body := range []string{
		`{"at":"tomorrow"}`,
		`{"at":"2026-07-26T09:00:00Z"}`,
		`{"at":"' OR 1=1 --"}`,
	} {
		rec := postSkip(t, h, cookie, "/api/channels/UCx/skip-scan", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", body, rec.Code)
		}
	}
	sub, err := ch.GetSubscription("UCx")
	if err != nil {
		t.Fatal(err)
	}
	if sub.NextScanAt != "2026-07-26 09:00:00" {
		t.Fatalf("a rejected request still moved the schedule to %q", sub.NextScanAt)
	}
}

// Both endpoints depend on the channels store; without it there is nothing to
// reschedule, and the house rule for an optional dependency is 503 rather than
// a nil dereference.
func TestSkip_reportsUnconfiguredChannels(t *testing.T) {
	deps, _, _, _, _, _, _ := activityTestDeps(t)
	deps.Channels = nil
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	for _, path := range []string{"/api/channels/UCx/skip-scan", "/api/channels/UCx/skip-meta"} {
		if rec := postSkip(t, h, cookie, path, ""); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d, want 503", path, rec.Code)
		}
	}
}

// An absent body is the ordinary skip and must not be an error, but a body that
// is not JSON at all is: it means the client meant to say something and failed,
// and guessing "they probably meant skip" would silently discard an undo.
func TestSkip_rejectsAMalformedBody(t *testing.T) {
	deps, _, ch, _, _, _, _ := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	seedSkippableChannel(t, ch, "UCx", "2026-07-26 09:00:00", "2026-08-01 12:00:00")

	for _, path := range []string{"/api/channels/UCx/skip-scan", "/api/channels/UCx/skip-meta"} {
		if rec := postSkip(t, h, cookie, path, `{"at":`); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400 for a malformed body", path, rec.Code)
		}
	}
	sub, err := ch.GetSubscription("UCx")
	if err != nil {
		t.Fatal(err)
	}
	if sub.NextScanAt != "2026-07-26 09:00:00" || sub.NextMetaRefreshAt != "2026-08-01 12:00:00" {
		t.Fatal("a rejected request still moved a schedule")
	}
}

// A store that cannot be read is a server fault, not a bad request: the client
// asked for something reasonable and peeq failed to do it.
func TestSkip_reportsAStoreFailure(t *testing.T) {
	deps, _, ch, _, _, _, db := activityTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	seedSkippableChannel(t, ch, "UCx", "2026-07-26 09:00:00", "2026-08-01 12:00:00")
	// Closing the database is the bluntest way to make every query fail, and
	// the login above has already happened so the session still resolves.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/api/channels/UCx/skip-scan", "/api/channels/UCx/skip-meta"} {
		if rec := postSkip(t, h, cookie, path, ""); rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s status = %d, want 500", path, rec.Code)
		}
	}
}
