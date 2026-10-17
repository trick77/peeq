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

	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/tokenauth"
	"github.com/trick77/ship/kit/auth"
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

// testSessions is a session signer keyed for this test alone.
func testSessions(t testing.TB) *auth.Sessions {
	t.Helper()
	s, err := auth.NewSessions(auth.SessionConfig{Name: SessionCookie, Key: auth.RandomKey()})
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	return s
}

// testDeps builds Deps with dev auth claims set, so /api/auth/login
// short-circuits straight to a session without contacting OIDC.
func testDeps(t *testing.T) Deps {
	t.Helper()
	db := openTestDB(t)
	settingsStore := settings.New(db)
	return Deps{
		Sessions:        testSessions(t),
		Settings:        settingsStore,
		TokenMiddleware: tokenauth.NewTokenMiddleware(settingsStore),
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
}

// stubOIDCBackend is kit/auth's Backend without a provider. The default
// (zero-value) tests fail at the state-cookie check, before Exchange is ever
// reached, so exchangeErr is only used by tests that specifically need to
// drive the callback handler past the cookie checks and into a code-exchange
// failure.
type stubOIDCBackend struct {
	exchangeErr error
	// verified, when set, makes the exchange succeed and Verify answer it
	// with the nonce "nonce123": a provider that signed the user in.
	verified *auth.Claims
}

func (stubOIDCBackend) AuthCodeURL(state string, opts ...oauth2.AuthCodeOption) string {
	cfg := oauth2.Config{ClientID: "peeq-test", Endpoint: oauth2.Endpoint{AuthURL: "https://idp.example.com/authorize"}}
	return cfg.AuthCodeURL(state, opts...)
}

func (s stubOIDCBackend) Exchange(context.Context, string, ...oauth2.AuthCodeOption) (*oauth2.Token, error) {
	if s.exchangeErr != nil {
		return nil, s.exchangeErr
	}
	if s.verified != nil {
		return &oauth2.Token{AccessToken: "at"}, nil
	}
	return nil, errors.New("stub: exchange not expected in this test")
}

func (s stubOIDCBackend) Verify(context.Context, *oauth2.Token) (auth.Claims, string, error) {
	if s.verified != nil {
		return *s.verified, "nonce123", nil
	}
	return auth.Claims{}, "", errors.New("stub: verify not expected in this test")
}

// testDepsWithOIDC is testDeps with a real (stub-backed) OIDC flow and no dev
// claims, so login and the callback take the provider path rather than the
// 503 "not configured" path.
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

func testDepsWithOIDCBackend(t *testing.T, backend auth.Backend) Deps {
	t.Helper()
	d := testDeps(t)
	d.DevAuthClaims = auth.Claims{}
	d.OIDC = auth.NewOIDCWithBackend(backend, auth.OIDCConfig{CookiePrefix: "peeq_"})
	return d
}

func TestServer_devLoginThenEmptyVideos(t *testing.T) {
	h := New(testDeps(t))

	loginReq := httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	loginRec := httptest.NewRecorder()
	h.ServeHTTP(loginRec, loginReq)

	var sessionCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == SessionCookie {
			sessionCookie = c
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatalf("expected %s cookie to be set by /api/auth/login, got none (status %d)", SessionCookie, loginRec.Code)
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
// With no session signer wired at all (a wiring bug), every gated route and
// login answer a refusal: requireAuth never fails open.
func TestServer_noSessions_failsClosed(t *testing.T) {
	h := New(Deps{DevAuthClaims: auth.Claims{Subject: "dev"}})
	for path, want := range map[string]int{"/api/videos": 401, "/api/auth/me": 401, "/api/auth/login": 503, "/api/auth/logout": 503} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s = %d, want %d", path, rec.Code, want)
		}
	}
}

func TestServer_noAuthConfigured_failsClosed(t *testing.T) {
	h := New(Deps{
		Sessions: testSessions(t),
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
