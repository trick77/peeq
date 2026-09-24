package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireAuthRejectsMissingSession(t *testing.T) {
	mw := NewMiddleware(fakeSessionLookup{}, fakeUserLookup{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)

	mw.RequireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not run")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRequireAuthRejectsUnknownSessionToken(t *testing.T) {
	mw := NewMiddleware(fakeSessionLookup{ok: false}, fakeUserLookup{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "bogus"})

	mw.RequireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not run")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestRequireAuthStoresUserInContext(t *testing.T) {
	user := User{ID: "u1", Username: "jan", Role: RoleAdmin}
	mw := NewMiddleware(
		fakeSessionLookup{session: Session{Token: "tok", UserID: "u1"}, ok: true},
		fakeUserLookup{user: user, ok: true},
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "tok"})

	mw.RequireAuth(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, ok := UserFromContext(r.Context())
		if !ok {
			t.Fatal("user missing from context")
		}
		if got.ID != user.ID {
			t.Fatalf("user id = %q, want %q", got.ID, user.ID)
		}
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestUserFromContext_AbsentByDefault(t *testing.T) {
	_, ok := UserFromContext(context.Background())
	if ok {
		t.Fatal("UserFromContext() = found, want not found on bare context")
	}
}

type fakeSessionLookup struct {
	session Session
	ok      bool
	err     error
}

func (f fakeSessionLookup) Lookup(context.Context, string) (Session, bool, error) {
	return f.session, f.ok, f.err
}

type fakeUserLookup struct {
	user User
	ok   bool
	err  error
}

func (f fakeUserLookup) FindByID(context.Context, string) (User, bool, error) {
	return f.user, f.ok, f.err
}

// captureLogs redirects slog's default logger into a buffer for the duration
// of the test and restores it afterwards.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// TestRequireAuthLogsSessionLookupFailure asserts that a session store fault
// in front of a protected route is not a silent 500: the middleware runs on
// every authenticated request, so a database fault here would otherwise leave
// no trace beyond the access line.
func TestRequireAuthLogsSessionLookupFailure(t *testing.T) {
	logs := captureLogs(t)
	mw := NewMiddleware(fakeSessionLookup{err: errors.New("session boom")}, fakeUserLookup{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/me?token=secret-query", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "sess-secret-token"})

	mw.RequireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not run")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "session lookup failed") {
		t.Fatalf("body = %s, want the generic message", rec.Body.String())
	}
	out := logs.String()
	if !strings.Contains(out, "request failed") || !strings.Contains(out, "err=\"session boom\"") {
		t.Fatalf("log should carry the cause, got: %s", out)
	}
	if !strings.Contains(out, "path=/api/me") || strings.Contains(out, "secret-query") {
		t.Fatalf("log should carry the path and never the query string, got: %s", out)
	}
	if strings.Contains(out, "sess-secret-token") {
		t.Fatalf("log must never carry the session token, got: %s", out)
	}
}

// TestRequireAuthLogsUserLookupFailure is the same guarantee for the user
// lookup that follows a valid session.
func TestRequireAuthLogsUserLookupFailure(t *testing.T) {
	logs := captureLogs(t)
	mw := NewMiddleware(
		fakeSessionLookup{session: Session{UserID: "u1"}, ok: true},
		fakeUserLookup{err: errors.New("user boom")},
	)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "tok"})

	mw.RequireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not run")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	out := logs.String()
	if !strings.Contains(out, "request failed") || !strings.Contains(out, "err=\"user boom\"") {
		t.Fatalf("log should carry the cause, got: %s", out)
	}
}
