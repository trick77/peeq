package httpapi

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/store"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// testDeps builds Deps with dev auth claims set, so /api/auth/login
// short-circuits straight to a session without contacting OIDC.
func testDeps(t *testing.T) Deps {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	return Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		Settings:       settings.New(db),
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
}

func TestServer_devLoginThenEmptyVideos(t *testing.T) {
	h := New(testDeps(t))

	loginReq := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, loginReq)

	var sessionCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			sessionCookie = c
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatalf("expected %s cookie to be set by /api/auth/login, got none (status %d)", auth.SessionCookieName, loginRec.Code)
	}

	videosReq := httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	videosReq.AddCookie(sessionCookie)
	videosRec := httptest.NewRecorder()
	h.ServeHTTP(videosRec, videosReq)

	if videosRec.Code != http.StatusOK {
		t.Fatalf("GET /api/videos status = %d, want 200", videosRec.Code)
	}
	if got := strings.TrimSpace(videosRec.Body.String()); got != "[]" {
		t.Fatalf("GET /api/videos body = %q, want []", got)
	}
}

// TestServer_noAuthConfigured_failsClosed asserts requireAuth never fails
// open: with no dev claims and no OIDC backend configured (AuthMode unset),
// neither the videos endpoint nor login can produce an authenticated session.
func TestServer_noAuthConfigured_failsClosed(t *testing.T) {
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	h := New(Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
	})

	videosReq := httptest.NewRequest(http.MethodGet, "/api/videos", nil)
	videosRec := httptest.NewRecorder()
	h.ServeHTTP(videosRec, videosReq)
	if videosRec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/videos (no cookie) status = %d, want 401", videosRec.Code)
	}

	// No path to obtain a session either: login can't fall back to OIDC.
	loginReq := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/auth/login status = %d, want 503", loginRec.Code)
	}
}
