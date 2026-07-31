// Package channelmeta reads a channel's metadata from YouTube — name,
// @handle, description, avatar, banner, subscriber count, verified flag — and
// stores it, plus the background worker that keeps that copy from going stale.
//
// It exists as its own package because two very different callers need the
// same fetch-and-store step: the HTTP layer, which resolves a channel the
// first time someone opens its page, and the refresher worker below, which
// re-reads subscribed channels once a week. That step used to be a method on
// the HTTP server and therefore unreachable from any worker.
package channelmeta

import (
	"context"
	"fmt"
	"log/slog"
	neturl "net/url"
	"time"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/ytdlp"
)

// sqlTimeLayout is SQLite's datetime('now') text form (UTC), matching the
// layout every other peeq package writes timestamps in.
const sqlTimeLayout = "2006-01-02 15:04:05"

// Resolver resolves a canonicalized channel url to its identity via yt-dlp:
// the authoritative UCID and display name, the description, the subscriber
// count and verified flag, and the remote avatar/banner urls the channel page
// renders.
//
// It also reports the channel's @handle, but that one is only a FALLBACK: a
// pasted url is what the user typed and what they expect to see back, so it
// wins wherever one exists. yt-dlp's is what gives a handle to a channel peeq
// never saw a url for — an import, or a channel discovered from a video.
//
// Declaring it here (rather than depending on the concrete *ytdlp.Runner type)
// keeps both callers testable with a fake that never shells out to yt-dlp; the
// real *ytdlp.Runner satisfies it.
type Resolver interface {
	ResolveChannel(ctx context.Context, url string) (ytdlp.ChannelInfo, error)
}

// var _ Resolver = (*ytdlp.Runner)(nil) proves at compile time that the real
// Runner still satisfies Resolver, so a signature drift in either type breaks
// the build immediately rather than rotting silently.
var _ Resolver = (*ytdlp.Runner)(nil)

// Refresher fetches channel metadata and stores it. It holds no state, so a
// single instance is shared by the HTTP layer and the worker.
type Refresher struct {
	Channels *channels.Store
	Resolver Resolver
	MediaDir string
	Logger   *slog.Logger
}

func (f *Refresher) logger() *slog.Logger {
	if f.Logger != nil {
		return f.Logger
	}
	return slog.Default()
}

// Resolve fetches channelID's metadata from YouTube and stores it, recording
// the attempt either way. cached is the channel's existing row, or nil when it
// has none, which decides how a FAILURE is remembered: an existing row is
// marked (keeping whatever metadata it holds), while a channel with no row
// gets a bare one purely to carry resolved_at, so the failure is remembered
// and not retried on every visit.
//
// The returned error is the resolve failure itself. Callers that ran this for
// a user who is waiting report it; background paths only log it.
func (f *Refresher) Resolve(ctx context.Context, channelID string, cached *channels.Channel) error {
	now := time.Now().UTC().Format(sqlTimeLayout)
	// PathEscape, not raw concatenation: an id reaches this from a URL path
	// segment, and Go's ServeMux hands back the DECODED value — so a "%2F" in
	// the request turns into a real "/" here and a crafted id would otherwise
	// steer yt-dlp at a different youtube.com page ("UCx/../../watch?v=…").
	// Escaping keeps the id a single path segment whatever it contains.
	url := "https://www.youtube.com/channel/" + neturl.PathEscape(channelID)
	// recordAttempt remembers that a resolve was tried and did not produce
	// usable metadata. EVERY unsuccessful exit has to go through it: the
	// resolved_at it writes is what stops maybeResolveChannel re-fetching the
	// channel on every single page visit, so an early return that skips it
	// turns one broken channel into an unbounded stream of YouTube calls.
	recordAttempt := func() {
		if cached == nil {
			// No row yet — create a bare one purely to carry resolved_at.
			if uerr := f.Channels.Upsert(channels.Channel{ID: channelID, ResolvedAt: now}); uerr != nil {
				f.logger().Error("cache channel after failed resolve", "channel_id", channelID, "err", uerr)
			}
			return
		}
		if merr := f.Channels.MarkResolveAttempted(channelID, now); merr != nil {
			f.logger().Error("mark resolve attempted", "channel_id", channelID, "err", merr)
		}
	}

	info, err := f.Resolver.ResolveChannel(ctx, url)
	if err != nil {
		recordAttempt()
		return err
	}

	// The UCID yt-dlp reports is the authoritative identity of whatever it
	// actually fetched, and it is not necessarily the channel we asked for: a
	// stale or redirecting url resolves to a DIFFERENT channel, and writing
	// that response onto this row would silently replace one channel's name,
	// artwork and subscriber count with another's — while resolve_ok asserts
	// the result is current. Refuse instead, and let the caller report it.
	// This matters more here than it did on the manual button: a weekly
	// unattended refresh would corrupt rows with nobody watching.
	//
	// A mismatch counts as an unsuccessful attempt and is recorded like any
	// other. It is a PERSISTENT condition — the url resolves elsewhere and
	// will keep doing so — so treating it as "not yet resolved" would leave
	// the channel re-fetching itself on every visit for as long as it exists.
	if info.UCID != "" && info.UCID != channelID {
		recordAttempt()
		return fmt.Errorf("resolved to a different channel (%s)", info.UCID)
	}

	// The stored handle wins over yt-dlp's, matching how the add-channel
	// handler resolves the same conflict: a handle peeq already has came from
	// a url the user pasted, and a refresh must not rewrite it underneath
	// them. yt-dlp's is what gives a handle to a channel that has none.
	handle := info.Handle
	if cached != nil && cached.Handle != "" {
		handle = cached.Handle
	}

	// Artwork goes into the row (migration 0023). Best-effort in both
	// directions: a fetch that fails is logged and skipped rather than stored,
	// so a blip cannot replace good artwork with nothing — the same rule the
	// avatar_path/banner_path COALESCE guards used to encode for the columns.
	f.storeImage(ctx, channelID, channels.ImageAvatar, info.AvatarURL)
	f.storeImage(ctx, channelID, channels.ImageBanner, info.BannerURL)
	if uerr := f.Channels.SaveResolved(channels.Channel{
		ID:          channelID,
		Name:        info.Name,
		Handle:      handle,
		Description: info.Description,
		Subscribers: info.Subscribers,
		Verified:    info.Verified,
		ResolvedAt:  now,
	}); uerr != nil {
		f.logger().Error("cache resolved channel", "channel_id", channelID, "err", uerr)
		return fmt.Errorf("cache resolved channel %s: %w", channelID, uerr)
	}
	return nil
}

// storeImage fetches one piece of channel artwork and stores it on the row.
//
// Every failure mode is a warning, never an error the caller acts on: a channel
// with no banner, a CDN blip and an unreadable response all mean "no new image
// this time", and the artwork already stored stays exactly as it was.
func (f *Refresher) storeImage(ctx context.Context, channelID, kind, url string) {
	if url == "" {
		return
	}
	mime, data, err := media.FetchImageBytes(ctx, url)
	if err != nil {
		f.logger().Warn("channel image fetch failed", "channel_id", channelID, "kind", kind, "err", err)
		return
	}
	if len(data) == 0 {
		return
	}
	if err := f.Channels.SetImage(channelID, kind, mime, data); err != nil {
		f.logger().Warn("channel image store failed", "channel_id", channelID, "kind", kind, "err", err)
	}
}
