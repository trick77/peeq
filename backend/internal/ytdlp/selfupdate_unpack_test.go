package ytdlp

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// zipEntry is one file in a test release archive.
type zipEntry struct {
	name string
	body string
	mode os.FileMode
}

// releaseZip builds an archive shaped like yt-dlp_linux.zip: an executable at
// the top level beside an _internal/ tree.
func releaseZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		h.SetMode(e.mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func serveBytes(t *testing.T, status int, body []byte) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func versionScript(v string) string { return "#!/bin/sh\necho " + v + "\n" }

func goodRelease(t *testing.T, v string) []byte {
	return releaseZip(t,
		zipEntry{name: "yt-dlp_linux", body: versionScript(v), mode: 0o755},
		zipEntry{name: "_internal/lib.so", body: "lib-" + v, mode: 0o755},
	)
}

// leftovers lists the entries of dir other than the install itself.
func leftovers(t *testing.T, dir, install string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.Name() != install {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestDownloadUnpackedFrom_installsTree(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "yt-dlp_linux")

	v, err := downloadUnpackedFrom(context.Background(), serveBytes(t, http.StatusOK, goodRelease(t, "2099.01.01")), dest, "yt-dlp_linux")
	if err != nil {
		t.Fatalf("downloadUnpackedFrom: %v", err)
	}
	if v != "2099.01.01" {
		t.Fatalf("version = %q, want 2099.01.01", v)
	}
	info, err := os.Stat(filepath.Join(dest, "yt-dlp_linux"))
	if err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("executable missing or not executable: %v %v", info, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "_internal", "lib.so")); string(got) != "lib-2099.01.01" {
		t.Fatalf("_internal/lib.so = %q", got)
	}
	if l := leftovers(t, dir, "yt-dlp_linux"); len(l) != 0 {
		t.Fatalf("leftovers after a first install: %v", l)
	}
}

// TestDownloadUnpackedFrom_replacesAndKeepsOnePrevious: the replaced tree is
// kept until the next update, because a yt-dlp still running from it loads
// modules from _internal/ lazily; the update after that removes it.
func TestDownloadUnpackedFrom_replacesAndKeepsOnePrevious(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "yt-dlp_linux")
	ctx := context.Background()

	for _, v := range []string{"2099.01.01", "2099.02.02", "2099.03.03"} {
		if _, err := downloadUnpackedFrom(ctx, serveBytes(t, http.StatusOK, goodRelease(t, v)), dest, "yt-dlp_linux"); err != nil {
			t.Fatalf("install %s: %v", v, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "_internal", "lib.so")); string(got) != "lib-2099.03.03" {
		t.Fatalf("installed tree = %q, want the last release", got)
	}
	l := leftovers(t, dir, "yt-dlp_linux")
	if len(l) != 1 || !strings.HasPrefix(l[0], ".yt-dlp-old-") {
		t.Fatalf("want exactly one previous tree kept, got %v", l)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, l[0], "_internal", "lib.so")); string(got) != "lib-2099.02.02" {
		t.Fatalf("kept tree = %q, want the release just replaced", got)
	}
}

// TestDownloadUnpackedFrom_failuresLeaveInstallIntact: a failed download, an
// archive that escapes its directory, and a build that does not run must each
// leave the working install untouched and nothing behind.
func TestDownloadUnpackedFrom_failuresLeaveInstallIntact(t *testing.T) {
	cases := map[string]struct {
		status int
		body   func(t *testing.T) []byte
	}{
		"server error": {http.StatusInternalServerError, func(*testing.T) []byte { return []byte("nope") }},
		"not a zip":    {http.StatusOK, func(*testing.T) []byte { return []byte("plain text") }},
		"path escape": {http.StatusOK, func(t *testing.T) []byte {
			return releaseZip(t,
				zipEntry{name: "yt-dlp_linux", body: versionScript("2099.01.01"), mode: 0o755},
				zipEntry{name: "../evil", body: "x", mode: 0o644},
			)
		}},
		"no executable": {http.StatusOK, func(t *testing.T) []byte {
			return releaseZip(t, zipEntry{name: "_internal/lib.so", body: "x", mode: 0o644})
		}},
		"build does not run": {http.StatusOK, func(t *testing.T) []byte {
			return releaseZip(t, zipEntry{name: "yt-dlp_linux", body: "#!/bin/sh\nexit 1\n", mode: 0o755})
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "bin")
			dest := filepath.Join(dir, "yt-dlp_linux")
			if err := os.MkdirAll(filepath.Join(dest, "_internal"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dest, "yt-dlp_linux"), []byte(versionScript("2024.01.01")), 0o755); err != nil {
				t.Fatal(err)
			}

			if _, err := downloadUnpackedFrom(context.Background(), serveBytes(t, c.status, c.body(t)), dest, "yt-dlp_linux"); err == nil {
				t.Fatal("expected an error")
			}
			if v, err := Version(context.Background(), filepath.Join(dest, "yt-dlp_linux")); err != nil || v != "2024.01.01" {
				t.Fatalf("existing install changed: %q %v", v, err)
			}
			if l := leftovers(t, dir, "yt-dlp_linux"); len(l) != 0 {
				t.Fatalf("leftovers: %v", l)
			}
			if _, err := os.Stat(filepath.Join(parent, "evil")); err == nil {
				t.Fatal("an archive entry escaped the install directory")
			}
		})
	}
}
