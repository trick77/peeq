package taimport

import "testing"

func TestPathMapper(t *testing.T) {
	m := PathMapper{
		TAMediaRoot:  "/ta/media",
		TACacheRoot:  "/ta/cache",
		PeeqMediaDir: "/data/media",
	}
	// A video id whose first character is upper-case, to prove the cache shard
	// is lowercased while the filename keeps the original id.
	const ch = "UC_channel"
	const vid = "Xabc123DEF4"

	cases := []struct {
		name string
		got  string
		want string
	}{
		{"src media", m.srcMedia(ch, vid), "/ta/media/UC_channel/Xabc123DEF4.mp4"},
		{"src subtitle", m.srcSubtitle(ch, vid, "en"), "/ta/media/UC_channel/Xabc123DEF4.en.vtt"},
		{"src thumbnail: cache volume, first char lowercased", m.srcThumbnail(vid), "/ta/cache/videos/x/Xabc123DEF4.jpg"},
		{"dst media: absolute, nested per-video dir", m.dstMedia(ch, vid), "/data/media/UC_channel/Xabc123DEF4/Xabc123DEF4.mp4"},
		{"dst thumbnail: absolute", m.dstThumbnail(ch, vid), "/data/media/UC_channel/Xabc123DEF4/Xabc123DEF4.jpg"},
		{"dst subtitle: absolute, for the copy", m.dstSubtitle(ch, vid, "en"), "/data/media/UC_channel/Xabc123DEF4/Xabc123DEF4.en.vtt"},
		{"stored subtitle: relative to MediaDir", m.storedSubtitleRel(ch, vid, "en"), "UC_channel/Xabc123DEF4/Xabc123DEF4.en.vtt"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// TestPathMapper_emptyVideoIDThumbnailDoesNotPanic guards the one method that
// indexes the id: a malformed row with an empty id must skip, not crash a
// migration mid-run.
func TestPathMapper_emptyVideoIDThumbnailDoesNotPanic(t *testing.T) {
	m := PathMapper{TACacheRoot: "/ta/cache"}
	if got := m.srcThumbnail(""); got != "" {
		t.Errorf("srcThumbnail(\"\") = %q, want empty", got)
	}
}
