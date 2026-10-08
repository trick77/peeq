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

// storeChannelArtAsync fetches a just-added or refreshed channel's avatar and
// banner in the background and stores them on its row; the page shows them on
// its next load. onArt fires when it is done, so a test can await it.
//
// Detached from the request, which has already answered: each image is a turn
// in the YouTube queue. On the interactive lane, because a person is looking
// at the channel. On the process-lifetime ctx, so a shutdown stops it waiting
// for a turn. One fetch per channel at a time: a repeated Refresh while one is
// queued does not queue the same two turns again.
func (s *server) storeChannelArtAsync(channelID, avatarURL, bannerURL string) {
	if s.channels == nil || s.images == nil || (avatarURL == "" && bannerURL == "") {
		return
	}
	s.artMu.Lock()
	if s.artInFlight[channelID] {
		s.artMu.Unlock()
		return
	}
	s.artInFlight[channelID] = true
	s.artMu.Unlock()
	base := s.background
	if base == nil {
		base = context.Background()
	}
	go func() {
		defer func() {
			// External input (a CDN response) is parsed here; contain a panic
			// as every other background goroutine in peeq does.
			if r := recover(); r != nil {
				slog.Error("channel art: recovered from panic", "channel_id", channelID, "panic", r)
			}
			s.artMu.Lock()
			delete(s.artInFlight, channelID)
			s.artMu.Unlock()
			if s.onArt != nil {
				s.onArt(channelID)
			}
		}()
		ctx := ytdlp.WithInteractive(base)
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
