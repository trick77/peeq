package ytdlp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveRelease starts a stub GitHub releases endpoint returning body.
func serveRelease(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GitHub rejects API requests without a User-Agent, so a missing one
		// would fail only in production. Assert it here instead.
		if r.Header.Get("User-Agent") == "" {
			t.Error("release check sent no User-Agent; GitHub would reject it")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestLatestVersionFrom_returnsTag covers the happy path with the exact
// payload shape GitHub serves.
func TestLatestVersionFrom_returnsTag(t *testing.T) {
	url := serveRelease(t, http.StatusOK, `{"tag_name":"2026.07.04","name":"yt-dlp 2026.07.04"}`)

	got, err := latestVersionFrom(context.Background(), url)
	if err != nil {
		t.Fatalf("latestVersionFrom: %v", err)
	}
	if got != "2026.07.04" {
		t.Fatalf("latest = %q, want %q", got, "2026.07.04")
	}
}

// TestLatestVersionFrom_rejectsUnexpectedTagShapes is the guard on the string
// comparison the indicator uses. yt-dlp tags a bare calendar version today;
// were upstream to start prefixing them, "v2026.07.04" would sort ABOVE every
// installed version and pin the indicator to "update available" forever with
// no way for the user to clear it. An unrecognised shape must be an error, not
// a version.
func TestLatestVersionFrom_rejectsUnexpectedTagShapes(t *testing.T) {
	cases := []struct {
		name, tag string
	}{
		{"v prefix", "v2026.07.04"},
		{"semver", "1.2.3"},
		{"unpadded month", "2026.7.4"},
		{"nightly suffix", "2026.07.04.232303"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := serveRelease(t, http.StatusOK, `{"tag_name":"`+tc.tag+`"}`)

			got, err := latestVersionFrom(context.Background(), url)
			if err == nil {
				t.Fatalf("tag %q accepted as version %q, want an error", tc.tag, got)
			}
		})
	}
}

// TestLatestVersionFrom_nonOK_errors covers a rate-limited or erroring GitHub:
// the status must surface rather than be parsed as a release.
func TestLatestVersionFrom_nonOK_errors(t *testing.T) {
	url := serveRelease(t, http.StatusForbidden, `{"message":"API rate limit exceeded"}`)

	if _, err := latestVersionFrom(context.Background(), url); err == nil {
		t.Fatal("403 accepted, want an error")
	} else if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error = %v, want it to name the status", err)
	}
}

// TestLatestVersionFrom_malformedBody_errors covers a proxy or captive portal
// answering with something that is not the release JSON at all.
func TestLatestVersionFrom_malformedBody_errors(t *testing.T) {
	url := serveRelease(t, http.StatusOK, `<html>not json</html>`)

	if _, err := latestVersionFrom(context.Background(), url); err == nil {
		t.Fatal("non-JSON body accepted, want an error")
	}
}

// TestLatestVersionFrom_cancelledContext_errors covers shutdown: an in-flight
// check must abort with the context rather than block the ticker's exit.
func TestLatestVersionFrom_cancelledContext_errors(t *testing.T) {
	url := serveRelease(t, http.StatusOK, `{"tag_name":"2026.07.04"}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := latestVersionFrom(ctx, url); err == nil {
		t.Fatal("cancelled context returned a version, want an error")
	}
}
