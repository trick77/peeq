package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaskPath(t *testing.T) {
	for in, want := range map[string]string{
		"/s/tok123":                  "/s/***",
		"/api/s/tok123":              "/api/s/***",
		"/api/s/tok123/stream":       "/api/s/***/stream",
		"/api/s/tok123/card.jpg":     "/api/s/***/card.jpg",
		"/api/p/grant456/stream":     "/api/p/***/stream",
		"/api/videos/v1/stream":      "/api/videos/v1/stream",
		"/s/":                        "/s/",
		"/api/settings":              "/api/settings",
		"/api/search/answer":         "/api/search/answer",
		"/api/pending/x/thumbnail":   "/api/pending/x/thumbnail",
		"/api/sx/not-a-share-prefix": "/api/sx/not-a-share-prefix",
	} {
		if got := maskPath(httptest.NewRequest(http.MethodGet, in, nil)); got != want {
			t.Errorf("maskPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClientIP(t *testing.T) {
	for _, tc := range []struct {
		name, peer, xff, want string
	}{
		{"behind the private proxy: the hop it appended", "172.18.0.2:5000", "198.51.100.7", "198.51.100.7"},
		{"behind the proxy, spoofed entry ignored", "172.18.0.2:5000", "6.6.6.6, 198.51.100.7", "198.51.100.7"},
		{"loopback proxy (make dev)", "127.0.0.1:5000", "198.51.100.7", "198.51.100.7"},
		{"public peer: its header is its own claim", "203.0.113.9:5000", "6.6.6.6", "203.0.113.9"},
		{"private peer, no header", "172.18.0.2:5000", "", "172.18.0.2"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = tc.peer
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := clientIP(r); got != tc.want {
			t.Errorf("%s: clientIP = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Through New: the access line is kit's, with the share token masked and no
// query string, and a share route is rate limited per caller.
func TestAccessLine_masksTheTokenAndLimitsTheCaller(t *testing.T) {
	logs := captureLogs(t)
	deps, _ := shareMetaDeps(t)
	h := New(deps)

	rec := getPublic(t, h, "/api/s/sekrit-token/thumbnail?x=querysecret")
	if rec.Code == http.StatusTooManyRequests {
		t.Fatalf("first request limited")
	}
	out := logs.String()
	if !strings.Contains(out, "msg=http_request") || !strings.Contains(out, "path=/api/s/***/thumbnail") {
		t.Fatalf("no masked access line: %s", out)
	}
	if strings.Contains(out, "sekrit-token") || strings.Contains(out, "querysecret") {
		t.Fatalf("token or query reached the log: %s", out)
	}

	limited := false
	for range int(tokenRouteLimit.Burst) + 1 {
		if getPublic(t, h, "/api/s/sekrit-token").Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("a share route was never rate limited")
	}
}
