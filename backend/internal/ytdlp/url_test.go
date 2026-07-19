package ytdlp

import "testing"

func TestCanonicalize_table(t *testing.T) {
	cases := map[string][2]string{ // input -> {watchURL, id}
		"https://youtu.be/dQw4w9WgXcQ":                           {"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PL123": {"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		"https://www.youtube.com/shorts/abc12345678":             {"https://www.youtube.com/watch?v=abc12345678", "abc12345678"},
	}

	for in, want := range cases {
		gotURL, gotID, _, err := Canonicalize(in)
		if err != nil {
			t.Fatalf("Canonicalize(%q): unexpected error: %v", in, err)
		}
		if gotURL != want[0] {
			t.Fatalf("Canonicalize(%q).watchURL = %q, want %q", in, gotURL, want[0])
		}
		if gotID != want[1] {
			t.Fatalf("Canonicalize(%q).id = %q, want %q", in, gotID, want[1])
		}
	}
}

func TestCanonicalize_kinds(t *testing.T) {
	cases := map[string]string{
		"https://youtu.be/dQw4w9WgXcQ":                     "video",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":      "video",
		"https://www.youtube.com/shorts/abc12345678":       "shorts",
		"https://www.youtube.com/live/abc12345678":         "live",
		"https://www.youtube.com/playlist?list=PLabc12345": "playlist",
	}
	for in, want := range cases {
		_, _, kind, err := Canonicalize(in)
		if err != nil {
			t.Fatalf("Canonicalize(%q): unexpected error: %v", in, err)
		}
		if kind != want {
			t.Fatalf("Canonicalize(%q).kind = %q, want %q", in, kind, want)
		}
	}
}

func TestCanonicalize_unknownHost(t *testing.T) {
	_, _, kind, err := Canonicalize("https://example.com/watch?v=dQw4w9WgXcQ")
	if err == nil {
		t.Fatal("expected error for non-YouTube host")
	}
	if kind != "unknown" {
		t.Fatalf("kind = %q, want %q", kind, "unknown")
	}
}

// TestCanonicalize_rejectsBareID locks the input-safety invariant: a raw,
// unparsed id-like string (which could start with '-' and be misread as a
// yt-dlp flag) must never canonicalize into a usable watch URL. Only a
// full URL is accepted.
func TestCanonicalize_rejectsBareID(t *testing.T) {
	for _, raw := range []string{"-rawLeadingDash", "dQw4w9WgXcQ"} {
		watchURL, _, kind, err := Canonicalize(raw)
		if err == nil {
			t.Fatalf("Canonicalize(%q): expected error for bare id, got watchURL=%q kind=%q", raw, watchURL, kind)
		}
		if watchURL != "" {
			t.Fatalf("Canonicalize(%q): expected empty watchURL on error, got %q", raw, watchURL)
		}
	}
}

func TestCanonicalize_emptyInput(t *testing.T) {
	if _, _, _, err := Canonicalize(""); err == nil {
		t.Fatal("expected error for empty input")
	}
}

// TestCanonicalize_channelKinds asserts channel URLs in their various
// YouTube shapes (UCID, @handle, legacy /c/, legacy /user/) canonicalize
// to kind "channel" and return early, before the 11-char video-id check.
func TestCanonicalize_channelKinds(t *testing.T) {
	cases := map[string]struct{ url, id string }{
		"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv": {"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv", "UCabcdefghijklmnopqrstuv"},
		"https://www.youtube.com/@SomeHandle":                      {"https://www.youtube.com/@SomeHandle", "@SomeHandle"},
		"https://www.youtube.com/c/SomeName":                       {"https://www.youtube.com/c/SomeName", "SomeName"},
		"https://www.youtube.com/user/LegacyName":                  {"https://www.youtube.com/user/LegacyName", "LegacyName"},
	}
	for in, want := range cases {
		gotURL, gotID, kind, err := Canonicalize(in)
		if err != nil {
			t.Fatalf("%s: unexpected err %v", in, err)
		}
		if kind != "channel" {
			t.Fatalf("%s: kind = %q, want channel", in, kind)
		}
		if gotURL != want.url || gotID != want.id {
			t.Fatalf("%s: got (%q,%q), want (%q,%q)", in, gotURL, gotID, want.url, want.id)
		}
	}
}
