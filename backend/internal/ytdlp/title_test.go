package ytdlp

import "testing"

// The separator rule only fires on a SPACED hyphen. An earlier shape of this
// idea replaced every "-", which turned "yt-dlp" into "yt—dlp" and "F-150"
// into "F—150" — the compounds are far more common in titles than the
// separator is.
func TestNormalizeTitle_emDashesOnlySpacedHyphens(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Building a Router - Part 3", "Building a Router — Part 3"},
		{"Testing the F-150 - Off Road", "Testing the F-150 — Off Road"},
		{"yt-dlp tips", "yt-dlp tips"},
		{"-Foo Bar-", "-Foo Bar-"},
		{"A - B - C", "A — B — C"},
		{"Router  -  Part 3", "Router — Part 3"},
		{"Router - Part 3", "Router — Part 3"},
	}
	for _, tc := range cases {
		if got := NormalizeTitle(tc.in); got != tc.want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Emoji are stripped by a cutoff at U+1F000, not by BMP block ranges. The
// ranges are the trap: 2600–27BF contains ★ U+2605 and 2190–21FF is arrows,
// so a range sweep would eat "Before → After" and every ★ rating in a title.
func TestNormalizeTitle_stripsEmojiButKeepsBMPSymbols(t *testing.T) {
	cases := []struct{ in, want string }{
		{"🔥 INSANE Build!! 🚀 - Part 2", "INSANE Build!! — Part 2"},
		{"A 👨‍👩‍👧‍👦 family", "A family"},
		{"Wave 👋🏽 hello", "Wave hello"},
		{"🇨🇭 Swiss trains", "Swiss trains"},
		// A keycap is ASCII "1" + VS16 + U+20E3; only the emoji halves go,
		// because the digit is real text — dropping it would eat "10 things".
		{"1️⃣ First", "1 First"},
		{"Sony® TV™ at 30° ★", "Sony® TV™ at 30° ★"},
		{"Before → After", "Before → After"},
		{"Checked ✓ and done", "Checked ✓ and done"},
	}
	for _, tc := range cases {
		if got := NormalizeTitle(tc.in); got != tc.want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Stripping a rune leaves a hole in the whitespace: "🔥 INSANE 🚀 Build" would
// come back as " INSANE  Build" without the collapse pass, and the doubled
// space would then also stop the separator rule from matching.
func TestNormalizeTitle_collapsesWhitespaceLeftBehind(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   spaced   out   ", "spaced out"},
		{"🔥", ""},
		{"🔥 🚀", ""},
		{"tabs\tand\nnewlines", "tabs and newlines"},
		{"　ideographic spaces here", "ideographic spaces here"},
	}
	for _, tc := range cases {
		if got := NormalizeTitle(tc.in); got != tc.want {
			t.Errorf("NormalizeTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Normalisation has to happen at the PARSE boundary, not at the write sites:
// this one entry feeds both the channel_videos ledger row and the videos row
// the scheduler upserts, so a title cleaned anywhere later would leave the two
// disagreeing.
func TestParseChannelEntries_normalizesTitles(t *testing.T) {
	out := []byte(`{"entries":[
		{"id":"abc123","title":"🔥 Building a Router - Part 3","duration":600},
		{"id":"","title":"skipped"}
	]}`)

	entries, err := parseChannelEntries(out)
	if err != nil {
		t.Fatalf("parseChannelEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (the id-less entry must be dropped)", len(entries))
	}
	if want := "Building a Router — Part 3"; entries[0].Title != want {
		t.Errorf("Title = %q, want %q", entries[0].Title, want)
	}
}

// The -J path is the title-fill for a manually added URL: the video row is
// created without a title and the download worker copies this one in.
func TestParseMeta_normalizesTitle(t *testing.T) {
	meta, err := parseMeta([]byte(`{"id":"abc123","title":"🚀 Launch Day - Recap"}`))
	if err != nil {
		t.Fatalf("parseMeta: %v", err)
	}
	if want := "Launch Day — Recap"; meta.Title != want {
		t.Errorf("Title = %q, want %q", meta.Title, want)
	}
}
