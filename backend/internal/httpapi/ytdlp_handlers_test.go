package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
)

// fakeYTDLPVersioner is a YTDLPVersioner double so tests never shell out to
// a real yt-dlp binary.
type fakeYTDLPVersioner struct {
	version    string
	updated    string
	versionErr error
	updateErr  error
}

func (f *fakeYTDLPVersioner) Version(context.Context) (string, error) {
	return f.version, f.versionErr
}

func (f *fakeYTDLPVersioner) UpdateLatest(context.Context) (string, error) {
	return f.updated, f.updateErr
}

// TestYTDLPVersion_returnsVersion covers the Settings page's version
// display reading through the wired YTDLPVersioner.
func TestYTDLPVersion_returnsVersion(t *testing.T) {
	deps := testDeps(t)
	deps.YTDLP = &fakeYTDLPVersioner{version: "2026.07.01"}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/ytdlp/version", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/ytdlp/version status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["version"] != "2026.07.01" {
		t.Fatalf("version = %q, want %q", got["version"], "2026.07.01")
	}
}

// TestYTDLPVersion_unconfigured_returns503 covers the fail-safe default: no
// YTDLPVersioner wired means the endpoint reports unavailable rather than
// panicking on a nil dereference.
func TestYTDLPVersion_unconfigured_returns503(t *testing.T) {
	h := New(testDeps(t))
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/ytdlp/version", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/ytdlp/version (unconfigured) status = %d, want 503, body = %s", rec.Code, rec.Body.String())
	}
}

// TestYTDLPUpdate_returnsUpdatedVersion covers the Update button's happy
// path.
func TestYTDLPUpdate_returnsUpdatedVersion(t *testing.T) {
	deps := testDeps(t)
	deps.YTDLP = &fakeYTDLPVersioner{updated: "2026.08.15"}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/ytdlp/update", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/ytdlp/update status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["version"] != "2026.08.15" {
		t.Fatalf("version = %q, want %q", got["version"], "2026.08.15")
	}
}

// TestYTDLPUpdate_error_returns500 covers the update-failure path (e.g. a
// network error downloading the release) surfacing as a 500 rather than a
// silently-swallowed success.
func TestYTDLPUpdate_error_returns500(t *testing.T) {
	deps := testDeps(t)
	deps.YTDLP = &fakeYTDLPVersioner{updateErr: errors.New("download failed")}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/ytdlp/update", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST /api/ytdlp/update (error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}
