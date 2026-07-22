package taimport

import (
	"context"
	"fmt"
	"time"

	"github.com/trick77/peeq/internal/channels"
)

// nextScanAtLayout matches the timestamp format peeq writes elsewhere when
// subscribing (see httpapi/channels_handlers.go).
const nextScanAtLayout = "2006-01-02 15:04:05"

// ChannelLister supplies TubeArchivist's subscribed channels. *Client
// satisfies it; tests use a fake so no HTTP is needed.
type ChannelLister interface {
	AllChannels(ctx context.Context) ([]Channel, error)
}

// ChannelWriter is the slice of *channels.Store this import needs.
type ChannelWriter interface {
	Upsert(c channels.Channel) error
	// Track marks a cached channel as explicitly tracked. channels is now a
	// metadata cache and Upsert only stores identity — tracking is a separate
	// flag (tracked_at) and is what makes the channel appear in the Channels
	// list, so an import must Track as well as Upsert.
	Track(channelID, trackedAt string) error
	Subscribe(channelID, nextScanAt string) error
	// Get returns the channel with the given id, or (nil, nil) if it is not
	// cached yet.
	Get(id string) (*channels.Channel, error)
}

// ChannelResult summarises an import run. Active and Inactive sum to
// Subscribed; Skipped counts channels deliberately left out.
type ChannelResult struct {
	Subscribed int
	Active     int
	Inactive   int
	Skipped    int
	// Names of the inactive channels, so the operator can see which dead
	// subscriptions they inherited. peeq's auto-unsubscribe will retire these
	// on its own over the following days.
	InactiveNames []string
}

// ImportChannels creates a tracked-channel row and a subscription for every
// channel TubeArchivist has subscribed, active or not.
//
// Subscriptions are created with autodownload off — that is the schema default
// (subscriptions.autodownload DEFAULT 0), so Subscribe needs no extra
// argument. This matters: peeq's first scan of a channel baselines it, marking
// everything currently on the channel as "seen" rather than pending, so
// importing a subscription carries it over without flooding the pending queue
// or re-downloading the back catalogue.
//
// Re-running is safe. Upsert refreshes name on conflict and Subscribe is
// ON CONFLICT DO NOTHING, so a partial run can simply be repeated.
//
// When dryRun is true the counts are computed exactly as for a real run but
// nothing is written.
func ImportChannels(ctx context.Context, lister ChannelLister, w ChannelWriter, dryRun bool, now time.Time) (ChannelResult, error) {
	var res ChannelResult

	all, err := lister.AllChannels(ctx)
	if err != nil {
		return res, err
	}

	nextScanAt := now.UTC().Format(nextScanAtLayout)

	for _, c := range all {
		// Defensive: the client already asks for filter=subscribed, but a
		// TubeArchivist version whose filter behaves differently must not
		// quietly create subscriptions for channels that were never followed.
		if !c.Subscribed || c.ID == "" {
			res.Skipped++
			continue
		}

		res.Subscribed++
		if c.Active {
			res.Active++
		} else {
			res.Inactive++
			res.InactiveNames = append(res.InactiveNames, c.Name)
		}

		if dryRun {
			continue
		}

		// TubeArchivist has no @handle to give us. channels.Store.Upsert treats
		// Handle as authoritative — it overwrites whatever is already stored,
		// even with empty — so passing empty here would blank the handle of
		// any channel peeq already resolved from a pasted URL. Look up the
		// existing row first and carry its handle forward; a brand-new
		// channel (Get returns nil, nil) still gets an empty handle, same as
		// before.
		existing, err := w.Get(c.ID)
		if err != nil {
			return res, fmt.Errorf("taimport: look up existing channel %s: %w", c.ID, err)
		}
		var handle string
		if existing != nil {
			handle = existing.Handle
		}
		if err := w.Upsert(channels.Channel{ID: c.ID, Name: c.Name, Handle: handle}); err != nil {
			return res, fmt.Errorf("taimport: cache channel %s: %w", c.ID, err)
		}
		// Upsert only caches identity; tracking is a separate flag (tracked_at)
		// and is what lists the channel on the Channels page. Track reuses the
		// import instant, and its COALESCE keeps any earlier tracked_at, so a
		// re-run stays idempotent.
		if err := w.Track(c.ID, nextScanAt); err != nil {
			return res, fmt.Errorf("taimport: track channel %s: %w", c.ID, err)
		}
		if err := w.Subscribe(c.ID, nextScanAt); err != nil {
			return res, fmt.Errorf("taimport: subscribe %s: %w", c.ID, err)
		}
	}

	return res, nil
}
