package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeBinPrinting writes a tiny throwaway shell script that prints stdout
// (a canned JSON payload) and exits 0. Modeled on fakeBinTouching /
// testdata/fake-ytdlp.sh, but for tests that need to assert on parsed
// output rather than just "was the binary invoked".
func fakeBinPrinting(t *testing.T, stdout string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-ytdlp-print.sh")
	content := "#!/bin/sh\ncat <<'BACKEND_EOF'\n" + stdout + "\nBACKEND_EOF\nexit 0\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}
	return script
}

func TestChannelVideos_noCookie_doesNotCallBinary(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: func() (string, string) { return "", "absent" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	if _, err := r.ChannelVideos(context.Background(), "UCabcdefghijklmnopqrstuv", 50); !errors.Is(err, ErrNoCookie) {
		t.Fatalf("want ErrNoCookie, got %v", err)
	}
	if _, e := os.Stat(called); e == nil {
		t.Fatal("binary must not run without a cookie")
	}
}

func TestResolveChannel_noCookie_doesNotCallBinary(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: func() (string, string) { return "", "absent" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	if _, err := r.ResolveChannel(context.Background(), "https://www.youtube.com/@x"); !errors.Is(err, ErrNoCookie) {
		t.Fatalf("want ErrNoCookie, got %v", err)
	}
	if _, e := os.Stat(called); e == nil {
		t.Fatal("binary must not run without a cookie")
	}
}

func TestChannelVideos_parsesEntries(t *testing.T) {
	const listing = `{"id":"UCabc","channel":"Chan","entries":[
	  {"id":"vid00000001","title":"A","duration":600,"live_status":"not_live","thumbnails":[{"url":"https://t/a.jpg"}]},
	  {"id":"vid00000002","title":"B","duration":120,"live_status":"is_upcoming"}
	]}`
	r := New(RunnerConfig{
		Bin:            fakeBinPrinting(t, listing), // helper: stub that prints its canned stdout, exit 0
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	got, err := r.ChannelVideos(context.Background(), "UCabc", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "vid00000001" || got[0].DurationSeconds != 600 || got[0].ThumbnailURL != "https://t/a.jpg" {
		t.Fatalf("entry 0 = %+v", got[0])
	}
	if got[1].LiveStatus != "is_upcoming" {
		t.Fatalf("entry 1 live_status = %q", got[1].LiveStatus)
	}
	if got[0].URL != "https://www.youtube.com/watch?v=vid00000001" {
		t.Fatalf("entry 0 url = %q", got[0].URL)
	}
}

func TestResolveChannel_parsesUcidAndName(t *testing.T) {
	const j = `{"id":"UCxyz","channel_id":"UCxyz","channel":"My Channel","entries":[]}`
	r := New(RunnerConfig{
		Bin:            fakeBinPrinting(t, j),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	info, err := r.ResolveChannel(context.Background(), "https://www.youtube.com/@x")
	if err != nil {
		t.Fatal(err)
	}
	if info.UCID != "UCxyz" || info.Name != "My Channel" {
		t.Fatalf("got (%q,%q), want (UCxyz, My Channel)", info.UCID, info.Name)
	}
}

// TestParseChannelInfo_picksAvatarAndBanner asserts the avatar and banner are
// selected by yt-dlp's thumbnail id, not by array position — the array also
// contains cropped variants, and its order is not guaranteed.
func TestParseChannelInfo_picksAvatarAndBanner(t *testing.T) {
	raw := []byte(`{
      "channel_id": "UCxyz",
      "channel": "Uncanny Expeditions",
      "description": "Long-form field documentaries.",
      "thumbnails": [
        {"id": "avatar_uncropped", "url": "https://x/avatar.jpg"},
        {"id": "banner_uncropped", "url": "https://x/banner.jpg"},
        {"id": "0", "url": "https://x/other.jpg"}
      ]
    }`)

	info, err := parseChannelInfo(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.UCID != "UCxyz" {
		t.Fatalf("UCID = %q", info.UCID)
	}
	if info.Name != "Uncanny Expeditions" {
		t.Fatalf("Name = %q", info.Name)
	}
	if info.Description != "Long-form field documentaries." {
		t.Fatalf("Description = %q", info.Description)
	}
	if info.AvatarURL != "https://x/avatar.jpg" {
		t.Fatalf("AvatarURL = %q", info.AvatarURL)
	}
	if info.BannerURL != "https://x/banner.jpg" {
		t.Fatalf("BannerURL = %q", info.BannerURL)
	}
}

// TestParseChannelInfo_missingImages_isNotAnError asserts a channel with no
// banner still resolves. Many channels have no banner at all, and that must
// not fail the whole resolve.
func TestParseChannelInfo_missingImages_isNotAnError(t *testing.T) {
	info, err := parseChannelInfo([]byte(`{"channel_id":"UCx","channel":"X"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.AvatarURL != "" || info.BannerURL != "" {
		t.Fatalf("expected empty image urls, got %+v", info)
	}
}

// TestParseChannelInfo_noUCID_isAnError asserts a response we cannot pin to a
// channel id is rejected rather than cached under an empty key.
func TestParseChannelInfo_noUCID_isAnError(t *testing.T) {
	if _, err := parseChannelInfo([]byte(`{"channel":"X"}`)); err == nil {
		t.Fatal("expected an error when no channel id is present")
	}
}
