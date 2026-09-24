package httpapi

import (
	"net/http"
	"testing"

	"github.com/trick77/peeq/internal/ytdlp"
)

// breakPausedRead makes the kill-switch row unreadable without touching
// anything else the handlers under test consult.
func breakPausedRead(t *testing.T, exec func(query string, args ...any) error) {
	t.Helper()
	if err := exec(`ALTER TABLE settings RENAME COLUMN youtube_paused TO youtube_paused_x`); err != nil {
		t.Fatalf("break settings read: %v", err)
	}
}

// TestDownloadsStatus_pauseReadError_500: an unreadable kill switch is a server
// fault, reported as one — not rendered as a pause the operator never set
// (which would put a Resume button in front of them that cannot work).
func TestDownloadsStatus_pauseReadError_500(t *testing.T) {
	deps, db := downloadsTestDepsDB(t, &fakeDownloadsRunner{})
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	breakPausedRead(t, func(q string, args ...any) error { _, err := db.Exec(q, args...); return err })

	rec := doRequest(t, h, sessionCookie, http.MethodGet, "/api/downloads/status")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", rec.Code, rec.Body.String())
	}
}

// TestChannelScan_pauseReadError_500 is the same contract for the scan-now
// endpoint, which used to answer "blocked" with an invented reason.
func TestChannelScan_pauseReadError_500(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCs", Name: "S"}})
	h := New(deps)
	if rec := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@s", "subscribe": true}); rec.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
	breakPausedRead(t, func(q string, args ...any) error { _, err := deps.Channels.DB().Exec(q, args...); return err })

	rec := postJSON(t, h, "/api/channels/UCs/scan", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body %s", rec.Code, rec.Body.String())
	}
}
