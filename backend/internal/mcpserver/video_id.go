package mcpserver

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/trick77/peeq/internal/ytdlp"
)

// idLike is what may reach the library and a log line as a video id: a
// YouTube id's alphabet, short. Anything else may be a URL with a query.
var idLike = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// videoArg is a tool's id argument read as a video reference.
type videoArg struct {
	// raw is the trimmed argument, for the client's text only: never log it.
	raw string
	// id is id-shaped, or "" when the argument is no video reference.
	id string
	// start is a moment link's timestamp in seconds, -1 when it has none.
	start int
}

// parseVideoArg reads an id argument: a bare id, peeq's own video link (what
// search sources carry), or a YouTube video link in any common shape
// (scheme optional; watch, youtu.be, shorts, live, embed, /v/; music. and
// youtube-nocookie hosts). Surrounding brackets, quotes and trailing
// punctuation from prose are dropped. A channel or playlist link is no
// video: id stays "".
func (h *handler) parseVideoArg(s string) videoArg {
	a := videoArg{raw: strings.TrimSpace(s), start: -1}
	ref := strings.TrimRight(strings.TrimLeft(a.raw, `<(["'`), `>)]"'.,;:!?`)
	if idLike.MatchString(ref) {
		a.id = ref
		return a
	}
	if ref == "" {
		return a
	}
	if !strings.Contains(ref, "://") {
		ref = "https://" + ref
	}
	u, err := url.Parse(ref)
	if err != nil || u.Host == "" {
		return a
	}
	id, ok := h.peeqLinkID(u)
	if !ok {
		id, ok = youtubeLinkID(u)
	}
	if !ok || !idLike.MatchString(id) {
		return a
	}
	a.id, a.start = id, linkStart(u)
	return a
}

// peeqLinkID is the id in a link to peeq's own video page
// (<public URL>/video/<id>), the URL every search source carries.
func (h *handler) peeqLinkID(u *url.URL) (string, bool) {
	pub, err := url.Parse(h.publicURL)
	if err != nil || pub.Host == "" || !strings.EqualFold(u.Host, pub.Host) {
		return "", false
	}
	rest, ok := strings.CutPrefix(u.Path, strings.TrimRight(pub.Path, "/")+"/video/")
	return strings.Trim(rest, "/"), ok
}

// youtubeLinkID maps the link shapes ytdlp.Canonicalize does not know (its
// strictness guards the download path) onto ones it does, then asks it.
// Only a video, short or live stream is a video id.
func youtubeLinkID(u *url.URL) (string, bool) {
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	switch host {
	case "music.youtube.com", "youtube-nocookie.com":
		host = "youtube.com"
	}
	c := url.URL{Scheme: "https", Host: host, Path: u.Path, RawQuery: u.RawQuery}
	if host == "youtube.com" {
		for _, p := range []string{"/embed/", "/v/"} {
			if id, ok := strings.CutPrefix(u.Path, p); ok {
				c = url.URL{Scheme: "https", Host: host, Path: "/watch",
					RawQuery: url.Values{"v": {strings.Trim(id, "/")}}.Encode()}
			}
		}
	}
	_, id, kind, err := ytdlp.Canonicalize(c.String())
	if err != nil {
		return "", false
	}
	switch kind {
	case "video", "shorts", "live":
		return id, true
	}
	return "", false
}

// linkStart is a moment link's timestamp: t= (YouTube, peeq), start= (embed)
// or #t=; -1 when there is none or it does not parse.
func linkStart(u *url.URL) int {
	q := u.Query()
	v := q.Get("t")
	if v == "" {
		v = q.Get("start")
	}
	if v == "" {
		v, _ = strings.CutPrefix(u.Fragment, "t=")
	}
	return parseTimestamp(v)
}

// parseTimestamp reads YouTube's t= forms: 754, 754s, 12m34s, 1h2m3s; -1 for
// anything else.
func parseTimestamp(s string) int {
	total, num := 0, 0
	digits := false
	for _, r := range s {
		unit := 0
		switch r {
		case 'h':
			unit = 3600
		case 'm':
			unit = 60
		case 's':
			unit = 1
		}
		switch {
		case r >= '0' && r <= '9' && num < 1e6:
			num, digits = num*10+int(r-'0'), true
		case unit > 0 && digits:
			total, num, digits = total+num*unit, 0, false
		default:
			return -1
		}
	}
	if s == "" {
		return -1
	}
	if digits {
		// Bare seconds; digits after a unit ("12m34") are no form YouTube writes.
		if strings.ContainsAny(s, "hms") {
			return -1
		}
		return num
	}
	return total
}

// logVideoID is id for a log line when it is id-shaped (a mistyped id
// too), else "invalid": an argument that did not reduce to an id can be a
// full URL.
func logVideoID(id string) string {
	if idLike.MatchString(id) {
		return id
	}
	return "invalid"
}

// unknownVideoText is the tool error for an argument peeq has no video for.
func unknownVideoText(arg string) string {
	return fmt.Sprintf("No video with id %q in peeq. Use the id from a search result.", arg)
}
