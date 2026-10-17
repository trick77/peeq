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

	dev.AllowAnonymousYoutube = true
	if _, s, _, err := newAuth(context.Background(), dev); err != nil || s == nil {
		t.Errorf("dev with anonymous YouTube: %v", err)
	}
}

// OIDC mode reads the provider's discovery document at boot: a provider that
// answers yields the flow, one that does not refuses the boot.
func TestNewAuth_oidcDiscovery(t *testing.T) {
	var issuer string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"` + issuer + `","authorization_endpoint":"` + issuer + `/authorize",` +
			`"token_endpoint":"` + issuer + `/token","jwks_uri":"` + issuer + `/jwks",` +
			`"id_token_signing_alg_values_supported":["RS256"]}`))
	}))
	defer idp.Close()
	issuer = idp.URL

	cfg := config.Config{
		AuthMode:      config.AuthModeOIDC,
		PublicURL:     "https://peeq.example.com",
		SessionSecret: strings.Repeat("k", 32),
		SessionTTL:    time.Hour,
		OIDC: config.OIDCConfig{
			Issuer: issuer, ClientID: "peeq", ClientSecret: "s",
			RedirectURL: "https://peeq.example.com/api/auth/callback",
		},
	}
	oidc, sessions, claims, err := newAuth(context.Background(), cfg)
	if err != nil || oidc == nil || sessions == nil || claims.Subject != "" {
		t.Fatalf("oidc: oidc=%v sessions=%v claims=%+v err=%v", oidc, sessions, claims, err)
	}
	rec := httptest.NewRecorder()
	oidc.Start(rec, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, issuer+"/authorize") ||
		!strings.Contains(loc, "scope=openid+profile+email&") {
		t.Errorf("login redirect = %q, want the provider with openid profile email", loc)
	}
	for _, c := range rec.Result().Cookies() {
		if !c.Secure {
			t.Errorf("cookie %s not Secure behind an https public URL", c.Name)
		}
	}

	idp.Close()
	if _, _, _, err := newAuth(context.Background(), cfg); err == nil {
		t.Error("an unreachable provider booted")
	}
}
