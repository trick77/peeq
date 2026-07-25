package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// fakeBinFailing writes a tiny throwaway shell script that exits non-zero,
// printing msg to stderr, simulating yt-dlp itself failing (network error,
// removed channel, etc.).
func fakeBinFailing(t *testing.T, msg string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-ytdlp-fail.sh")
	content := "#!/bin/sh\necho '" + msg + "' >&2\nexit 1\n"
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

func TestChannelStreams_noCookie_doesNotCallBinary(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: func() (string, string) { return "", "absent" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	if _, err := r.ChannelStreams(context.Background(), "UCabcdefghijklmnopqrstuv", 50); !errors.Is(err, ErrNoCookie) {
		t.Fatalf("want ErrNoCookie, got %v", err)
	}
	if _, e := os.Stat(called); e == nil {
		t.Fatal("binary must not run without a cookie")
	}
}

// TestChannelStreams_queriesStreamsTab is the whole difference between the two
// listing methods: same flags, different tab. A stream VOD is only reachable
// under /streams, so a url that still ended in /videos would list uploads twice
// and never surface a livestream.
func TestChannelStreams_queriesStreamsTab(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	script := filepath.Join(t.TempDir(), "fake-ytdlp-args.sh")
	content := "#!/bin/sh\necho \"$@\" > " + argsFile + "\necho '{\"entries\":[]}'\nexit 0\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}
	r := New(RunnerConfig{
		Bin:            script,
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	if _, err := r.ChannelStreams(context.Background(), "UCabc", 50); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.TrimSpace(string(raw))
	if !strings.HasSuffix(args, "/streams") {
		t.Fatalf("streams url is not the final arg: %s", args)
	}
	// The approximate-date flag matters just as much here — an undated stream
	// VOD reaches the inbox with no date to sort or render.
	if !strings.Contains(args, "youtubetab:approximate_date") {
		t.Fatalf("args missing approximate_date: %s", args)
	}
}

// TestChannelStreams_parsesLiveStatus proves the field the scan branches on
// survives the round trip: was_live (a finished VOD) must be distinguishable
// from is_live (still airing).
func TestChannelStreams_parsesLiveStatus(t *testing.T) {
	const listing = `{"id":"UCabc","channel":"Chan","entries":[
	  {"id":"vod00000001","title":"Finished stream","duration":7200,"live_status":"was_live"},
	  {"id":"vod00000002","title":"Airing now","live_status":"is_live"}
	]}`
	r := New(RunnerConfig{
		Bin:            fakeBinPrinting(t, listing),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	got, err := r.ChannelStreams(context.Background(), "UCabc", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].LiveStatus != "was_live" || got[0].DurationSeconds != 7200 {
		t.Fatalf("entry 0 = %+v", got[0])
	}
	if got[1].LiveStatus != "is_live" {
		t.Fatalf("entry 1 live_status = %q, want is_live", got[1].LiveStatus)
	}
	if got[0].URL != "https://www.youtube.com/watch?v=vod00000001" {
		t.Fatalf("entry 0 url = %q", got[0].URL)
	}
}

// TestChannelStreams_missingTabIsRecognisable: a channel that has never gone
// live has no /streams tab, and yt-dlp fails outright. The error must stay
// recognisable so the scheduler can keep that (very common) case quiet.
func TestChannelStreams_missingTabIsRecognisable(t *testing.T) {
	r := New(RunnerConfig{
		Bin:            fakeBinFailing(t, "ERROR: [youtube:tab] UCabc: This channel does not have a streams tab"),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	_, err := r.ChannelStreams(context.Background(), "UCabc", 50)
	if err == nil {
		t.Fatal("want an error for a missing streams tab")
	}
	if !IsMissingTab(err) {
		t.Fatalf("IsMissingTab(%v) = false, want true", err)
	}
}

// TestIsMissingTab_onlyMatchesTheTabError guards the demotion-to-debug: a real
// failure (a bot block, an extractor break) must never be mistaken for the
// boring "this channel never streamed" case and logged away quietly.
func TestIsMissingTab_onlyMatchesTheTabError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"missing tab", &ExecError{Err: errors.New("exit status 1"), Stderr: "ERROR: [youtube:tab] UCabc: This channel does not have a streams tab"}, true},
		{"other exec failure", &ExecError{Err: errors.New("exit status 1"), Stderr: "ERROR: unable to extract yt initial data"}, false},
		{"blocked sentinel", ErrBlocked, false},
		{"terminal", &TerminalError{Reason: "deleted"}, false},
		{"nil", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsMissingTab(tc.err); got != tc.want {
				t.Fatalf("IsMissingTab(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
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
        {"id": "0", "url": "https://x/other.jpg", "width": 900, "height": 900}
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

// TestParseChannelInfo_malformedJSON_isAnError asserts an unparseable
// response (yt-dlp emitting something that isn't the expected JSON shape) is
// reported rather than panicking or resolving to a zero-value channel.
func TestParseChannelInfo_malformedJSON_isAnError(t *testing.T) {
	if _, err := parseChannelInfo([]byte(`not json`)); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

// TestParseChannelInfo_nameFallsBackToUploader asserts that when yt-dlp's
// "channel" field is empty, the name falls back to "uploader" — some
// channel responses only populate uploader.
func TestParseChannelInfo_nameFallsBackToUploader(t *testing.T) {
	info, err := parseChannelInfo([]byte(`{"channel_id":"UCx","uploader":"Uploader Name"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Name != "Uploader Name" {
		t.Fatalf("Name = %q, want fallback to uploader", info.Name)
	}
}

// TestParseChannelInfo_nameFallsBackToTitle asserts that when both "channel"
// and "uploader" are empty, the name falls back to "title" as the last
// resort before leaving the channel unnamed.
func TestParseChannelInfo_nameFallsBackToTitle(t *testing.T) {
	info, err := parseChannelInfo([]byte(`{"channel_id":"UCx","title":"Title Only"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Name != "Title Only" {
		t.Fatalf("Name = %q, want fallback to title", info.Name)
	}
}

// TestResolveChannel_execFailure_isReported asserts a yt-dlp process failure
// (network error, removed channel, etc.) surfaces as an error rather than a
// zero-value ChannelInfo being treated as a resolved channel.
func TestResolveChannel_execFailure_isReported(t *testing.T) {
	r := New(RunnerConfig{
		Bin:            fakeBinFailing(t, "yt-dlp: unable to resolve channel"),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	if _, err := r.ResolveChannel(context.Background(), "https://www.youtube.com/@x"); err == nil {
		t.Fatal("expected an error when yt-dlp itself fails")
	}
}

// TestResolveChannel_unresolvableChannel_wrapsErrorWithURL asserts a response
// that parses but carries no channel id is rejected, and the wrapped error
// names the URL that failed — otherwise a multi-channel batch operation
// can't tell the user which one broke.
func TestResolveChannel_unresolvableChannel_wrapsErrorWithURL(t *testing.T) {
	r := New(RunnerConfig{
		Bin:            fakeBinPrinting(t, `{"channel":"No Id Here"}`),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	_, err := r.ResolveChannel(context.Background(), "https://www.youtube.com/@mystery")
	if err == nil {
		t.Fatal("expected an error for a channel with no resolvable id")
	}
	if !strings.Contains(err.Error(), "https://www.youtube.com/@mystery") {
		t.Fatalf("err = %v, want it to name the failing URL", err)
	}
}

// TestParseChannelInfo_readsPublishedFacts asserts the subscriber count,
// verified flag and @handle are read from the SAME metadata-only response
// peeq already fetches — the whole point of showing them is that they cost no
// extra call. The field names are yt-dlp's, verified against a live response.
func TestParseChannelInfo_readsPublishedFacts(t *testing.T) {
	raw := []byte(`{
      "channel_id": "UCxyz",
      "channel": "Uncanny Expeditions",
      "uploader_id": "@uncanny",
      "channel_follower_count": 7240000,
      "channel_is_verified": true
    }`)

	info, err := parseChannelInfo(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Subscribers != 7240000 {
		t.Fatalf("Subscribers = %d, want 7240000", info.Subscribers)
	}
	if !info.Verified {
		t.Fatal("Verified = false, want true")
	}
	if info.Handle != "@uncanny" {
		t.Fatalf("Handle = %q, want @uncanny", info.Handle)
	}
}

// TestParseChannelInfo_absentFactsAreZero asserts a channel that hides its
// subscriber count parses cleanly with a zero — which callers read as
// "unknown". A hidden count must not fail the resolve.
func TestParseChannelInfo_absentFactsAreZero(t *testing.T) {
	info, err := parseChannelInfo([]byte(`{"channel_id":"UCx","channel":"X"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.Subscribers != 0 || info.Verified {
		t.Fatalf("expected unknown/false, got %+v", info)
	}
}

// TestChannelHandle_rejectsLegacyUploaderIDs asserts only a real @handle is
// accepted. Older channels report a bare name or the UCID as uploader_id, and
// storing either as a handle would render a link to a page that is not there.
func TestChannelHandle_rejectsLegacyUploaderIDs(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"@uncanny", "@uncanny"},
		{"UCiDJtJKMICpb9B1qf7qjEOA", ""},
		{"SomeLegacyName", ""},
		{"@", ""},
		{"", ""},
	} {
		if got := channelHandle(tc.in); got != tc.want {
			t.Errorf("channelHandle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestParseChannelInfo_prefersTheBannerCrop asserts the stored banner is
// YouTube's own wide crop, not the 16:9 original. The original is the asset a
// creator uploads and mostly gets cropped away; rendering it in peeq's short,
// wide header would cover-crop to the middle of the artwork and zoom into
// whatever sits there. The numbered variants are the strip YouTube actually
// shows, and the widest of them is the one to keep.
func TestParseChannelInfo_prefersTheBannerCrop(t *testing.T) {
	raw := []byte(`{
      "channel_id": "UCxyz",
      "thumbnails": [
        {"id": "0", "url": "https://x/b-small.jpg", "width": 1060, "height": 175},
        {"id": "5", "url": "https://x/b-wide.jpg", "width": 2560, "height": 424},
        {"id": "banner_uncropped", "url": "https://x/b-uncropped.jpg"},
        {"id": "7", "url": "https://x/av.jpg", "width": 900, "height": 900},
        {"id": "avatar_uncropped", "url": "https://x/avatar.jpg"}
      ]
    }`)

	info, err := parseChannelInfo(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.BannerURL != "https://x/b-wide.jpg" {
		t.Fatalf("BannerURL = %q, want the widest 6:1 crop", info.BannerURL)
	}
	// The square avatar crop must never be mistaken for a banner.
	if info.AvatarURL != "https://x/avatar.jpg" {
		t.Fatalf("AvatarURL = %q", info.AvatarURL)
	}
}

// TestPickBanner_fallsBackToUncropped asserts a channel whose thumbnails
// carry no usable dimensions still gets a banner rather than none — the crop
// is a better framing, not a requirement.
func TestPickBanner_fallsBackToUncropped(t *testing.T) {
	got := pickBanner([]channelThumb{
		{ID: "0", URL: "https://x/no-dims.jpg"},
		{ID: "7", URL: "https://x/av.jpg", Width: 900, Height: 900},
	}, "https://x/uncropped.jpg")
	if got != "https://x/uncropped.jpg" {
		t.Fatalf("pickBanner = %q, want the uncropped fallback", got)
	}
	if got := pickBanner(nil, ""); got != "" {
		t.Fatalf("pickBanner with nothing = %q, want empty", got)
	}
}

func TestChannelVideos_timestampBecomesPublishedDate(t *testing.T) {
	// 1784757600 is 2026-07-22T22:00:00Z — late enough in the UTC day that a
	// host on any of Europe's summer offsets would roll it to the 23rd, which
	// is what pins the conversion to UTC instead of local time.
	const listing = `{"id":"UCabc","entries":[
	  {"id":"vid00000001","title":"A","timestamp":1784757600},
	  {"id":"vid00000002","title":"B"},
	  {"id":"vid00000003","title":"C","timestamp":0}
	]}`
	r := New(RunnerConfig{
		Bin:            fakeBinPrinting(t, listing),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	got, err := r.ChannelVideos(context.Background(), "UCabc", 50)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].PublishedAt != "2026-07-22" {
		t.Fatalf("entry 0 published = %q, want 2026-07-22", got[0].PublishedAt)
	}
	// A missing or zero timestamp must stay empty, never become the epoch:
	// "1970-01-01" would render as a 56-year-old upload on the inbox card.
	if got[1].PublishedAt != "" || got[2].PublishedAt != "" {
		t.Fatalf("dateless entries = %q, %q; want both empty",
			got[1].PublishedAt, got[2].PublishedAt)
	}
}

func TestChannelVideos_requestsApproximateDates(t *testing.T) {
	// Without this extractor-arg yt-dlp reports no date at all for a flat
	// listing, so the whole feature hinges on the flag reaching the binary.
	argsFile := filepath.Join(t.TempDir(), "args")
	script := filepath.Join(t.TempDir(), "fake-ytdlp-args.sh")
	content := "#!/bin/sh\necho \"$@\" > " + argsFile + "\necho '{\"entries\":[]}'\nexit 0\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}
	r := New(RunnerConfig{
		Bin:            script,
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	if _, err := r.ChannelVideos(context.Background(), "UCabc", 50); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := string(raw)
	if !strings.Contains(args, "youtubetab:approximate_date") {
		t.Fatalf("args missing approximate_date: %s", args)
	}
	// The url must still come last — appending the flag after it would make
	// yt-dlp read the url as the flag's value.
	if !strings.HasSuffix(strings.TrimSpace(args), "/videos") {
		t.Fatalf("url is not the final arg: %s", args)
	}
}

// TestChannelStreams_dateFallsBackToReleaseTimestamp guards the field the
// scan-backlog gate depends on. approximate_date derives `timestamp` from the
// relative-time text on an upload card; a stream card says "streamed 2 years
// ago" instead, and yt-dlp reports that as release_timestamp. Without the
// fallback a stream VOD arrives undated, every date-based decision downstream
// fails open, and a whole back catalogue reads as brand new.
func TestChannelStreams_dateFallsBackToReleaseTimestamp(t *testing.T) {
	// 1583280000 = 2020-03-04, 1751328000 = 2025-07-01 (both UTC).
	const listing = `{"id":"UCabc","channel":"Chan","entries":[
	  {"id":"vod00000001","title":"Only release","live_status":"was_live","release_timestamp":1583280000},
	  {"id":"vod00000002","title":"Both, timestamp wins","live_status":"was_live","timestamp":1751328000,"release_timestamp":1583280000},
	  {"id":"vod00000003","title":"Neither","live_status":"was_live"}
	]}`
	r := New(RunnerConfig{
		Bin:            fakeBinPrinting(t, listing),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	got, err := r.ChannelStreams(context.Background(), "UCabc", 50)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].PublishedAt != "2020-03-04" {
		t.Fatalf("release-only date = %q, want 2020-03-04", got[0].PublishedAt)
	}
	// timestamp is the better answer whenever present: release_timestamp is when
	// the broadcast STARTED, which for a multi-day premiere is not publication.
	if got[1].PublishedAt != "2025-07-01" {
		t.Fatalf("both-present date = %q, want timestamp to win", got[1].PublishedAt)
	}
	// Undated must stay "" rather than becoming the epoch — the scan gate reads
	// "" as "cannot judge" and fails open, which is the safe direction.
	if got[2].PublishedAt != "" {
		t.Fatalf("undated date = %q, want empty", got[2].PublishedAt)
	}
}
