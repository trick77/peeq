package mcpserver

import (
	"fmt"
	"strings"

	"github.com/trick77/peeq/internal/ytdlp"
)

// videoIDArg is a tool's video id argument as an id: a YouTube video URL
// (watch, youtu.be, shorts, live) becomes its id, anything else comes back
// trimmed. A channel or playlist URL stays as given, so the lookup misses
// rather than reading a channel id as a video id.
func videoIDArg(s string) string {
	s = strings.TrimSpace(s)
	_, id, kind, err := ytdlp.Canonicalize(s)
	if err != nil {
		return s
	}
	switch kind {
	case "video", "shorts", "live":
		return id
	}
	return s
}

// logVideoID is id for a log line when it has a video id's shape, else
// "invalid": an argument that did not reduce to an id can be a full URL.
func logVideoID(id string) string {
	if youtubeID.MatchString(id) {
		return id
	}
	return "invalid"
}

// unknownVideoText is the tool error for an id peeq has no video for.
func unknownVideoText(id string) string {
	return fmt.Sprintf("No video with id %q in peeq. Use the id from a search result.", id)
}
