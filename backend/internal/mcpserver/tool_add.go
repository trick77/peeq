package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/trick77/peeq/internal/ytdlp"
	mcp "github.com/trick77/toolkit/mcpserver"
)

const (
	// maxAddVideos caps one add_videos call: what a confirmation card can
	// show and a person can check at a glance.
	maxAddVideos = 10
	// maxAddURLBytes refuses a pasted wall of text before it reaches the
	// URL parser, whose error would echo it back.
	maxAddURLBytes = 2048
	// addReasonRunes caps a rejection reason to one short line.
	addReasonRunes = 200
	// upNextPath is the SPA page a queued download shows up on.
	upNextPath = "/up-next"
	// alreadyMessage is a skipped video's status line.
	alreadyMessage = "already queued or downloaded"
)

const addVideosDescription = "Add YouTube videos to the user's peeq library: peeq downloads, transcribes and " +
	"summarizes them in the background. Use it when the user asks to add, save or download YouTube videos. " +
	"Pass full single-video links, at most 10 per call; bare ids, playlists, channels and live streams are " +
	"refused. A video already queued or downloaded is reported, not added twice."

type addArgs struct {
	Videos []addVideo `json:"videos"`
}

type addVideo struct {
	URL string `json:"url"`
	// Title is for the client's confirmation only; peeq reads the real one
	// from YouTube when the download starts.
	Title string `json:"title"`
}

func addVideosSchema() map[string]any {
	item := mcp.ObjectSchema(map[string]any{
		"url": map[string]any{"type": "string", "minLength": 1, "maxLength": maxAddURLBytes,
			"description": "A full YouTube video link (https://youtu.be/… or https://www.youtube.com/watch?v=…)."},
		"title": map[string]any{"type": "string",
			"description": "The video's title, if known. Shown to the user when confirming; peeq does not use it."},
	}, "url")
	return mcp.ObjectSchema(map[string]any{
		"videos": map[string]any{"type": "array", "minItems": 1, "maxItems": maxAddVideos, "items": item,
			"description": "The videos to add, each listed once."},
	}, "videos")
}

func runAddVideos(ctx context.Context, h *handler, raw json.RawMessage) mcp.Result {
	var a addArgs
	if err := mcp.DecodeArgs(raw, &a); err != nil {
		return mcp.ErrorResult("invalid arguments: " + err.Error())
	}
	urls, msg := addURLs(a.Videos)
	if msg != "" {
		return mcp.ErrorResult(msg)
	}
	got := h.lib.AddVideos(ctx, urls)
	if len(got) != len(urls) {
		// The card pairs items[i] with the i-th video: a short answer would
		// put every status on the wrong row.
		slog.Warn("mcp: add videos answered the wrong count", "want", len(urls), "got", len(got))
		return mcp.ErrorResult("adding the videos failed; try again")
	}

	lines := make([]string, len(got))
	items := make([]any, len(got))
	for i, v := range got {
		sent := strings.TrimSpace(a.Videos[i].URL)
		switch v.Outcome {
		case AddQueued:
			lines[i] = "queued: " + sent
			items[i] = map[string]any{"status": "done", "message": "queued"}
		case AddAlready:
			lines[i] = alreadyMessage + ": " + sent
			items[i] = map[string]any{"status": "skipped", "message": alreadyMessage}
		default:
			reason := mcp.OneLine(v.Message, addReasonRunes)
			if reason == "" {
				reason = "not added"
			}
			lines[i] = "rejected: " + sent + ": " + reason
			items[i] = map[string]any{"status": "failed", "message": reason}
		}
	}
	return mcp.Result{
		Text: strings.Join(lines, "\n"),
		// loom's confirmation card: items[i] is the i-th video, link where the
		// queued downloads show.
		Structured: map[string]any{
			"items": items,
			"link":  map[string]any{"url": h.publicURL + upNextPath, "label": "peeq"},
		},
	}
}

// addURLs validates the videos and returns the trimmed URLs to add, or the
// reason the whole call is refused. No bare-id shortcut: any 11-character
// word ("downloading") would pass for one, and the Add view refuses ids too.
func addURLs(vs []addVideo) ([]string, string) {
	if len(vs) == 0 {
		return nil, "videos is required: pass at least one video"
	}
	if len(vs) > maxAddVideos {
		return nil, fmt.Sprintf("at most %d videos per call; split the list", maxAddVideos)
	}
	urls := make([]string, len(vs))
	seen := make(map[string]int, len(vs))
	for i, v := range vs {
		u := strings.TrimSpace(v.URL)
		switch {
		case u == "":
			return nil, fmt.Sprintf("videos[%d].url is required", i)
		case len(u) > maxAddURLBytes:
			return nil, fmt.Sprintf("videos[%d].url is too long", i)
		case strings.IndexFunc(u, unicode.IsControl) >= 0:
			// The text echoes the url on its own line; a newline in it
			// would split one video's line in two.
			return nil, fmt.Sprintf("videos[%d].url contains control characters", i)
		}
		key := videoKey(u)
		if j, dup := seen[key]; dup {
			return nil, fmt.Sprintf("videos[%d] repeats videos[%d]; list each video once", i, j)
		}
		seen[key] = i
		urls[i] = u
	}
	return urls, ""
}

// videoKey is what two urls for the same video share: the video id for any
// link form (youtu.be, watch?v=, shorts, live), else the url itself.
func videoKey(u string) string {
	if _, id, kind, err := ytdlp.Canonicalize(u); err == nil {
		switch kind {
		case "video", "shorts", "live":
			return "video:" + id
		}
	}
	return "url:" + u
}
