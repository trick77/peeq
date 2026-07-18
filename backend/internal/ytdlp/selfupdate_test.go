package ytdlp

import (
	"context"
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
