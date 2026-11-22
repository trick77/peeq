package mcpserver

import (
	"strings"
	"testing"
)

func TestParseVideoArg(t *testing.T) {
	h := &handler{publicURL: "https://peeq.example.com"}
	const id = "dQw4w9WgXcQ"
	for _, tc := range []struct {
		name, in, id string
		start        int
	}{
		{"bare id", id, id, -1},
		{"bare id with spaces", "  " + id + "\n", id, -1},
		{"short bare id", "abc123", "abc123", -1},
		{"mistyped bare id", "dQw4w9WgXc", "dQw4w9WgXc", -1},
		{"watch url", "https://www.youtube.com/watch?v=" + id, id, -1},
		{"watch url with params", "https://www.youtube.com/watch?v=" + id + "&list=PL1&t=12m34s", id, 754},
		{"mobile", "https://m.youtube.com/watch?v=" + id, id, -1},
		{"music", "https://music.youtube.com/watch?v=" + id, id, -1},
		{"youtu.be with seconds", "https://youtu.be/" + id + "?t=754", id, 754},
		{"youtu.be with h m s", "https://youtu.be/" + id + "?t=1h2m3s", id, 3723},
		{"youtu.be fragment", "https://youtu.be/" + id + "#t=90", id, 90},
		{"youtu.be bad t", "https://youtu.be/" + id + "?t=soon", id, -1},
		{"no scheme youtu.be", "youtu.be/" + id, id, -1},
		{"no scheme watch", "www.youtube.com/watch?v=" + id + "&t=42s", id, 42},
		{"shorts", "https://youtube.com/shorts/" + id, id, -1},
		{"live", "https://www.youtube.com/live/" + id, id, -1},
		{"embed", "https://www.youtube.com/embed/" + id + "?start=30", id, 30},
		{"nocookie embed", "https://www.youtube-nocookie.com/embed/" + id, id, -1},
		{"v path", "https://www.youtube.com/v/" + id, id, -1},
		{"angle brackets", "<https://youtu.be/" + id + ">", id, -1},
		{"trailing punctuation", "https://youtube.com/shorts/" + id + ".", id, -1},
		{"parenthesised", "(https://www.youtube.com/live/" + id + "),", id, -1},
		{"quoted bare id", `"` + id + `"`, id, -1},
		{"peeq link", "https://peeq.example.com/video/bikeVideo01?t=754", "bikeVideo01", 754},
		{"peeq link no scheme", "peeq.example.com/video/bikeVideo01", "bikeVideo01", -1},
		{"peeq link other case", "https://PEEQ.example.com/video/abc123/", "abc123", -1},
		// Not a video reference: no id, so no lookup.
		{"other host video path", "https://other.example.com/video/abc123", "", -1},
		{"peeq non-video path", "https://peeq.example.com/search?q=x", "", -1},
		{"channel url", "https://www.youtube.com/channel/UCabcdefghij", "", -1},
		{"handle url", "https://www.youtube.com/@veritasium", "", -1},
		{"playlist url", "https://www.youtube.com/playlist?list=PL123", "", -1},
		{"other host", "https://example.com/watch?v=" + id, "", -1},
		{"embed bad id", "https://www.youtube.com/embed/x%20y", "", -1},
		{"unparseable", "https://youtu.be/%zz", "", -1},
		{"garbage", " not a video ", "", -1},
		{"too long", strings.Repeat("a", 40), "", -1},
		{"empty", "  ", "", -1},
		{"only brackets", "<>", "", -1},
	} {
		got := h.parseVideoArg(tc.in)
		if got.id != tc.id || got.start != tc.start {
			t.Errorf("%s: parseVideoArg(%q) = id %q start %d, want %q %d", tc.name, tc.in, got.id, got.start, tc.id, tc.start)
		}
		if got.raw != strings.TrimSpace(tc.in) {
			t.Errorf("%s: raw = %q", tc.name, got.raw)
		}
	}
}

// peeq served under a path prefix, or with no public URL at all.
func TestParseVideoArg_peeqLinkFollowsThePublicURL(t *testing.T) {
	h := &handler{publicURL: "https://example.com/peeq"}
	if got := h.parseVideoArg("https://example.com/peeq/video/abc123?t=5"); got.id != "abc123" || got.start != 5 {
		t.Errorf("prefixed: %+v", got)
	}
	if got := h.parseVideoArg("https://example.com/video/abc123"); got.id != "" {
		t.Errorf("outside the prefix: %+v", got)
	}
	none := &handler{}
	if got := none.parseVideoArg("https://peeq.example.com/video/abc123"); got.id != "" {
		t.Errorf("no public URL: %+v", got)
	}
	if got := none.parseVideoArg("https://youtu.be/dQw4w9WgXcQ"); got.id != "dQw4w9WgXcQ" {
		t.Errorf("no public URL, youtube link: %+v", got)
	}
}

func TestParseTimestamp(t *testing.T) {
	for in, want := range map[string]int{
		"754": 754, "754s": 754, "12m34s": 754, "1h": 3600, "1h2m3s": 3723, "0": 0,
		"": -1, "soon": -1, "12x": -1, "m": -1, "-5": -1, "12m34": -1,
	} {
		if got := parseTimestamp(in); got != want {
			t.Errorf("parseTimestamp(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestLogVideoID(t *testing.T) {
	for _, in := range []string{"dQw4w9WgXcQ", "dQw4w9WgXc", "abc123"} {
		if got := logVideoID(in); got != in {
			t.Errorf("logVideoID(%q) = %q, want it as is", in, got)
		}
	}
	for _, in := range []string{"https://www.youtube.com/@veritasium", "two words", "", strings.Repeat("a", 40)} {
		if got := logVideoID(in); got != "invalid" {
			t.Errorf("logVideoID(%q) = %q, want invalid", in, got)
		}
	}
}
