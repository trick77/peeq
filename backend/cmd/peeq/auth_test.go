package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/config"
	"github.com/trick77/ship/kit/auth"
)

// issued signs c with s and returns the request a browser would send back.
func issued(t *testing.T, s *auth.Sessions, c auth.Claims) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := s.Issue(rec, c); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, ck := range rec.Result().Cookies() {
		req.AddCookie(ck)
	}
	return req
}

// A configured secret keeps sessions across a restart; dev without one signs
// out on every start. An unset auth mode refuses to boot.
func TestNewAuth(t *testing.T) {
	dev := config.Config{AuthMode: config.AuthModeDev, SessionTTL: time.Hour,
		Dev: config.DevUserConfig{Subject: "dev-admin", Username: "dev"}}
	oidc, s1, claims, err := newAuth(context.Background(), dev)
	if err != nil || oidc != nil || claims.Subject != "dev-admin" {
		t.Fatalf("dev: oidc=%v claims=%+v err=%v", oidc, claims, err)
	}
	_, s2, _, _ := newAuth(context.Background(), dev)
	if _, ok := s2.Read(issued(t, s1, claims)); ok {
		t.Error("dev without a secret: a session survived a restart")
	}

	dev.SessionSecret = strings.Repeat("k", 32)
	_, s1, _, _ = newAuth(context.Background(), dev)
	_, s2, _, _ = newAuth(context.Background(), dev)
	if got, ok := s2.Read(issued(t, s1, claims)); !ok || got.Subject != "dev-admin" {
		t.Error("with a secret: the session did not survive a restart")
	}

	if _, _, _, err := newAuth(context.Background(), config.Config{SessionTTL: time.Hour}); err == nil ||
		!strings.Contains(err.Error(), "BACKEND_AUTH_MODE") {
		t.Errorf("unset auth mode: err = %v", err)
	}
}
