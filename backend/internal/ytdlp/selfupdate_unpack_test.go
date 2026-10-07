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
	"time"
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

// fakeBuild is a stand-in yt-dlp: it reports version, and lists Chrome as an
// impersonation target unless chrome is false.
func fakeBuild(version string, chrome bool) string {
	target := "Chrome-133      Macos-15     curl_cffi"
	if !chrome {
		target = "Chrome          -            curl_cffi (unavailable)"
	}
	return "#!/bin/sh\ncase \"$1\" in\n--version) echo " + version + " ;;\n--list-impersonate-targets) echo '" + target + "' ;;\nesac\n"
}

func goodRelease(t *testing.T, v string) []byte {
	return releaseZip(t,
		zipEntry{name: "yt-dlp_linux", body: fakeBuild(v, true), mode: 0o755},
		zipEntry{name: "_internal/lib.so", body: "lib-" + v, mode: 0o755},
	)
}

func install(t *testing.T, dest, v string) {
	t.Helper()
	if _, err := downloadUnpackedFrom(context.Background(), serveBytes(t, http.StatusOK, goodRelease(t, v)), dest, "yt-dlp_linux"); err != nil {
		t.Fatalf("install %s: %v", v, err)
	}
}

func installedLib(t *testing.T, dest string) string {
	t.Helper()
	got, _ := os.ReadFile(filepath.Join(dest, "_internal", "lib.so"))
	return string(got)
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
	if got := installedLib(t, dest); got != "lib-2099.01.01" {
		t.Fatalf("_internal/lib.so = %q", got)
	}
	if info, err := os.Lstat(dest); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("install is not a symlink: %v %v", info, err)
	}
	if l := leftovers(t, dir, "yt-dlp_linux"); len(l) != 1 || !strings.HasPrefix(l[0], treePrefix) {
		t.Fatalf("want exactly the one tree the link points at, got %v", l)
	}
}

// TestDownloadUnpackedFrom_runningTreeSurvivesUpdates: a yt-dlp runs from the
// real path of its tree and loads modules from it as it goes, so no update may
// change or remove the tree a run started in, however many follow. Age
// expires a tree, counted from when it was replaced, never from when it was
// unpacked, and never the current or previous one.
func TestDownloadUnpackedFrom_runningTreeSurvivesUpdates(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "yt-dlp_linux")

	install(t, dest, "2099.01.01")
	running, err := filepath.EvalSymlinks(dest) // where a run started now lives
	if err != nil {
		t.Fatal(err)
	}
	// Installed long ago: only the replacement may start its clock.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(running, old, old); err != nil {
		t.Fatal(err)
	}
	install(t, dest, "2099.02.02")
	install(t, dest, "2099.03.03")

	if got, _ := os.ReadFile(filepath.Join(running, "_internal", "lib.so")); string(got) != "lib-2099.01.01" {
		t.Fatalf("the running tree changed or went away under its run: %q", got)
	}

	prev := treeKeep
	treeKeep = 0
	t.Cleanup(func() { treeKeep = prev })
	install(t, dest, "2099.04.04")

	if got := installedLib(t, dest); got != "lib-2099.04.04" {
		t.Fatalf("installed release = %q, want the last one", got)
	}
	if l := leftovers(t, dir, "yt-dlp_linux"); len(l) != 2 {
		t.Fatalf("expired trees not removed: want the current and the previous tree, got %v", l)
	}
}

// TestDownloadUnpackedFrom_sameVersionIsNotSwapped: pressing Update on the
// version already installed must not stack another ~100MB tree.
func TestDownloadUnpackedFrom_sameVersionIsNotSwapped(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "yt-dlp_linux")
	install(t, dest, "2099.01.01")
	before, err := filepath.EvalSymlinks(dest)
	if err != nil {
		t.Fatal(err)
	}
	install(t, dest, "2099.01.01")
	after, err := filepath.EvalSymlinks(dest)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("same version swapped in: %s -> %s", before, after)
	}
	if l := leftovers(t, dir, "yt-dlp_linux"); len(l) != 1 {
		t.Fatalf("want only the installed tree, got %v", l)
	}
}

// TestDownloadUnpackedFrom_removesCrashLeftovers: a process that died
// mid-update leaves its temp archive and link; the next update removes them.
func TestDownloadUnpackedFrom_removesCrashLeftovers(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "yt-dlp_linux")
	for _, name := range []string{downloadPrefix + "123", linkPrefix + "456"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	install(t, dest, "2099.01.01")
	if l := leftovers(t, dir, "yt-dlp_linux"); len(l) != 1 || !strings.HasPrefix(l[0], treePrefix) {
		t.Fatalf("crash leftovers not removed: %v", l)
	}
}

// TestDownloadUnpackedFrom_refusesNonLink: nothing installs anything but the
// link at the install path, and moving something else aside would hand it to
// the stale-tree sweep.
func TestDownloadUnpackedFrom_refusesNonLink(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "yt-dlp_linux")
	if err := os.MkdirAll(dest, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := downloadUnpackedFrom(context.Background(), serveBytes(t, http.StatusOK, goodRelease(t, "2099.01.01")), dest, "yt-dlp_linux"); err == nil {
		t.Fatal("expected an error for a plain directory at the install path")
	}
	if info, err := os.Lstat(dest); err != nil || !info.IsDir() {
		t.Fatalf("plain directory changed: %v %v", info, err)
	}
	if l := leftovers(t, dir, "yt-dlp_linux"); len(l) != 0 {
		t.Fatalf("leftovers: %v", l)
	}
}

// TestDownloadUnpackedFrom_failuresLeaveInstallIntact: a failed download, an
// archive that escapes its directory, a build that does not run and a build
// that cannot impersonate Chrome must each leave the working install
// untouched and nothing behind.
func TestDownloadUnpackedFrom_failuresLeaveInstallIntact(t *testing.T) {
	cases := map[string]struct {
		status int
		body   func(t *testing.T) []byte
	}{
		"server error": {http.StatusInternalServerError, func(*testing.T) []byte { return []byte("nope") }},
		"not a zip":    {http.StatusOK, func(*testing.T) []byte { return []byte("plain text") }},
		"path escape": {http.StatusOK, func(t *testing.T) []byte {
			return releaseZip(t,
				zipEntry{name: "yt-dlp_linux", body: fakeBuild("2099.02.02", true), mode: 0o755},
				zipEntry{name: "../evil", body: "x", mode: 0o644},
			)
		}},
		"no executable": {http.StatusOK, func(t *testing.T) []byte {
			return releaseZip(t, zipEntry{name: "_internal/lib.so", body: "x", mode: 0o644})
		}},
		"build does not run": {http.StatusOK, func(t *testing.T) []byte {
			return releaseZip(t, zipEntry{name: "yt-dlp_linux", body: "#!/bin/sh\nexit 1\n", mode: 0o755})
		}},
		"no chrome impersonation": {http.StatusOK, func(t *testing.T) []byte {
			return releaseZip(t, zipEntry{name: "yt-dlp_linux", body: fakeBuild("2099.02.02", false), mode: 0o755})
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "bin")
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(dir, "yt-dlp_linux")
			install(t, dest, "2099.01.01")
			before := leftovers(t, dir, "yt-dlp_linux")

			if _, err := downloadUnpackedFrom(context.Background(), serveBytes(t, c.status, c.body(t)), dest, "yt-dlp_linux"); err == nil {
				t.Fatal("expected an error")
			}
			if v, err := Version(context.Background(), filepath.Join(dest, "yt-dlp_linux")); err != nil || v != "2099.01.01" {
				t.Fatalf("existing install changed: %q %v", v, err)
			}
			if after := leftovers(t, dir, "yt-dlp_linux"); len(after) != len(before) {
				t.Fatalf("leftovers: before %v, after %v", before, after)
			}
			if _, err := os.Stat(filepath.Join(parent, "evil")); err == nil {
				t.Fatal("an archive entry escaped the install directory")
			}
		})
	}
}
