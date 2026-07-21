package media

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFetchImage_savesAndReturnsRelativePath asserts the image lands under
// mediaDir and the returned path is relative, matching how subtitle_path is
// stored (SafeMediaPath resolves both, but relative is what new code writes).
func TestFetchImage_savesAndReturnsRelativePath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff fake jpeg bytes"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	rel, err := FetchImage(context.Background(), srv.URL, dir, ".channels/UCx/avatar")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if filepath.IsAbs(rel) {
		t.Fatalf("returned path %q is absolute, want relative", rel)
	}
	if !strings.HasSuffix(rel, ".jpg") {
		t.Fatalf("returned path %q has no jpeg extension", rel)
	}
	if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

// TestFetchImage_rejectsNonImage asserts an HTML error page served with a 200
// is not written to disk as if it were an avatar.
func TestFetchImage_rejectsNonImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>nope</html>"))
	}))
	defer srv.Close()

	if _, err := FetchImage(context.Background(), srv.URL, t.TempDir(), ".channels/UCx/avatar"); err == nil {
		t.Fatal("expected an error for a non-image content type")
	}
}

// TestFetchImage_rejectsOversizeBody asserts a hostile or broken server
// cannot fill the disk through this path.
func TestFetchImage_rejectsOversizeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		big := make([]byte, maxImageBytes+1024)
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	if _, err := FetchImage(context.Background(), srv.URL, t.TempDir(), ".channels/UCx/avatar"); err == nil {
		t.Fatal("expected an error for an oversize body")
	}
}

// TestFetchImage_emptyURL asserts a channel with no banner is a no-op rather
// than an error the caller has to special-case.
func TestFetchImage_emptyURL(t *testing.T) {
	rel, err := FetchImage(context.Background(), "", t.TempDir(), ".channels/UCx/banner")
	if err != nil {
		t.Fatalf("empty url should not error: %v", err)
	}
	if rel != "" {
		t.Fatalf("rel = %q, want empty", rel)
	}
}

// TestFetchImage_emptyMediaDir asserts a misconfigured media dir is reported
// as an error rather than attempting to write outside any known root.
func TestFetchImage_emptyMediaDir(t *testing.T) {
	_, err := FetchImage(context.Background(), "http://example.com/x.jpg", "", ".channels/UCx/avatar")
	if err == nil {
		t.Fatal("expected an error for an unconfigured media dir")
	}
	if !strings.Contains(err.Error(), "media dir not configured") {
		t.Fatalf("err = %v, want mention of unconfigured media dir", err)
	}
}

// TestFetchImage_rejectsMalformedURL asserts a URL that fails to parse into
// an *http.Request is reported rather than panicking or being silently
// swallowed.
func TestFetchImage_rejectsMalformedURL(t *testing.T) {
	_, err := FetchImage(context.Background(), "http://%zz", t.TempDir(), ".channels/UCx/avatar")
	if err == nil {
		t.Fatal("expected an error for a malformed URL")
	}
}

// TestFetchImage_rejectsUnreachableHost asserts a connection failure (refused,
// unresolvable, etc.) surfaces as an error rather than a panic.
func TestFetchImage_rejectsUnreachableHost(t *testing.T) {
	// Port 1 is a reserved, well-known port nothing listens on; the
	// connection is refused immediately instead of timing out.
	_, err := FetchImage(context.Background(), "http://127.0.0.1:1/x.jpg", t.TempDir(), ".channels/UCx/avatar")
	if err == nil {
		t.Fatal("expected an error for an unreachable host")
	}
}

// TestFetchImage_rejectsNon200Status asserts a non-2xx response (a private
// video's now-410 avatar, a moved banner, etc.) is rejected rather than the
// error body being written to disk as if it were image data.
func TestFetchImage_rejectsNon200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := FetchImage(context.Background(), srv.URL, t.TempDir(), ".channels/UCx/avatar")
	if err == nil {
		t.Fatal("expected an error for a non-200 status")
	}
	if !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("err = %v, want mention of status 404", err)
	}
}

// TestFetchImage_rejectsTruncatedBody asserts a connection that closes before
// delivering the Content-Length it promised is reported as an error rather
// than a shorter-than-expected file being written silently.
func TestFetchImage_rejectsTruncatedBody(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 1024)
		_, _ = conn.Read(buf)
		// Claim a Content-Length far larger than what is actually sent, then
		// close the connection: io.ReadAll must observe an unexpected EOF.
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: image/jpeg\r\nContent-Length: 1000\r\n\r\n1234567890"))
	}()

	url := "http://" + ln.Addr().String() + "/x.jpg"
	_, err = FetchImage(context.Background(), url, t.TempDir(), ".channels/UCx/avatar")
	if err == nil {
		t.Fatal("expected an error for a truncated body")
	}
}

// TestFetchImage_mkdirAllFailure asserts a destination directory that cannot
// be created (because a path component is already a regular file) is
// reported as an error instead of panicking.
func TestFetchImage_mkdirAllFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff fake jpeg bytes"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	// relBase is ".channels/UCx/avatar", so its parent directory is
	// dir/.channels/UCx. Blocking ".channels" as a regular file makes the
	// MkdirAll for that parent fail.
	if err := os.WriteFile(filepath.Join(dir, ".channels"), []byte("not a dir"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := FetchImage(context.Background(), srv.URL, dir, ".channels/UCx/avatar")
	if err == nil {
		t.Fatal("expected an error when the destination directory cannot be created")
	}
}

// TestFetchImage_writeFileFailure asserts a temp-file write that fails
// (because the temp path is already occupied by a directory) is reported as
// an error instead of the rename step papering over it.
func TestFetchImage_writeFileFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff fake jpeg bytes"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	// The function writes to dest+".tmp" before renaming to dest. Occupying
	// that exact path with a directory makes os.WriteFile fail to open it.
	if err := os.MkdirAll(filepath.Join(dir, "avatar.jpg.tmp"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := FetchImage(context.Background(), srv.URL, dir, "avatar")
	if err == nil {
		t.Fatal("expected an error when the temp file cannot be written")
	}
}

// TestFetchImage_renameFailure asserts a failed final rename (destination
// occupied by a non-empty directory) is reported as an error, and the temp
// file it cleans up after itself does not linger.
func TestFetchImage_renameFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff fake jpeg bytes"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	// dest is dir/avatar.jpg; occupy it with a non-empty directory so the
	// final os.Rename cannot replace it.
	destDir := filepath.Join(dir, "avatar.jpg")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(destDir, "inner"), []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := FetchImage(context.Background(), srv.URL, dir, "avatar")
	if err == nil {
		t.Fatal("expected an error when the final rename fails")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "avatar.jpg.tmp")); !os.IsNotExist(statErr) {
		t.Fatalf("temp file %q should have been removed after a failed rename", "avatar.jpg.tmp")
	}
}
