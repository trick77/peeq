package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/store"
	"golang.org/x/oauth2"
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
	d, _, _ := testDepsWithStores(t)
	return d
}

// testDepsWithStores is testDeps but also returns the session/user stores it
// built, so callers that need to construct their own auth.Service (e.g. with
// a non-nil OIDC service) can reuse them instead of duplicating setup.
func testDepsWithStores(t *testing.T) (Deps, *auth.SessionStore, *auth.UserStore) {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	settingsStore := settings.New(db)
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
	}, sessions, users
}

// stubOIDCBackend satisfies auth.OIDCBackend. The default (zero-value) tests
// fail at the state-cookie check, before Exchange is ever reached, so
// exchangeErr is only used by tests that specifically need to drive the
// callback handler past the cookie checks and into a code-exchange failure.
type stubOIDCBackend struct {
	exchangeErr error
}

func (stubOIDCBackend) AuthCodeURL(state string, _ ...oauth2.AuthCodeOption) string {
	return "https://idp.example.com/authorize?state=" + state
}

func (s stubOIDCBackend) Exchange(context.Context, string) (*oauth2.Token, error) {
	if s.exchangeErr != nil {
		return nil, s.exchangeErr
	}
	return nil, errors.New("stub: exchange not expected in this test")
}

func (stubOIDCBackend) VerifyClaims(context.Context, *oauth2.Token) (auth.VerifiedClaims, error) {
	return auth.VerifiedClaims{}, errors.New("stub: verify not expected in this test")
}

// testDepsWithOIDC is testDeps with a real (stub-backed) OIDC service, so the
// callback handler takes the redirect path rather than the 503 "not
// configured" path.
func testDepsWithOIDC(t *testing.T) Deps {
	t.Helper()
	return testDepsWithOIDCBackend(t, stubOIDCBackend{})
}

// testDepsWithOIDCExchangeError is testDepsWithOIDC but with a backend whose
// Exchange call fails with the given error — used to drive the callback
// handler past the state/nonce cookie checks and into the code-exchange
// failure path, which is one of the two paths that can carry a secret into
// an unredacted log line.
func testDepsWithOIDCExchangeError(t *testing.T, exchangeErr error) Deps {
	t.Helper()
	return testDepsWithOIDCBackend(t, stubOIDCBackend{exchangeErr: exchangeErr})
}

func testDepsWithOIDCBackend(t *testing.T, backend auth.OIDCBackend) Deps {
	t.Helper()
	d, sessions, users := testDepsWithStores(t)
	oidcSvc := auth.NewOIDCService(auth.OIDCServiceConfig{
		Issuer:       "https://idp.example.com",
		ClientID:     "peeq-test",
		ClientSecret: "test-secret",
		RedirectURL:  "https://peeq.example.com/api/auth/callback",
		Backend:      backend,
		SecureCookie: false,
	})
	d.AuthService = auth.NewService(oidcSvc, sessions, users)
	return d
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
