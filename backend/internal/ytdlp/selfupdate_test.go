package ytdlp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestVersion_parsesBinOutput(t *testing.T) {
	t.Setenv("FAKE_YTDLP_VERSION", "2024.07.01")
	p, err := filepath.Abs("testdata/fake-ytdlp.sh")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Version(context.Background(), p)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if got != "2024.07.01" {
		t.Fatalf("Version = %q, want %q", got, "2024.07.01")
	}
}

func TestVersion_binMissing(t *testing.T) {
	if _, err := Version(context.Background(), filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("expected error for missing binary")
	}
}

// TestUpdateLatest_usesInjectedDownloader locks the seam that lets tests
// (and this test) avoid ever hitting the network: UpdateLatest delegates
// the actual fetch to the package-level downloader variable, which tests
// can swap for a fake that just writes a placeholder file and reports a
// version.
func TestUpdateLatest_usesInjectedDownloader(t *testing.T) {
	dir := t.TempDir()

	prev := downloader
	t.Cleanup(func() { downloader = prev })

	var gotDest string
	downloader = func(ctx context.Context, destPath string) (string, error) {
		gotDest = destPath
		if err := os.WriteFile(destPath, []byte("fake binary"), 0o755); err != nil {
			return "", err
		}
		return "2024.09.01", nil
	}

	got, err := UpdateLatest(context.Background(), dir)
	if err != nil {
		t.Fatalf("UpdateLatest: %v", err)
	}
	if got != "2024.09.01" {
		t.Fatalf("UpdateLatest version = %q, want %q", got, "2024.09.01")
	}
	if filepath.Dir(gotDest) != dir {
		t.Fatalf("downloader destPath dir = %q, want %q", filepath.Dir(gotDest), dir)
	}
	if _, err := os.Stat(gotDest); err != nil {
		t.Fatalf("expected downloaded file to exist: %v", err)
	}
}

func TestUpdateLatest_propagatesDownloaderError(t *testing.T) {
	prev := downloader
	t.Cleanup(func() { downloader = prev })

	downloader = func(ctx context.Context, destPath string) (string, error) {
		return "", os.ErrPermission
	}

	if _, err := UpdateLatest(context.Background(), t.TempDir()); err == nil {
		t.Fatal("expected error to propagate from downloader")
	}
}

// TestDownloadReleaseFrom_success_replacesFile drives downloadReleaseFrom
// against a real httptest.Server (never the real GitHub URL) and checks
// the happy path: the served bytes land at destPath and the file is
// executable.
func TestDownloadReleaseFrom_success_replacesFile(t *testing.T) {
	const body = "#!/bin/sh\necho 2099.01.01\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	dir := t.TempDir()
	destPath := filepath.Join(dir, "yt-dlp")

	version, err := downloadReleaseFrom(context.Background(), srv.URL, destPath)
	if err != nil {
		t.Fatalf("downloadReleaseFrom: %v", err)
	}
	if version != "2099.01.01" {
		t.Fatalf("version = %q, want %q", version, "2099.01.01")
	}

	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read destPath: %v", err)
	}
	if string(got) != body {
		t.Fatalf("destPath contents = %q, want %q", got, body)
	}

	info, err := os.Stat(destPath)
	if err != nil {
		t.Fatalf("stat destPath: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("destPath mode %v not executable", info.Mode())
	}

	// No leftover temp download file should remain in the same directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "yt-dlp" {
			t.Fatalf("unexpected leftover file %q in %q", e.Name(), dir)
		}
	}
}

// TestDownloadReleaseFrom_failure_leavesExistingBinaryIntact is the core
// atomicity guarantee: a failing (500) download must not truncate or
// otherwise touch a pre-existing binary at destPath. Before the atomic
// rewrite, downloadLatestRelease opened destPath with O_TRUNC up front and
// streamed the body straight into it, so a failed/short download left a
// zero-byte or partial file in place of a working binary. This proves
// that regression is fixed: the old bytes are byte-for-byte unchanged
// after a failed download attempt.
func TestDownloadReleaseFrom_failure_leavesExistingBinaryIntact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	destPath := filepath.Join(dir, "yt-dlp")
	const existing = "#!/bin/sh\necho 2024.01.01\n"
	if err := os.WriteFile(destPath, []byte(existing), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := downloadReleaseFrom(context.Background(), srv.URL, destPath); err == nil {
		t.Fatal("expected error from failing download, got nil")
	}

	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read destPath after failed download: %v", err)
	}
	if string(got) != existing {
		t.Fatalf("destPath contents after failed download = %q, want unchanged %q", got, existing)
	}

	// No leftover temp download file should remain in the same directory
	// either — the failure path must clean up after itself.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "yt-dlp" {
			t.Fatalf("unexpected leftover file %q in %q after failed download", e.Name(), dir)
		}
	}
}

// TestDownloadReleaseFrom_midDownloadFailure_leavesExistingBinaryIntact
// exercises the specific regression this fix targets: a *200* response
// whose body is cut off mid-stream (declared Content-Length larger than
// what's actually sent, then the connection drops). The pre-fix
// implementation opened destPath with O_TRUNC before starting the copy,
// so a failure here truncated the live binary in place with no rollback.
// This test hijacks the raw connection to force exactly that: a 200
// status followed by a short, incomplete body.
func TestDownloadReleaseFrom_midDownloadFailure_leavesExistingBinaryIntact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		// Declare far more bytes than we actually send, then close the
		// connection: the client's body read fails partway through with
		// an unexpected-EOF, simulating a dropped/interrupted download.
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 10000000\r\n\r\n")
		_, _ = buf.WriteString("only a few bytes before the connection dies")
		_ = buf.Flush()
	}))
	defer srv.Close()

	dir := t.TempDir()
	destPath := filepath.Join(dir, "yt-dlp")
	const existing = "#!/bin/sh\necho 2024.01.01\n"
	if err := os.WriteFile(destPath, []byte(existing), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := downloadReleaseFrom(context.Background(), srv.URL, destPath); err == nil {
		t.Fatal("expected error from mid-download failure, got nil")
	}

	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("read destPath after mid-download failure: %v", err)
	}
	if string(got) != existing {
		t.Fatalf("destPath contents after mid-download failure = %q (len %d), want unchanged %q (len %d) — the live binary must never be truncated by a failed download",
			got, len(got), existing, len(existing))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "yt-dlp" {
			t.Fatalf("unexpected leftover file %q in %q after mid-download failure", e.Name(), dir)
		}
	}
}
