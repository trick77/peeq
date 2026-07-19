package ytdlp

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// videoIDRe matches a YouTube video id: exactly 11 characters from the
// URL-safe base64-ish alphabet YouTube uses. Real ids can start with '-'
// or '_'; Canonicalize never hands a bare id to the shell, only a full
// https://www.youtube.com/watch?v=<id> URL, so a leading '-' here is not
// an argument-injection risk downstream.
var videoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// Canonicalize parses raw as a YouTube URL and returns its canonical watch
// (or playlist) URL, the video/playlist id, and a content kind. raw must
// be a full URL with a recognized YouTube host and path shape; a bare id
// string is never accepted, so callers can never accidentally construct a
// yt-dlp command line from unparsed user input.
func Canonicalize(raw string) (watchURL, id, kind string, err error) {
	if strings.TrimSpace(raw) == "" {
		return "", "", "", fmt.Errorf("ytdlp: empty url")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", "", "", fmt.Errorf("ytdlp: parse url %q: %w", raw, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", "", "", fmt.Errorf("ytdlp: %q is not a full URL", raw)
	}

	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	path := strings.Trim(u.Path, "/")

	switch host {
	case "youtu.be":
		id = path
		kind = "video"
	case "youtube.com":
		switch {
		case path == "watch":
			id = u.Query().Get("v")
			kind = "video"
		case strings.HasPrefix(path, "shorts/"):
			id = strings.TrimPrefix(path, "shorts/")
			kind = "shorts"
		case strings.HasPrefix(path, "live/"):
			id = strings.TrimPrefix(path, "live/")
			kind = "live"
		case path == "playlist":
			id = u.Query().Get("list")
			kind = "playlist"
		case strings.HasPrefix(path, "channel/"):
			id = strings.TrimPrefix(path, "channel/")
			return "https://www.youtube.com/channel/" + id, id, "channel", nil
		case strings.HasPrefix(path, "@"):
			return "https://www.youtube.com/" + path, path, "channel", nil
		case strings.HasPrefix(path, "c/"):
			id = strings.TrimPrefix(path, "c/")
			return "https://www.youtube.com/c/" + id, id, "channel", nil
		case strings.HasPrefix(path, "user/"):
			id = strings.TrimPrefix(path, "user/")
			return "https://www.youtube.com/user/" + id, id, "channel", nil
		default:
			return "", "", "unknown", fmt.Errorf("ytdlp: unrecognized youtube.com path %q", u.Path)
		}
	default:
		return "", "", "unknown", fmt.Errorf("ytdlp: unrecognized host %q", u.Host)
	}

	if kind == "playlist" {
		if id == "" {
			return "", "", "unknown", fmt.Errorf("ytdlp: missing playlist id in %q", raw)
		}
		return "https://www.youtube.com/playlist?list=" + id, id, kind, nil
	}

	if !videoIDRe.MatchString(id) {
		return "", "", "unknown", fmt.Errorf("ytdlp: invalid video id %q in %q", id, raw)
	}

	return "https://www.youtube.com/watch?v=" + id, id, kind, nil
}
