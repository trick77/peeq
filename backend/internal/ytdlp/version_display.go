package ytdlp

import (
	"strings"
	"time"
)

// DisplayVersion renders a yt-dlp version, a dotted date ("2026.10.26"), as
// peeq shows dates: "26.10.2026". A same-day build suffix stays, after a dash
// ("2026.10.26.1" → "26.10.2026-1"). Anything that is not a valid dotted date
// comes back unchanged.
//
// Display only. The raw string is what is compared, stored and sent to
// GitHub; this is for text a person reads (Activity rows).
func DisplayVersion(v string) string {
	parts := strings.SplitN(v, ".", 4)
	if len(parts) < 3 || len(parts[0]) != 4 || len(parts[1]) != 2 || len(parts[2]) != 2 ||
		(len(parts) == 4 && parts[3] == "") {
		return v
	}
	if _, err := time.Parse("2006.01.02", strings.Join(parts[:3], ".")); err != nil {
		return v
	}
	out := parts[2] + "." + parts[1] + "." + parts[0]
	if len(parts) == 4 {
		out += "-" + parts[3]
	}
	return out
}
