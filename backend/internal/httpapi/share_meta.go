package httpapi

import (
	"bytes"
	"fmt"
	"html"
	"image"
	_ "image/gif" // register decoders for whatever yt-dlp's --write-thumbnail produced
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode"

	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/sharecard"
	"github.com/trick77/peeq/internal/videos"
	_ "golang.org/x/image/webp" // yt-dlp thumbnails are frequently .webp (decode-only)
)

// handleShareShell serves the HTML shell for /s/{token} with Open Graph and
// Twitter meta injected, so a shared link pasted into Slack, iMessage, WhatsApp
// or Discord unfurls as a real card instead of a bare "peeq". The SPA sets
// document.title client-side, which no unfurler ever runs — only server-rendered
// tags reach a crawler.
//
// This route is registered ahead of the SPA catch-all purely to decorate it: a
// human always boots the same app either way. An unknown, expired or revoked
// token gets the undecorated shell (200, no tags), NOT a 404 — the page's own
// neutral dead-end then renders, and a dead link stays indistinguishable from
// one that never existed, exactly as resolveShare's uniform 404 intends.
func (s *server) handleShareShell(w http.ResponseWriter, r *http.Request) {
	if len(s.shell) == 0 { // no embedded shell (tests, or an unbuilt frontend)
		s.static.ServeHTTP(w, r)
		return
	}
	tags := ""
	if v := s.lookupShared(r); v != nil {
		tags = s.videoMeta(r, v)
	}
	serveShell(w, s.shell, tags)
}

// lookupShared resolves a share token to its video WITHOUT writing any response,
// so the meta path can fall back to the plain shell. It is the read-only twin of
// resolveShare, which owns the uniform-404 behavior for the data routes.
func (s *server) lookupShared(r *http.Request) *videos.Video {
	if s.shareLinks == nil || s.videos == nil {
		return nil
	}
	videoID, ok, err := s.shareLinks.Resolve(r.Context(), r.PathValue("token"))
	if err != nil || !ok {
		return nil
	}
	v, err := s.videos.Get(videoID)
	if err != nil {
		return nil
	}
	return v
}

// videoMeta builds the OG/Twitter block for a live shared video. The og:image is
// the rendered card at /api/s/{token}/card.jpg — addressed by the share token,
// never by the video id, which publicVideoDTO withholds on purpose (peeq's video
// id IS the YouTube id).
func (s *server) videoMeta(r *http.Request, v *videos.Video) string {
	base := s.externalBase(r)
	token := r.PathValue("token")
	img := base + "/api/s/" + token + "/card.jpg"
	return buildMeta("video.other", v.Title, shareDescription(v), img, base+"/s/"+token, 1200, 1200)
}

// shareDescription is the unfurl's subtitle: who made it and how long it runs,
// plus the opening of the summary when one finished. The summary is stripped of
// markdown and clamped, so a card never shows raw "## Overview" or a wall of text.
func shareDescription(v *videos.Video) string {
	line := v.ChannelName
	if d := shareDuration(v.DurationSeconds); d != "" {
		if line != "" {
			line += " · "
		}
		line += d
	}
	if v.SummaryStatus == videos.SummaryDone {
		if snip := clampWords(plainText(v.Summary), 200); snip != "" {
			if line != "" {
				line += " · "
			}
			line += snip
		}
	}
	return line
}

// shareCardSubtitle is the one line printed under the title ON the card. It stays
// channel · duration even when a summary exists: the card has room for one line,
// and the provenance is what a recipient needs from a glance at the image.
func shareCardSubtitle(v *videos.Video) string {
	sub := v.ChannelName
	if d := shareDuration(v.DurationSeconds); d != "" {
		if sub != "" {
			sub += " · "
		}
		sub += d
	}
	return sub
}

// shareDuration renders a runtime the way the UI writes it ("8 min", "1 h 12 min").
func shareDuration(sec int64) string {
	if sec <= 0 {
		return ""
	}
	mins := (sec + 59) / 60 // round up, so a 40-second clip is "1 min", not "0 min"
	if mins < 60 {
		return fmt.Sprintf("%d min", mins)
	}
	h, m := mins/60, mins%60
	if m == 0 {
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}

// plainText flattens a markdown summary into one line of prose: heading markers,
// list bullets, emphasis and code fences are dropped, and all whitespace
// (including the newlines between paragraphs) collapses to single spaces.
func plainText(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") {
			continue
		}
		line = strings.TrimLeft(line, "#>-*+ \t")
		if line == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(line)
	}
	out := strings.NewReplacer("**", "", "__", "", "`", "", "*", "", "_", "").Replace(b.String())
	return strings.Join(strings.Fields(out), " ")
}

// clampWords cuts s to at most max runes, ending on a word boundary and marking
// the cut with an ellipsis. A string already within the limit is returned as-is.
func clampWords(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	cut := string(r[:max])
	if i := strings.LastIndexFunc(cut, unicode.IsSpace); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:—-") + "…"
}

// handleShareCard renders the og:image for a share token: the video's thumbnail
// with its title and channel, on peeq's dark surface. Public and gated only by
// the token, exactly like the other /api/s/ routes — a dead token 404s with no
// detail. Cacheable, because unfurlers refetch the image per recipient.
func (s *server) handleShareCard(w http.ResponseWriter, r *http.Request) {
	v := s.resolveShare(w, r)
	if v == nil {
		return
	}
	jpg, err := sharecard.Render(s.loadThumbnail(v.ThumbnailPath), v.Title, shareCardSubtitle(v))
	if err != nil {
		serverError(w, r, err, "render share card failed")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Write(jpg)
}

// loadThumbnail decodes a stored thumbnail, or returns nil — missing, unsafe,
// unreadable and undecodable all mean "no image", and the card falls back to its
// text-only layout instead of the whole unfurl failing.
func (s *server) loadThumbnail(storedPath string) image.Image {
	if storedPath == "" {
		return nil
	}
	safe, err := media.SafeMediaPath(s.mediaDir, storedPath)
	if err != nil {
		return nil
	}
	f, err := os.Open(safe)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16<<20))
	if err != nil {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	return img
}

// buildMeta renders the OG/Twitter block. All dynamic strings are HTML-escaped
// for safe use inside double-quoted attribute values. imgW/imgH let clients
// (notably iMessage) lay the card out without downloading the image first.
func buildMeta(ogType, title, desc, img, url string, imgW, imgH int) string {
	var b strings.Builder
	meta := func(attr, key, val string) {
		b.WriteString(`<meta ` + attr + `="` + key + `" content="` + html.EscapeString(val) + "\">\n")
	}
	meta("property", "og:site_name", "peeq")
	meta("property", "og:type", ogType)
	meta("property", "og:title", title)
	meta("property", "og:description", desc)
	meta("property", "og:url", url)
	meta("name", "twitter:card", "summary_large_image")
	meta("name", "twitter:title", title)
	meta("name", "twitter:description", desc)
	meta("property", "og:image", img)
	meta("name", "twitter:image", img)
	meta("property", "og:image:width", fmt.Sprintf("%d", imgW))
	meta("property", "og:image:height", fmt.Sprintf("%d", imgH))
	return b.String()
}

// externalBase is the absolute origin the card and canonical URL are advertised
// under. Relative URLs never unfurl, so this prefers the configured external
// base (BACKEND_PUBLIC_URL — the same source shareURL copies into the popover)
// and only reconstructs the origin from the request when none is set, honoring a
// reverse proxy's X-Forwarded-Proto.
func (s *server) externalBase(r *http.Request) string {
	if s.publicURL != "" {
		return strings.TrimRight(s.publicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	}
	return scheme + "://" + r.Host
}

// serveShell writes the SPA shell with the meta block injected right after
// </title> (present in both the tracked dist placeholder and a real Vite build);
// failing that, before </head>. An empty block serves the shell untouched.
func serveShell(w http.ResponseWriter, shell []byte, tags string) {
	out := string(shell)
	if tags != "" {
		lower := strings.ToLower(out)
		inject := "\n" + tags
		if i := strings.Index(lower, "</title>"); i >= 0 {
			pos := i + len("</title>")
			out = out[:pos] + inject + out[pos:]
		} else if i := strings.Index(lower, "</head>"); i >= 0 {
			out = out[:i] + inject + out[i:]
		} else {
			out = tags + out
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Same rule as the SPA handler: this response names the hashed bundles, so it
	// must be revalidated or a client keeps booting an old build.
	w.Header().Set("Cache-Control", "no-cache")
	w.Write([]byte(out))
}
