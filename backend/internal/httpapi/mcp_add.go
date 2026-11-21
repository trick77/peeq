package httpapi

import (
	"context"
	"log/slog"

	"github.com/trick77/peeq/internal/logx"
	"github.com/trick77/peeq/internal/mcpserver"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// mcpAddFailed is what a client hears when queueing failed on peeq's side;
// the cause stays in the log.
const mcpAddFailed = "could not queue; try again"

// AddVideos queues each URL the way the Add view's POST /api/downloads does
// (enqueueDownloadByURL: its refusals are the client's messages), except that
// a video with an active job, or downloaded, is left alone: the Add view
// re-queues on purpose, a person pasting it again, while an MCP client
// retries a call it is unsure went through.
//
// The whole call holds mcpAddMu, so an overlapping call sees this one's jobs.
// Cheap: an enqueue is two local writes, never a network call.
func (l mcpLibrary) AddVideos(ctx context.Context, urls []string) []mcpserver.AddedVideo {
	l.s.mcpAddMu.Lock()
	defer l.s.mcpAddMu.Unlock()
	out := make([]mcpserver.AddedVideo, len(urls))
	for i, u := range urls {
		out[i] = l.addVideo(ctx, u)
	}
	return out
}

func (l mcpLibrary) addVideo(ctx context.Context, rawURL string) mcpserver.AddedVideo {
	res := mcpserver.AddedVideo{URL: rawURL, Outcome: mcpserver.AddRejected}
	id, already, err := l.inPipeline(ctx, rawURL)
	if err != nil {
		slog.WarnContext(ctx, "mcp: add video check failed", "video_id", id, "err", err)
		res.Message = mcpAddFailed
		return res
	}
	if already {
		res.VideoID, res.Outcome = id, mcpserver.AddAlready
		return res
	}
	item, ee := l.s.enqueueDownloadByURL(rawURL)
	switch {
	case ee == nil:
		res.VideoID, res.Outcome = item.VideoID, mcpserver.AddQueued
	case ee.cause == nil:
		// A refusal (bad link, playlist, downloads not configured): its
		// message is client-safe, bar a URL's query string it may quote.
		res.Message = logx.RedactText(ee.message)
	default:
		// Never the URL in the log (AGENTS.md); the canonical id when known.
		slog.WarnContext(ctx, "mcp: add video failed", "video_id", id, "status", ee.status, "err", ee.cause)
		res.Message = mcpAddFailed
	}
	return res
}

// inPipeline reports whether a single-video URL's video has a pending or
// running download job or is downloaded. A video whose status says 'queued'
// is not enough: canceling a pending job leaves that status behind with no
// job. Anything not a single-video URL is enqueueDownloadByURL's to answer.
func (l mcpLibrary) inPipeline(ctx context.Context, rawURL string) (string, bool, error) {
	if l.s.videos == nil || l.s.jobs == nil {
		return "", false, nil
	}
	// A parse failure is not ours to report: the enqueue refuses the URL
	// with the Add view's message.
	if _, id, kind, err := ytdlp.Canonicalize(rawURL); err == nil && (kind == "video" || kind == "shorts") {
		return l.videoInPipeline(ctx, id)
	}
	return "", false, nil
}

func (l mcpLibrary) videoInPipeline(ctx context.Context, id string) (string, bool, error) {
	v, err := l.s.videos.GetCtx(ctx, id)
	if err != nil {
		return id, false, err
	}
	if v == nil {
		return id, false, nil
	}
	if v.Status == videos.StatusDownloaded {
		return id, true, nil
	}
	active, err := l.s.jobs.ActiveIDsForVideos([]string{id})
	if err != nil {
		return id, false, err
	}
	return id, len(active) > 0, nil
}
