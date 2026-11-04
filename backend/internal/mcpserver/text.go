package mcpserver

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/trick77/peeq/internal/rag"
)

// maxToolTextBytes caps one tool's text (Server.MaxTextBytes). The calling
// client truncates at 32 KB; staying well under it keeps the cut ours, at a
// line we chose. Tools that page (search, transcript) budget against it.
const maxToolTextBytes = 24 * 1024

// clock renders seconds as mm:ss, or h:mm:ss from an hour on.
func clock(sec int) string {
	if sec < 0 {
		sec = 0
	}
	h, m, s := sec/3600, sec%3600/60, sec%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

// boldHighlights turns the FTS snippet's STX/ETX markers into markdown bold.
func boldHighlights(s string) string {
	return strings.NewReplacer(rag.HighlightStart, "**", rag.HighlightEnd, "**").Replace(s)
}

// stripHighlights drops the FTS snippet's markers.
func stripHighlights(s string) string {
	return strings.NewReplacer(rag.HighlightStart, "", rag.HighlightEnd, "").Replace(s)
}

// videoURL is peeq's page for a video, opened at sec when sec > 0. The SPA
// reads ?t= on a cold load of /video/<id>.
func (h *handler) videoURL(id string, sec int) string {
	u := h.publicURL + "/video/" + url.PathEscape(id)
	if sec > 0 {
		u += fmt.Sprintf("?t=%d", sec)
	}
	return u
}

// youtubeURL is the video on YouTube, at sec when sec > 0.
func youtubeURL(id string, sec int) string {
	u := "https://youtu.be/" + url.PathEscape(id)
	if sec > 0 {
		u += fmt.Sprintf("?t=%d", sec)
	}
	return u
}

var youtubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// thumbnailURL is YouTube's public medium thumbnail for id, or "" when id is
// not shaped like a YouTube video id.
func thumbnailURL(id string) string {
	if !youtubeID.MatchString(id) {
		return ""
	}
	return "https://i.ytimg.com/vi/" + id + "/mqdefault.jpg"
}

// sourceLabel is how a client badges a peeq source.
func sourceLabel(channel string) string {
	if channel == "" {
		return "peeq"
	}
	return "peeq · " + channel
}

// joinCounts renders a histogram's first n buckets, with counts or without.
func joinCounts(cs []CategoryCount, n int, withCounts bool) string {
	parts := make([]string, 0, n)
	for i, c := range cs {
		if i == n {
			break
		}
		if withCounts {
			parts = append(parts, fmt.Sprintf("%s (%d)", c.Category, c.Count))
		} else {
			parts = append(parts, c.Category)
		}
	}
	return strings.Join(parts, ", ")
}
