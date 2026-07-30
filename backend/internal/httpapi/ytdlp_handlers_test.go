package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

// fakeYTDLPVersioner is a YTDLPVersioner double so tests never shell out to
// a real yt-dlp binary.
type fakeYTDLPVersioner struct {
	version    string
	updated    string
	versionErr error
	updateErr  error

	latest    string
	checkedAt time.Time
	checkErr  string
}

func (f *fakeYTDLPVersioner) Version(context.Context) (string, error) {
	return f.version, f.versionErr
}

func (f *fakeYTDLPVersioner) UpdateLatest(context.Context) (string, error) {
	return f.updated, f.updateErr
}

func (f *fakeYTDLPVersioner) Latest(context.Context) (string, time.Time, string) {
	return f.latest, f.checkedAt, f.checkErr
}

// TestYTDLPVersion_returnsVersion covers the Settings page's version
// display reading through the wired YTDLPVersioner.
func TestYTDLPVersion_returnsVersion(t *testing.T) {
	got := getYTDLPVersion(t, &fakeYTDLPVersioner{version: "2026.07.01"})
	if got.Version != "2026.07.01" {
		t.Fatalf("version = %q, want %q", got.Version, "2026.07.01")
	}
}

// getYTDLPVersion is the shared arrange/act for the version endpoint: wire
// the double, call, and decode.
func getYTDLPVersion(t *testing.T, f *fakeYTDLPVersioner) ytdlpVersionResponse {
	t.Helper()
	deps := testDeps(t)
	deps.YTDLP = f
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/ytdlp/version", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/ytdlp/version status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got ytdlpVersionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return got
}

// TestYTDLPVersion_behindLatest_reportsUpdateAvailable is the signal the nav
// rail's indicator is drawn from.
func TestYTDLPVersion_behindLatest_reportsUpdateAvailable(t *testing.T) {
	checked := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	got := getYTDLPVersion(t, &fakeYTDLPVersioner{
		version: "2026.07.01", latest: "2026.08.15", checkedAt: checked,
	})

	want := ytdlpVersionResponse{
		Version:         "2026.07.01",
		Latest:          "2026.08.15",
		UpdateAvailable: true,
		CheckedAt:       "2026-07-30T08:00:00Z",
	}
	if got != want {
		t.Fatalf("version response = %+v, want %+v", got, want)
	}
}

// TestYTDLPVersion_atLatest_reportsNoUpdate keeps the indicator silent on the
// ordinary case.
func TestYTDLPVersion_atLatest_reportsNoUpdate(t *testing.T) {
	got := getYTDLPVersion(t, &fakeYTDLPVersioner{version: "2026.08.15", latest: "2026.08.15"})
	if got.UpdateAvailable {
		t.Fatalf("update_available = true for an installed version equal to latest: %+v", got)
	}
}

// TestYTDLPVersion_aheadOfLatest_reportsNoUpdate covers a nightly or
// self-built binary NEWER than the last stable release. Comparing for
// inequality rather than order would nag that user forever to "update" to an
// older build.
func TestYTDLPVersion_aheadOfLatest_reportsNoUpdate(t *testing.T) {
	got := getYTDLPVersion(t, &fakeYTDLPVersioner{version: "2026.09.02", latest: "2026.08.15"})
	if got.UpdateAvailable {
		t.Fatalf("update_available = true for an installed version ahead of latest: %+v", got)
	}
}

// TestYTDLPVersion_beforeFirstCheck_reportsNoUpdate covers the cold cache at
// boot: an unknown latest release must not be read as "you are behind".
func TestYTDLPVersion_beforeFirstCheck_reportsNoUpdate(t *testing.T) {
	got := getYTDLPVersion(t, &fakeYTDLPVersioner{version: "2026.07.01"})
	if got.UpdateAvailable || got.Latest != "" || got.CheckedAt != "" {
		t.Fatalf("cold cache leaked a latest release: %+v", got)
	}
}

// TestYTDLPVersion_checkFailed_reportsErrorAndKeepsLatest covers an
// unreachable GitHub. The stale latest is still returned — it is the best
// information available — and the error rides along so the page can say the
// answer has stopped refreshing rather than implying all is well.
func TestYTDLPVersion_checkFailed_reportsErrorAndKeepsLatest(t *testing.T) {
	checked := time.Date(2026, 7, 28, 6, 0, 0, 0, time.UTC)
	got := getYTDLPVersion(t, &fakeYTDLPVersioner{
		version: "2026.07.01", latest: "2026.08.15", checkedAt: checked,
		checkErr: "dial tcp: lookup api.github.com: no such host",
	})

	if got.CheckError == "" {
		t.Fatalf("check_error dropped: %+v", got)
	}
	if got.Latest != "2026.08.15" || !got.UpdateAvailable {
		t.Fatalf("a failed check discarded the last known release: %+v", got)
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
	var got ytdlpUpdateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != "2026.08.15" {
		t.Fatalf("version = %q, want %q", got.Version, "2026.08.15")
	}
}

// TestYTDLPUpdate_versionChanged_reportsUpdated covers the Update button's
// receipt for a real upgrade: the pre-update version comes back alongside the
// new one so the page can say what moved.
func TestYTDLPUpdate_versionChanged_reportsUpdated(t *testing.T) {
	deps := testDeps(t)
	deps.YTDLP = &fakeYTDLPVersioner{version: "2026.07.01", updated: "2026.08.15"}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/ytdlp/update", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/ytdlp/update status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got ytdlpUpdateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := ytdlpUpdateResponse{Version: "2026.08.15", PreviousVersion: "2026.07.01", Updated: true}
	if got != want {
		t.Fatalf("update response = %+v, want %+v", got, want)
	}
}

// TestYTDLPUpdate_versionUnchanged_reportsNotUpdated is the case the Settings
// page could not previously distinguish: the reinstall happened but landed on
// the same version, and the button must be able to say so.
func TestYTDLPUpdate_versionUnchanged_reportsNotUpdated(t *testing.T) {
	deps := testDeps(t)
	deps.YTDLP = &fakeYTDLPVersioner{version: "2026.08.15", updated: "2026.08.15"}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/ytdlp/update", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/ytdlp/update status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got ytdlpUpdateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := ytdlpUpdateResponse{Version: "2026.08.15", PreviousVersion: "2026.08.15", Updated: false}
	if got != want {
		t.Fatalf("update response = %+v, want %+v", got, want)
	}
}

// TestYTDLPUpdate_versionReadFails_stillUpdates covers an unreadable or
// missing binary — exactly when an update is most wanted. Failing to read the
// version it is about to replace must not block the update; it just leaves no
// previous version to report.
func TestYTDLPUpdate_versionReadFails_stillUpdates(t *testing.T) {
	deps := testDeps(t)
	deps.YTDLP = &fakeYTDLPVersioner{
		versionErr: errors.New("no such file"),
		updated:    "2026.08.15",
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/ytdlp/update", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/ytdlp/update status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	var got ytdlpUpdateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := ytdlpUpdateResponse{Version: "2026.08.15", PreviousVersion: "", Updated: true}
	if got != want {
		t.Fatalf("update response = %+v, want %+v", got, want)
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
