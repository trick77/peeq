package mcpserver

import "testing"

func TestVideoIDArg(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"bare id", "dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"bare id with spaces", "  dQw4w9WgXcQ\n", "dQw4w9WgXcQ"},
		{"watch url", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"watch url with params", " https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42s&list=PL123 ", "dQw4w9WgXcQ"},
		{"mobile watch url", "https://m.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"youtu.be", "https://youtu.be/dQw4w9WgXcQ?t=10", "dQw4w9WgXcQ"},
		{"shorts", "https://youtube.com/shorts/dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"live", "https://www.youtube.com/live/dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		// Not a video: left as given, so the lookup misses instead of
		// turning a channel or playlist id into a video id.
		{"channel url", "https://www.youtube.com/channel/UCabcdefghij", "https://www.youtube.com/channel/UCabcdefghij"},
		{"handle url", "https://www.youtube.com/@veritasium", "https://www.youtube.com/@veritasium"},
		{"playlist url", "https://www.youtube.com/playlist?list=PL123", "https://www.youtube.com/playlist?list=PL123"},
		{"other host", "https://example.com/watch?v=dQw4w9WgXcQ", "https://example.com/watch?v=dQw4w9WgXcQ"},
		{"garbage", " not a video ", "not a video"},
		{"empty", "  ", ""},
	} {
		if got := videoIDArg(tc.in); got != tc.want {
			t.Errorf("%s: videoIDArg(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestLogVideoID(t *testing.T) {
	if got := logVideoID("dQw4w9WgXcQ"); got != "dQw4w9WgXcQ" {
		t.Errorf("id-shaped = %q", got)
	}
	for _, in := range []string{"https://www.youtube.com/@veritasium", "short", ""} {
		if got := logVideoID(in); got != "invalid" {
			t.Errorf("logVideoID(%q) = %q, want invalid", in, got)
		}
	}
}
