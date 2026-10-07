package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/ytdlp"
)

// handleChannelAvatar and handleChannelBanner serve a channel's stored
// artwork from the database, like video thumbnails.
func (s *server) handleChannelAvatar(w http.ResponseWriter, r *http.Request) {
	s.serveChannelImage(w, r, channels.ImageAvatar)
}

func (s *server) handleChannelBanner(w http.ResponseWriter, r *http.Request) {
	s.serveChannelImage(w, r, channels.ImageBanner)
}

func (s *server) serveChannelImage(w http.ResponseWriter, r *http.Request, kind string) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	img, err := s.channels.GetImage(r.PathValue("id"), kind)
	if err != nil {
		serverError(w, r, err, "load channel image failed")
		return
	}
	if img == nil {
		http.NotFound(w, r)
		return
	}
	// Artwork changes at most weekly (the metadata refresher's interval), so a
	// day of browser caching is safe and saves a request per channel per page.
	// The pending-thumbnail route has said the same for longer.
	imageOwnedDay.apply(w, r)
	serveStoredImage(w, r, img.Mime, img.Bytes, img.UpdatedAt)
}

// storeChannelArtAsync fetches a just-added channel's avatar and banner in the
// background and stores them on its row; the page shows them on its next load.
// onChannelResolved fires when it is done, so a test can await it.
//
// Detached from the request, which has already answered: each image is a turn
// in the YouTube queue. On the interactive lane, because a person just added
// the channel and is looking at it.
func (s *server) storeChannelArtAsync(channelID, avatarURL, bannerURL string) {
	if s.channels == nil || s.images == nil || (avatarURL == "" && bannerURL == "") {
		return
	}
	go func() {
		defer func() {
			// External input (a CDN response) is parsed here; contain a panic
			// as every other background goroutine in peeq does.
			if r := recover(); r != nil {
				slog.Error("channel art: recovered from panic", "channel_id", channelID, "panic", r)
			}
			if s.onChannelResolved != nil {
				s.onChannelResolved(channelID)
			}
		}()
		ctx := ytdlp.WithInteractive(context.Background())
		s.storeChannelImage(ctx, channelID, channels.ImageAvatar, avatarURL)
		s.storeChannelImage(ctx, channelID, channels.ImageBanner, bannerURL)
	}()
}

// storeChannelImage fetches one piece of channel artwork and stores it on the
// row. Best-effort throughout: a channel with no banner, a CDN blip and an
// unreadable response all mean "no image".
func (s *server) storeChannelImage(ctx context.Context, channelID, kind, url string) {
	if url == "" || s.channels == nil || s.images == nil {
		return
	}
	mime, data, err := s.images(ctx, url)
	if err != nil {
		slog.Warn("channel image fetch failed", "channel_id", channelID, "kind", kind, "err", err)
		return
	}
	if len(data) == 0 {
		return
	}
	if err := s.channels.SetImage(channelID, kind, mime, data); err != nil {
		slog.Warn("channel image store failed", "channel_id", channelID, "kind", kind, "err", err)
	}
}
