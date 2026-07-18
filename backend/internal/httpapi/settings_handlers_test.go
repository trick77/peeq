package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/vark/internal/auth"
)

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
