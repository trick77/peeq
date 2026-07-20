package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/apitoken"
)

// createToken generates a token over the API and returns its plaintext.
func createToken(t *testing.T, h http.Handler, sessionCookie *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/token", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/settings/token status = %d, body = %s", rec.Code, rec.Body)
	}
	var created struct {
		Token     string `json:"token"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created token: %v", err)
	}
	return created.Token
}

func TestGetAPIToken_reportsAbsentAndNeverReturnsAToken(t *testing.T) {
	// Given
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)

	// When: no token has been generated.
	req := httptest.NewRequest(http.MethodGet, "/api/settings/token", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body)
	}
	var status struct {
		Present   bool   `json:"present"`
		CreatedAt string `json:"created_at"`
		Token     string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if status.Present {
		t.Fatalf("present = true before any token was generated")
	}
	if status.Token != "" {
		t.Fatalf("GET returned a token field: %q", status.Token)
	}
}

func TestPostAPIToken_returnsThePlaintextExactlyOnce(t *testing.T) {
	// Given
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)

	// When
	token := createToken(t, h, sessionCookie)

	// Then: correctly shaped.
	if !strings.HasPrefix(token, apitoken.Prefix) {
		t.Fatalf("token %q lacks the peeq_ prefix", token)
	}
	if len(token) != 48 {
		t.Fatalf("len(token) = %d, want 48", len(token))
	}

	// And: a subsequent GET reports it present but never echoes it.
	getReq := httptest.NewRequest(http.MethodGet, "/api/settings/token", nil)
	getReq.AddCookie(sessionCookie)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	if !strings.Contains(getRec.Body.String(), `"present":true`) {
		t.Fatalf("GET after create: present is not true (body=%s)", getRec.Body)
	}
	if strings.Contains(getRec.Body.String(), token) {
		t.Fatalf("GET echoed the token back: %s", getRec.Body)
	}
}

func TestPostAPIToken_regenerationInvalidatesThePreviousToken(t *testing.T) {
	// Given: a token exists.
	deps := testDeps(t)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	first := createToken(t, h, sessionCookie)

	// When: it is regenerated.
	second := createToken(t, h, sessionCookie)

	// Then: a new value is issued and the old one no longer verifies.
	if first == second {
		t.Fatalf("regeneration returned the same token %q", first)
	}
	storedHash := deps.Settings.APITokenHash(context.Background())
	if apitoken.Verify(first, storedHash) {
		t.Fatalf("the old token still verifies after regeneration")
	}
	if !apitoken.Verify(second, storedHash) {
		t.Fatalf("the new token does not verify")
	}
}

func TestGetSettings_neverCarriesTheAPIToken(t *testing.T) {
	// Regression guard for the "nothing secret in Settings" contract.
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)
	token := createToken(t, h, sessionCookie)

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(strings.ToLower(body), "api_token") {
		t.Fatalf("GET /api/settings carries an api_token field: %s", body)
	}
	if strings.Contains(body, token) {
		t.Fatalf("GET /api/settings carries the token: %s", body)
	}
}

func TestMachineCookie_writesTheCookieWithAValidToken(t *testing.T) {
	// Given: a generated token.
	deps := testDeps(t)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	token := createToken(t, h, sessionCookie)

	// When: a machine pushes a cookie with a bearer token and NO session.
	body := `{"cookie":` + strconv.Quote(validYouTubeCookieBody) + `}`
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "SID") {
		t.Fatalf("the machine route echoed the cookie back: %s", rec.Body)
	}
	if got := deps.Settings.CookieStatus(context.Background()); got != "valid" {
		t.Fatalf("cookie_status = %q, want valid", got)
	}
}

func TestMachineCookie_rejectsRequestsWithoutAValidToken(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"wrong token", "Bearer peeq_not-the-right-token-value-at-all-xx"},
		{"wrong scheme", "Basic something"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a token exists, but the request does not present it.
			deps := testDeps(t)
			h := New(deps)
			sessionCookie := loginAndGetCookie(t, h)
			createToken(t, h, sessionCookie)

			// When
			body := `{"cookie":` + strconv.Quote(validYouTubeCookieBody) + `}`
			req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", strings.NewReader(body))
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			// Then: rejected, and nothing was written.
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body)
			}
			if got := deps.Settings.CookieStatus(context.Background()); got == "valid" {
				t.Fatalf("an unauthorized request wrote the cookie")
			}
		})
	}
}

func TestMachineCookie_rejectsWhenNoTokenHasBeenGenerated(t *testing.T) {
	// Given: peeq is unconfigured — no token exists at all.
	deps := testDeps(t)
	h := New(deps)

	// When: a request presents some token anyway.
	body := `{"cookie":` + strconv.Quote(validYouTubeCookieBody) + `}`
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer peeq_anything-at-all-goes-here-for-this")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then: an empty stored hash never authenticates.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body)
	}
	if got := deps.Settings.CookieStatus(context.Background()); got == "valid" {
		t.Fatalf("an unconfigured peeq accepted a cookie write")
	}
}

func TestMachineCookie_returnsMinimalAckNotSettings(t *testing.T) {
	// Given: a generated token and a valid cookie body
	deps := testDeps(t)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	token := createToken(t, h, sessionCookie)
	body := `{"cookie":` + strconv.Quote(validYouTubeCookieBody) + `}`

	// When: the machine route accepts the cookie
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then: 200 with only status and present — no settings fields at all
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["status"] != "valid" {
		t.Fatalf(`status = %v, want "valid"`, got["status"])
	}
	if got["present"] != true {
		t.Fatalf("present = %v, want true", got["present"])
	}
	if len(got) != 2 {
		t.Fatalf("machine ack leaked extra fields: %v", got)
	}
	// Belt and braces: settings-only keys must never appear.
	for _, leaked := range []string{"format_preset", "limit_rate", "cookie_text", "api_token_hash"} {
		if _, ok := got[leaked]; ok {
			t.Fatalf("machine ack leaked %q", leaked)
		}
	}
}

func TestSessionCookieRoute_stillReturnsSettingsView(t *testing.T) {
	// Given: the session-authenticated cookie route (unchanged behaviour)
	deps := testDeps(t)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	body := `{"cookie":` + strconv.Quote(validYouTubeCookieBody) + `}`

	// When
	req := httptest.NewRequest(http.MethodPut, "/api/settings/cookie", strings.NewReader(body))
	req.AddCookie(sessionCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then: the full settings view, as before
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["format_preset"]; !ok {
		t.Fatalf("session route no longer returns the settings view: %v", got)
	}
}

func TestMachineCookie_resumesWorker(t *testing.T) {
	// Given: a paused worker and a generated token
	deps := testDeps(t)
	worker := &fakeWorker{paused: true}
	deps.Worker = worker
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	token := createToken(t, h, sessionCookie)
	before := worker.resumes()
	body := `{"cookie":` + strconv.Quote(validYouTubeCookieBody) + `}`

	// When: a machine-path cookie write succeeds
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then: the download worker was un-wedged exactly once
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body)
	}
	if got := worker.resumes() - before; got != 1 {
		t.Fatalf("resumes = %d, want 1", got)
	}
}

func TestMachineCookie_doesNotResumeWorkerOnInvalidCookie(t *testing.T) {
	// Given: a wired worker and a body that fails cookie.Validate
	deps := testDeps(t)
	worker := &fakeWorker{paused: true}
	deps.Worker = worker
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	token := createToken(t, h, sessionCookie)
	before := worker.resumes()

	// When
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie",
		strings.NewReader(`{"cookie":"this is not a netscape cookie file"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then: rejected, and the worker was never resumed
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body)
	}
	if got := worker.resumes() - before; got != 0 {
		t.Fatalf("resumes = %d, want 0 — resumed on an invalid cookie", got)
	}
}

func TestMachineCookie_rejectsAMalformedCookieBody(t *testing.T) {
	// Given
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)
	token := createToken(t, h, sessionCookie)

	// When
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie",
		strings.NewReader(`{"cookie":"this is not a netscape cookie file"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body)
	}
}
