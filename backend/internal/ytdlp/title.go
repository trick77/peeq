package ytdlp

import "strings"

// NormalizeTitle cleans a YouTube title for display: emoji are stripped, odd
// whitespace is collapsed, and a hyphen used as a separator becomes an em dash.
//
// This runs at the PARSE boundary (parseChannelEntries, parseMeta) rather than
// at the four places a title is written, so the ledger row, the videos row, the
// inbox card and the player all carry the same string. There is no backfill:
// titles already in the database keep whatever yt-dlp gave them.
func NormalizeTitle(raw string) string {
	return emDashSeparator(collapseSpace(stripEmoji(raw)))
}

// stripEmoji drops every rune at or above U+1F000, plus the three BMP code
// points that glue emoji sequences together.
//
// The blunt cutoff is the point. Every real emoji lives in the Supplementary
// Multilingual Plane above it — the pictographs, the U+1F3FB skin-tone
// modifiers, the U+1F1E6 regional indicators behind flags, and every member of
// a ZWJ family like "\U0001F468‍\U0001F469‍\U0001F467". The whole BMP
// is therefore left alone and "™ © ® ° ★ ▶ → ✓" survive by construction, with
// no exclusion list to maintain.
//
// Do NOT trade this for block ranges. 2600–27BF (misc symbols) contains ★
// U+2605, and 2190–21FF is arrows — "Before → After" is a real title shape.
// If a specific BMP glyph like ✅ or ➡ ever has to go, add it to a short
// explicit list here rather than reopening range sweeps.
func stripEmoji(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r >= 0x1F000:
			// Emoji proper, everything above the BMP.
		case r == 0x200D:
			// Zero-width joiner: without it the remnants of a stripped
			// family sequence would fuse with the next character.
		case r >= 0xFE00 && r <= 0xFE0F:
			// Variation selectors, VS16 in particular — the suffix that
			// turns a BMP character into its emoji presentation.
		case r == 0x20E3:
			// Combining keycap, the second half of "1️⃣".
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// collapseSpace maps the space-like runes YouTube titles pick up onto a plain
// space, squeezes runs down to one, and trims the ends.
//
// It runs AFTER stripEmoji on purpose: removing "🔥 INSANE 🚀 Build" leaves
// leading and doubled spaces that only this step can see.
func collapseSpace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		// U+00A0 no-break, U+202F narrow no-break, U+3000 ideographic.
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == 0x00A0 || r == 0x202F || r == 0x3000 {
			if !prevSpace {
				b.WriteByte(' ')
			}
			prevSpace = true
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}

// emDashSeparator rewrites a SPACED hyphen as an em dash: "Router - Part 3"
// becomes "Router — Part 3".
//
// Only the spaced form is touched, which is what keeps "yt-dlp", "F-150" and
// "Mac-mini" intact. It runs after collapseSpace, so "Router  -  Part 3" and
// the no-break-space variants have already been reduced to the one form
// matched here. A hyphen at either end of the title is not a separator and is
// left alone by the same rule.
func emDashSeparator(s string) string {
	return strings.ReplaceAll(s, " - ", " — ")
}
