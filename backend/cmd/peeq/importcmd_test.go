package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/taimport"
)

func TestFormatChannelResult_realRun(t *testing.T) {
	res := taimport.ChannelResult{
		Subscribed: 12, Active: 10, Inactive: 2, Skipped: 37,
		InactiveNames: []string{"Dead Channel", "Gone Too"},
	}

	got := formatChannelResult(res, false)

	// Assert each count on the same line as its label, so swapping the
	// active/inactive lines (or dropping the labels) fails this test. A bare
	// strings.Contains(got, "2") would also match inside "12", so the label
	// must be part of the match.
	for _, want := range []string{
		"Subscriptions:  12",
		"active:       10",
		"inactive:     2",
		"Skipped:        37",
		"Dead Channel",
		"Gone Too",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "dry run") {
		t.Errorf("real run mentioned a dry run:\n%s", got)
	}
	// TA's channel_active=false means its last refresh failed to fetch the
	// channel — usually transient, NOT that the channel is gone. The summary
	// must not mislabel it that way or promise it will be auto-retired; it must
	// say peeq re-checks each channel itself.
	low := strings.ToLower(got)
	if strings.Contains(low, "gone from youtube") {
		t.Errorf("mislabels a TA-inactive channel as gone from YouTube:\n%s", got)
	}
	if !strings.Contains(low, "re-check") {
		t.Errorf("does not explain that peeq re-checks inactive channels:\n%s", got)
	}
}

func TestFormatChannelResult_dryRunSaysSo(t *testing.T) {
	res := taimport.ChannelResult{Subscribed: 3, Active: 3}

	got := formatChannelResult(res, true)

	if !strings.Contains(strings.ToLower(got), "dry run") {
		t.Errorf("dry run not labelled:\n%s", got)
	}
	if !strings.Contains(strings.ToLower(got), "nothing was written") {
		t.Errorf("dry run did not say nothing was written:\n%s", got)
	}
}

func TestFormatChannelResult_noInactiveOmitsTheList(t *testing.T) {
	res := taimport.ChannelResult{Subscribed: 5, Active: 5}

	got := formatChannelResult(res, false)

	if strings.Contains(strings.ToLower(got), "inactive channel") {
		t.Errorf("listed inactive channels when there are none:\n%s", got)
	}
}

func TestRunImportChannels_requiresURLAndToken(t *testing.T) {
	if err := runImportChannels([]string{}); err == nil {
		t.Fatal("err = nil, want an error when --ta-url is missing")
	}
	if err := runImportChannels([]string{"--ta-url", "http://ta:8000"}); err == nil {
		t.Fatal("err = nil, want an error when --ta-token is missing")
	}
}

func TestRunImportChannels_helpReturnsNil(t *testing.T) {
	// --help must not look like a failure: flag.ContinueOnError makes
	// fs.Parse return flag.ErrHelp, which must not propagate as an error
	// after usage has already been printed.
	if err := runImportChannels([]string{"--help"}); err != nil {
		t.Errorf("runImportChannels([--help]) = %v, want nil", err)
	}
}

func TestDispatchSubcommand_noArgsFallsThrough(t *testing.T) {
	// This is exactly how the container starts the server: no arguments at
	// all. It MUST fall through rather than be treated as handled.
	handled, err := dispatchSubcommand([]string{"peeq"})
	if handled {
		t.Error("handled = true, want false (no subcommand named)")
	}
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestDispatchSubcommand_unknownArgFallsThrough(t *testing.T) {
	handled, err := dispatchSubcommand([]string{"peeq", "serve-nonsense"})
	if handled {
		t.Error("handled = true, want false (unrecognised subcommand)")
	}
	if err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestDispatchSubcommand_dispatchesAndPropagatesError(t *testing.T) {
	// Register a fake subcommand rather than exercising a real one, and
	// restore the map afterwards so this test cannot leak state into others.
	const name = "test-only-fake"
	if _, exists := importSubcommands[name]; exists {
		t.Fatalf("subcommand %q already registered; pick a different name", name)
	}

	var gotArgs []string
	wantErr := errors.New("boom")
	importSubcommands[name] = func(a []string) error {
		gotArgs = a
		return wantErr
	}
	defer delete(importSubcommands, name)

	handled, err := dispatchSubcommand([]string{"peeq", name, "--x", "1"})

	if !handled {
		t.Error("handled = false, want true (subcommand was registered)")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if !reflect.DeepEqual(gotArgs, []string{"--x", "1"}) {
		t.Errorf("gotArgs = %v, want [--x 1] (args after the subcommand name)", gotArgs)
	}
}

// taServer starts an httptest server serving TubeArchivist's /api/channel/
// envelope shape for page 1 and a 404 (TubeArchivist's "no more results"
// signal) for every subsequent page.
func taServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"channel_id":"UC_a","channel_name":"Alpha","channel_active":true,"channel_subscribed":true}
		]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setImportEnv sets the env vars config.Load() requires, targeting a fresh
// temp-file SQLite database, dev auth, and loopback binding (dev auth
// requires it). t.Setenv auto-restores everything after the test.
func setImportEnv(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	t.Setenv("BACKEND_DB_PATH", dbPath)
	t.Setenv("BACKEND_SESSION_SECRET", "test-secret")
	t.Setenv("BACKEND_CHAT_BASE_URL", "http://chat.invalid")
	t.Setenv("BACKEND_EMBED_BASE_URL", "http://embed.invalid")
	t.Setenv("BACKEND_EMBED_MODEL", "test-model")
	t.Setenv("BACKEND_AUTH_MODE", "dev")
	t.Setenv("BACKEND_ADDR", "127.0.0.1:8080")
	t.Setenv("BACKEND_PUBLIC_URL", "")
	return dbPath
}

func subscriptionCount(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM subscriptions").Scan(&n); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	return n
}

func TestRunImportChannels_writesRealRows(t *testing.T) {
	dbPath := setImportEnv(t)
	srv := taServer(t)

	if err := runImportChannels([]string{"--ta-url", srv.URL, "--ta-token", "t"}); err != nil {
		t.Fatalf("runImportChannels: %v", err)
	}

	if got := subscriptionCount(t, dbPath); got != 1 {
		t.Errorf("subscriptions = %d, want 1 (the wiring must actually write rows, not just return nil)", got)
	}
}

func TestRunImportChannels_dryRunWritesNoRows(t *testing.T) {
	dbPath := setImportEnv(t)
	srv := taServer(t)

	if err := runImportChannels([]string{"--ta-url", srv.URL, "--ta-token", "t", "--dry-run"}); err != nil {
		t.Fatalf("runImportChannels: %v", err)
	}

	if got := subscriptionCount(t, dbPath); got != 0 {
		t.Errorf("subscriptions = %d, want 0 on --dry-run", got)
	}
}

func TestRunImportVideos_requiresFlags(t *testing.T) {
	if err := runImportVideos([]string{}); err == nil {
		t.Fatal("err = nil, want an error when --ta-url is missing")
	}
	if err := runImportVideos([]string{"--ta-url", "http://ta:8000"}); err == nil {
		t.Fatal("err = nil, want an error when --ta-token is missing")
	}
	if err := runImportVideos([]string{"--ta-url", "http://ta:8000", "--ta-token", "t"}); err == nil {
		t.Fatal("err = nil, want an error when --ta-media is missing")
	}
	if err := runImportVideos([]string{"--ta-url", "http://ta:8000", "--ta-token", "t", "--ta-media", "/m"}); err == nil {
		t.Fatal("err = nil, want an error when --ta-cache is missing")
	}
}

func TestFormatVideoResult_dryRunAndReal(t *testing.T) {
	dry := formatVideoResult(taimport.VideoResult{Planned: 5, BytesMedia: 3 * 1024 * 1024}, true)
	if !strings.Contains(strings.ToLower(dry), "dry run") {
		t.Errorf("dry run not labelled:\n%s", dry)
	}
	if !strings.Contains(dry, "5 videos") {
		t.Errorf("dry run missing the planned count:\n%s", dry)
	}

	real := formatVideoResult(taimport.VideoResult{Imported: 4, SkippedDownloaded: 2, BytesMedia: 1024 * 1024}, false)
	if strings.Contains(strings.ToLower(real), "dry run") {
		t.Errorf("real run mentioned a dry run:\n%s", real)
	}
	for _, want := range []string{"Imported:", "4 videos", "2 already imported"} {
		if !strings.Contains(real, want) {
			t.Errorf("real output missing %q:\n%s", want, real)
		}
	}
}

func TestFormatVideoResult_reportsResumeCount(t *testing.T) {
	out := formatVideoResult(taimport.VideoResult{Imported: 3, WithResume: 2}, false)
	if !strings.Contains(out, "2 imported with a saved resume position") {
		t.Errorf("resume-position count missing or wrong:\n%s", out)
	}
}

func TestSelectChannelIDs(t *testing.T) {
	chans := []taimport.Channel{{ID: "UC1"}, {ID: "UC2"}, {ID: "UC3"}}
	if got := selectChannelIDs(chans, nil, 0); len(got) != 3 {
		t.Errorf("all = %v, want 3", got)
	}
	if got := selectChannelIDs(chans, nil, 2); len(got) != 2 || got[0] != "UC1" {
		t.Errorf("first-2 = %v", got)
	}
	if got := selectChannelIDs(chans, []string{"UC2"}, 0); len(got) != 1 || got[0] != "UC2" {
		t.Errorf("filter = %v", got)
	}
}

func TestParseTypes(t *testing.T) {
	if got := parseTypes(""); got != nil {
		t.Errorf("empty = %v, want nil (all types)", got)
	}
	got := parseTypes("videos, shorts ,streams")
	if len(got) != 3 || got[0] != "videos" || got[1] != "shorts" || got[2] != "streams" {
		t.Errorf("parsed = %v", got)
	}
}

// taVideoServer serves both /api/channel/ (one subscribed channel) and
// /api/video/ (one unwatched video on page 1, empty after) so runImportVideos
// can be exercised end to end.
func taVideoServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/channel/"):
			if r.URL.Query().Get("page") != "1" {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"channel_id":"UC_a","channel_name":"Alpha","channel_active":true,"channel_subscribed":true}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/video/"):
			if r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte(`{"data":[]}`))
				return
			}
			_, _ = w.Write([]byte(`{"data":[{"youtube_id":"vid00000001","channel":{"channel_id":"UC_a","channel_name":"Alpha"},"title":"V","player":{"duration":100},"vid_type":"videos"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func setupTAMedia(t *testing.T) (taMedia, taCache string) {
	t.Helper()
	taMedia, taCache = t.TempDir(), t.TempDir()
	dir := filepath.Join(taMedia, "UC_a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vid00000001.mp4"), []byte("mp4-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return taMedia, taCache
}

func videoCount(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM videos").Scan(&n); err != nil {
		t.Fatalf("count videos: %v", err)
	}
	return n
}

func TestRunImportVideos_writesRealRows(t *testing.T) {
	dbPath := setImportEnv(t)
	t.Setenv("BACKEND_MEDIA_DIR", t.TempDir())
	taMedia, taCache := setupTAMedia(t)
	srv := taVideoServer(t)

	if err := runImportVideos([]string{"--ta-url", srv.URL, "--ta-token", "t", "--ta-media", taMedia, "--ta-cache", taCache}); err != nil {
		t.Fatalf("runImportVideos: %v", err)
	}

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	var status string
	if err := db.QueryRow(`SELECT status FROM videos WHERE id='vid00000001'`).Scan(&status); err != nil {
		t.Fatalf("query video: %v", err)
	}
	if status != "downloaded" {
		t.Errorf("status = %q, want downloaded (the wiring must actually write rows)", status)
	}
}

func TestRunImportVideos_dryRunWritesNoRows(t *testing.T) {
	dbPath := setImportEnv(t)
	t.Setenv("BACKEND_MEDIA_DIR", t.TempDir())
	taMedia, taCache := setupTAMedia(t)
	srv := taVideoServer(t)

	if err := runImportVideos([]string{"--ta-url", srv.URL, "--ta-token", "t", "--ta-media", taMedia, "--ta-cache", taCache, "--dry-run"}); err != nil {
		t.Fatalf("runImportVideos: %v", err)
	}
	if got := videoCount(t, dbPath); got != 0 {
		t.Errorf("videos = %d, want 0 on --dry-run", got)
	}
}
