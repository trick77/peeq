package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/settings"
)

// flakyDBTX satisfies settings.DBTX but lets a test force either
// ExecContext or QueryRowContext to fail independently, while the other
// still runs against a real migrated *sql.DB. This is how the store-error
// branches in the settings handlers (which take *settings.Store, a
// concrete type) get exercised without touching any non-test source.
type flakyDBTX struct {
	db        *sql.DB
	failExec  bool
	failQuery bool
}

func (f *flakyDBTX) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if f.failExec {
		return nil, errors.New("flakyDBTX: forced exec failure")
	}
	return f.db.ExecContext(ctx, query, args...)
}

func (f *flakyDBTX) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if f.failQuery {
		return f.db.QueryRowContext(ctx, "SELECT * FROM flaky_dbtx_nonexistent_table")
	}
	return f.db.QueryRowContext(ctx, query, args...)
}

// testDepsNilSettings builds Deps with dev auth wired but Settings left
// nil, so every settings/cookie route takes its "settings are not
// configured" 503 branch.
func testDepsNilSettings(t *testing.T) Deps {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	return Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
}

// testDepsFlakySettings builds Deps whose auth stores are backed by a
// normal working DB (so login always succeeds) but whose Settings store is
// backed by a flakyDBTX over a second, separately migrated DB, so a test
// can force Get/Update to fail independently of authentication.
func testDepsFlakySettings(t *testing.T) (Deps, *flakyDBTX) {
	t.Helper()
	authDB := openTestDB(t)
	sessions := auth.NewSessionStore(authDB, false)
	users := auth.NewUserStore(authDB)
	settingsDB := openTestDB(t)
	flaky := &flakyDBTX{db: settingsDB}
	settingsStore := settings.New(flaky)
	return Deps{
		AuthService:     auth.NewService(nil, sessions, users),
		AuthMiddleware:  auth.NewMiddleware(sessions, users),
		Settings:        settingsStore,
		TokenMiddleware: auth.NewTokenMiddleware(settingsStore),
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}, flaky
}

const validYouTubeCookieBody = "# Netscape HTTP Cookie File\n" +
	".youtube.com\tTRUE\t/\tTRUE\t1789000000\tSID\tabc\n" +
	".youtube.com\tTRUE\t/\tTRUE\t1789000000\t__Secure-3PSID\tdef\n"

// loginAndGetCookie performs a dev-mode login against h and returns the
// resulting session cookie for use on subsequent authenticated requests.
func loginAndGetCookie(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	loginReq := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, loginReq)
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	t.Fatalf("no session cookie set by /api/auth/login (status %d)", loginRec.Code)
	return nil
}

// TestSettingsHandlers_putCookieThenGetSettings_hidesCookieBody is the
// central Task 6 guarantee: after PUT /api/settings/cookie with a valid
// cookie, GET /api/settings reports cookie_status=="valid" but the response
// body never contains the cookie text or any "cookie_text" field.
func TestSettingsHandlers_putCookieThenGetSettings_hidesCookieBody(t *testing.T) {
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)

	putBody, _ := json.Marshal(map[string]string{"cookie": validYouTubeCookieBody})
	putReq := httptest.NewRequest(http.MethodPut, "/api/settings/cookie", bytes.NewReader(putBody))
	putReq.AddCookie(sessionCookie)
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	h.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings/cookie status = %d, body = %s", putRec.Code, putRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	getReq.AddCookie(sessionCookie)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings status = %d, body = %s", getRec.Code, getRec.Body.String())
	}

	rawBody := getRec.Body.String()
	if strings.Contains(rawBody, "cookie_text") {
		t.Fatalf("GET /api/settings body leaks cookie_text field: %s", rawBody)
	}
	if strings.Contains(rawBody, validYouTubeCookieBody) || strings.Contains(rawBody, "__Secure-3PSID") {
		t.Fatalf("GET /api/settings body leaks cookie value: %s", rawBody)
	}

	var got map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal GET /api/settings body: %v", err)
	}
	if got["cookie_status"] != "valid" {
		t.Fatalf("cookie_status = %v, want %q", got["cookie_status"], "valid")
	}
	if _, present := got["cookie_text"]; present {
		t.Fatalf("GET /api/settings JSON has a cookie_text field, want none")
	}
}

func TestSettingsHandlers_putCookieRejectsInvalid(t *testing.T) {
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)

	putBody, _ := json.Marshal(map[string]string{"cookie": "garbage"})
	putReq := httptest.NewRequest(http.MethodPut, "/api/settings/cookie", bytes.NewReader(putBody))
	putReq.AddCookie(sessionCookie)
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	h.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusBadRequest {
		t.Fatalf("PUT /api/settings/cookie (garbage) status = %d, want 400", putRec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(putRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if got["error"] == "" {
		t.Fatal("expected a non-empty error message")
	}
}

func TestSettingsHandlers_putSettingsUpdatesFields(t *testing.T) {
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)

	putBody, _ := json.Marshal(map[string]any{
		"retention_days": 30,
		"min_free_gb":    10,
	})
	putReq := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(putBody))
	putReq.AddCookie(sessionCookie)
	putReq.Header.Set("Content-Type", "application/json")
	putRec := httptest.NewRecorder()
	h.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings status = %d, body = %s", putRec.Code, putRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	getReq.AddCookie(sessionCookie)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)

	var got map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal GET /api/settings body: %v", err)
	}
	if got["retention_days"] != float64(30) {
		t.Fatalf("retention_days = %v, want 30", got["retention_days"])
	}
	if got["min_free_gb"] != float64(10) {
		t.Fatalf("min_free_gb = %v, want 10", got["min_free_gb"])
	}
}

// subtitles_default is the only bool in the patch, so it gets its own
// round-trip: a bool that never reaches the store would silently read back
// as the seeded false and look like "the toggle just doesn't stick".
func TestSettingsHandlers_putSettingsUpdatesSubtitlesDefault(t *testing.T) {
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)

	put := func(v bool) map[string]any {
		body, _ := json.Marshal(map[string]any{"subtitles_default": v})
		req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(body))
		req.AddCookie(sessionCookie)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT /api/settings status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal PUT /api/settings body: %v", err)
		}
		return got
	}

	if got := put(true); got["subtitles_default"] != true {
		t.Fatalf("subtitles_default = %v, want true", got["subtitles_default"])
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	getReq.AddCookie(sessionCookie)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	var got map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal GET /api/settings body: %v", err)
	}
	if got["subtitles_default"] != true {
		t.Fatalf("GET subtitles_default = %v, want true", got["subtitles_default"])
	}

	if got := put(false); got["subtitles_default"] != false {
		t.Fatalf("subtitles_default = %v, want false", got["subtitles_default"])
	}
}

// TestSettingsHandlers_putCookieResumesWorker is the wiring guarantee for
// finding 1: a SUCCESSFUL cookie PUT must call Worker.Resume() (so a queue
// paused on a blocked/expired cookie un-wedges when the user re-pastes), while
// a REJECTED cookie must not — the queue should stay paused on bad input.
func TestSettingsHandlers_putCookieResumesWorker(t *testing.T) {
	deps := testDeps(t)
	fw := &fakeWorker{}
	deps.Worker = fw
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	putCookie := func(t *testing.T, body string) int {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"cookie": body})
		req := httptest.NewRequest(http.MethodPut, "/api/settings/cookie", bytes.NewReader(payload))
		req.AddCookie(sessionCookie)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// Rejected cookie: 400, worker must NOT be resumed.
	if code := putCookie(t, "garbage"); code != http.StatusBadRequest {
		t.Fatalf("PUT invalid cookie status = %d, want 400", code)
	}
	if n := fw.resumes(); n != 0 {
		t.Fatalf("Resume called %d times after a rejected cookie, want 0", n)
	}

	// Valid cookie: 200, worker resumed exactly once.
	if code := putCookie(t, validYouTubeCookieBody); code != http.StatusOK {
		t.Fatalf("PUT valid cookie status = %d, want 200", code)
	}
	if n := fw.resumes(); n != 1 {
		t.Fatalf("Resume called %d times after a valid cookie, want 1", n)
	}
}

// TestSettingsHandlers_putRejectsNegativeNumbers is finding 4's API guard: a
// negative retention_days / min_free_gb / throttle_base_seconds must be
// rejected with 400 and NEVER persisted (a negative retention_days would
// otherwise tombstone the whole library on the next sweep; a negative
// min_free_gb would freeze the queue permanently).
func TestSettingsHandlers_putRejectsNegativeNumbers(t *testing.T) {
	for _, field := range []string{"retention_days", "min_free_gb", "throttle_base_seconds"} {
		t.Run(field, func(t *testing.T) {
			h := New(testDeps(t))
			sessionCookie := loginAndGetCookie(t, h)

			putBody, _ := json.Marshal(map[string]any{field: -1})
			putReq := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(putBody))
			putReq.AddCookie(sessionCookie)
			putReq.Header.Set("Content-Type", "application/json")
			putRec := httptest.NewRecorder()
			h.ServeHTTP(putRec, putReq)
			if putRec.Code != http.StatusBadRequest {
				t.Fatalf("PUT %s=-1 status = %d, want 400, body = %s", field, putRec.Code, putRec.Body.String())
			}

			// The rejected value must not have been persisted: the stored
			// settings keep their (non-negative) defaults.
			getReq := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
			getReq.AddCookie(sessionCookie)
			getRec := httptest.NewRecorder()
			h.ServeHTTP(getRec, getReq)
			var got map[string]any
			if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal GET /api/settings: %v", err)
			}
			if v, ok := got[field].(float64); !ok || v < 0 {
				t.Fatalf("%s persisted as %v after a rejected negative PUT, want a non-negative default", field, got[field])
			}
		})
	}
}

// An unknown format_preset makes ytdlp.Resolve error, which surfaces as a
// failed download for EVERY video until the setting is corrected. A retired
// id gets here without anyone hand-crafting a request — a Settings tab
// rendered before the deploy that retired it still carries its button — so
// the write is refused and the stored preset left alone.
func TestSettingsHandlers_putRejectsUnknownFormatPreset(t *testing.T) {
	for _, preset := range []string{"apple-720p", "", "bestvideo+bestaudio"} {
		t.Run(preset, func(t *testing.T) {
			h := New(testDeps(t))
			sessionCookie := loginAndGetCookie(t, h)

			putBody, _ := json.Marshal(map[string]any{"format_preset": preset})
			putReq := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(putBody))
			putReq.AddCookie(sessionCookie)
			putReq.Header.Set("Content-Type", "application/json")
			putRec := httptest.NewRecorder()
			h.ServeHTTP(putRec, putReq)
			if putRec.Code != http.StatusBadRequest {
				t.Fatalf("PUT format_preset=%q status = %d, want 400, body = %s", preset, putRec.Code, putRec.Body.String())
			}

			// The rejected value must not have been persisted.
			getReq := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
			getReq.AddCookie(sessionCookie)
			getRec := httptest.NewRecorder()
			h.ServeHTTP(getRec, getReq)
			var got map[string]any
			if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal GET /api/settings: %v", err)
			}
			if got["format_preset"] != "apple-1080p" {
				t.Fatalf("format_preset = %v after a rejected PUT, want the untouched default", got["format_preset"])
			}
		})
	}

	// Every live preset id, and the "custom" escape hatch, still write.
	for _, preset := range []string{"apple-1080p", "apple-vp9-4k", "best-mp4", "custom"} {
		t.Run("accepts/"+preset, func(t *testing.T) {
			h := New(testDeps(t))
			sessionCookie := loginAndGetCookie(t, h)

			putBody, _ := json.Marshal(map[string]any{"format_preset": preset, "format_custom": "bestvideo+bestaudio"})
			putReq := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(putBody))
			putReq.AddCookie(sessionCookie)
			putReq.Header.Set("Content-Type", "application/json")
			putRec := httptest.NewRecorder()
			h.ServeHTTP(putRec, putReq)
			if putRec.Code != http.StatusOK {
				t.Fatalf("PUT format_preset=%q status = %d, want 200, body = %s", preset, putRec.Code, putRec.Body.String())
			}
		})
	}
}

func TestSettingsHandlers_cookieHealth(t *testing.T) {
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)

	healthReq := httptest.NewRequest(http.MethodGet, "/api/cookie/health", nil)
	healthReq.AddCookie(sessionCookie)
	healthRec := httptest.NewRecorder()
	h.ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK {
		t.Fatalf("GET /api/cookie/health status = %d, body = %s", healthRec.Code, healthRec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(healthRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal cookie health body: %v", err)
	}
	if got["status"] != "absent" {
		t.Fatalf("status = %v, want %q before any cookie is set", got["status"], "absent")
	}
	if got["present"] != false {
		t.Fatalf("present = %v, want false before any cookie is set", got["present"])
	}
}

// TestSettingsHandlers_settingsNotConfigured covers the "s.settings == nil"
// guard on every settings/cookie route: a deployment that never wired a
// settings store must fail closed with 503, not panic.
func TestSettingsHandlers_settingsNotConfigured(t *testing.T) {
	h := New(testDepsNilSettings(t))
	sessionCookie := loginAndGetCookie(t, h)

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/settings", nil),
		httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader([]byte("{}"))),
		httptest.NewRequest(http.MethodPut, "/api/settings/cookie", bytes.NewReader([]byte(`{"cookie":"x"}`))),
		httptest.NewRequest(http.MethodGet, "/api/cookie/health", nil),
	} {
		req.AddCookie(sessionCookie)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s status = %d, want 503, body = %s", req.Method, req.URL.Path, rec.Code, rec.Body.String())
		}
	}
}

// TestSettingsHandlers_getSettings_storeError covers the store-error branch
// of handleGetSettings: a real callers-visible failure (DB blip) must
// surface as a generic 500, never a panic or a leaked internal error.
func TestSettingsHandlers_getSettings_storeError(t *testing.T) {
	deps, flaky := testDepsFlakySettings(t)
	flaky.failQuery = true
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /api/settings (store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestSettingsHandlers_putSettings_invalidBody covers handlePutSettings'
// JSON-decode error branch.
func TestSettingsHandlers_putSettings_invalidBody(t *testing.T) {
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader("not json"))
	req.AddCookie(sessionCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT /api/settings (bad body) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestSettingsHandlers_putSettings_updateStoreError covers the
// s.settings.Update error branch of handlePutSettings.
func TestSettingsHandlers_putSettings_updateStoreError(t *testing.T) {
	deps, flaky := testDepsFlakySettings(t)
	flaky.failExec = true
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	putBody, _ := json.Marshal(map[string]any{"retention_days": 5})
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(putBody))
	req.AddCookie(sessionCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PUT /api/settings (update store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestSettingsHandlers_putSettings_getAfterUpdateStoreError covers the
// second store-error branch in handlePutSettings: Update succeeds but the
// follow-up Get (used to build the response) fails.
func TestSettingsHandlers_putSettings_getAfterUpdateStoreError(t *testing.T) {
	deps, flaky := testDepsFlakySettings(t)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	// Update succeeds (failExec still false); only the follow-up Get fails.
	flaky.failQuery = true

	putBody, _ := json.Marshal(map[string]any{"retention_days": 5})
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader(putBody))
	req.AddCookie(sessionCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PUT /api/settings (get-after-update store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestSettingsHandlers_putCookie_invalidBody covers applyCookie's
// JSON-decode error branch.
func TestSettingsHandlers_putCookie_invalidBody(t *testing.T) {
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodPut, "/api/settings/cookie", strings.NewReader("not json"))
	req.AddCookie(sessionCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT /api/settings/cookie (bad body) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestSettingsHandlers_putCookie_empty covers applyCookie's
// empty-cookie-body rejection.
func TestSettingsHandlers_putCookie_empty(t *testing.T) {
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)

	putBody, _ := json.Marshal(map[string]string{"cookie": ""})
	req := httptest.NewRequest(http.MethodPut, "/api/settings/cookie", bytes.NewReader(putBody))
	req.AddCookie(sessionCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT /api/settings/cookie (empty) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if got["error"] == "" {
		t.Fatal("expected a non-empty error message")
	}
}

// TestSettingsHandlers_putCookie_getAfterSetStoreError covers the
// session-route store-error branch in applyCookie: SetCookie succeeds but
// the follow-up Get (used to build the full settings response) fails.
func TestSettingsHandlers_putCookie_getAfterSetStoreError(t *testing.T) {
	deps, flaky := testDepsFlakySettings(t)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	// SetCookie succeeds (failExec still false); only the follow-up Get fails.
	flaky.failQuery = true

	putBody, _ := json.Marshal(map[string]string{"cookie": validYouTubeCookieBody})
	req := httptest.NewRequest(http.MethodPut, "/api/settings/cookie", bytes.NewReader(putBody))
	req.AddCookie(sessionCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PUT /api/settings/cookie (get-after-set store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestSettingsHandlers_cookieHealth_storeError covers the store-error
// branch of handleCookieHealth.
func TestSettingsHandlers_cookieHealth_storeError(t *testing.T) {
	deps, flaky := testDepsFlakySettings(t)
	flaky.failQuery = true
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/api/cookie/health", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /api/cookie/health (store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

func TestSettingsHandlers_requireAuth(t *testing.T) {
	h := New(testDeps(t))

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/settings", nil),
		httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewReader([]byte("{}"))),
		httptest.NewRequest(http.MethodPut, "/api/settings/cookie", bytes.NewReader([]byte("{}"))),
		httptest.NewRequest(http.MethodGet, "/api/cookie/health", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", req.Method, req.URL.Path, rec.Code)
		}
	}
}
