package ytdlp

import (
	"regexp"
	"time"
)

// versionDate is a yt-dlp version: a dotted date and an optional build
// suffix. ui/src/format.ts formatYtdlpVersion uses the same pattern.
var versionDate = regexp.MustCompile(`^(\d{4})\.(\d{2})\.(\d{2})(?:\.(.+))?$`)

// DisplayVersion renders a yt-dlp version, a dotted date ("2026.10.26"), as
// "26.10.2026". A same-day build suffix stays, after a dash
// ("2026.10.26.1" → "26.10.2026-1"). Anything that is not a valid dotted date
// comes back unchanged.
//
// Display only. The raw string is what is compared, stored and sent to
// GitHub; this is for text a person reads (Activity rows).
func DisplayVersion(v string) string {
	m := versionDate.FindStringSubmatch(v)
	if m == nil {
		return v
	}
	if _, err := time.Parse("2006.01.02", m[1]+"."+m[2]+"."+m[3]); err != nil {
		return v
	}
	out := m[3] + "." + m[2] + "." + m[1]
	if m[4] != "" {
		out += "-" + m[4]
	}
	return out
}
