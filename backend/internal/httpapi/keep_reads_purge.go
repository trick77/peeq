package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/ship/kit/httpx"
)

// handleKeepReadsImpact answers what turning the channel's keep_reads off
// would take, so the confirm dialog can name real numbers before anything goes.
func (s *server) handleKeepReadsImpact(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadChannelOr404(w, r)
	if !ok {
		return
	}
	counts, err := s.videos.KeptImpact(c.ID)
	if err != nil {
		httpx.ServerError(w, r, err, "count kept videos failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, counts)
}

// loadChannelOr404 is the lookup both keep_reads reads open with: 503 without
// the stores, 500 on a failed read, 404 for an unknown channel. ok is false
// once it has answered.
func (s *server) loadChannelOr404(w http.ResponseWriter, r *http.Request) (*channels.Channel, bool) {
	if s.channels == nil || s.videos == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "channels are not configured")
		return nil, false
	}
	c, err := s.channels.Get(r.PathValue("id"))
	if err != nil {
		httpx.ServerError(w, r, err, "load channel failed")
		return nil, false
	}
	if c == nil {
		httpx.WriteError(w, http.StatusNotFound, "channel not found")
		return nil, false
	}
	return c, true
}

// handleChannelStorage answers what peeq stores for a channel, split into its
// videos on disk, what is kept after a file went, and the rest — the channel
// Settings tab shows it beside the keep switch. Its own request, never part of
// the channel detail: it visits every video row of the channel.
func (s *server) handleChannelStorage(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadChannelOr404(w, r)
	if !ok {
		return
	}
	// The header's name resolution (handleChannelDetail): a channel row
	// without a name falls back to the one on its videos, so legacy rows
	// count here exactly when they count there.
	name := c.Name
	if name == "" {
		videoName, _, err := s.channels.NameFromVideos(c.ID)
		if err != nil {
			httpx.ServerError(w, r, err, "load channel failed")
			return
		}
		name = videoName
	}
	st, err := s.videos.ChannelStorage(r.Context(), c.ID, name)
	if err != nil {
		httpx.ServerError(w, r, err, "measure channel storage failed")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

// purgeKeptAsync carries out a keep_reads switch-off in the background: the
// channel's tombstones and kept inbox reads are purged, its inbox videos lose
// their index (videos.Store.PurgeKept). A channel can hold hundreds, which is
// too long to hold a request open for; the PUT answers at once.
//
// No Activity row: the log is for automatic work, and this is a click. The
// channel page re-reads its counts instead.
func (s *server) purgeKeptAsync(channelID string) {
	if s.claimPurge(channelID) {
		go s.runPurge(channelID)
	}
}

// resumeKeptPurges runs a switch-off purge for every channel whose keep_reads
// is off, one channel after another. New hands it to Deps.StartWorker, so it
// runs as a tracked worker: a purge runs in the background of the request that
// turned the switch off, so a shutdown can cut it short, and this finishes it.
// On a channel with nothing kept it is one query.
func (s *server) resumeKeptPurges(ctx context.Context) {
	ids, err := s.channels.KeepReadsOff()
	if err != nil {
		slog.Error("keep reads purge: list channels failed", "err", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil || s.purgeBase().Err() != nil {
			return
		}
		if s.claimPurge(id) {
			s.runPurge(id)
		}
	}
}

// claimPurge marks a channel's purge as running and reports whether the caller
// should run it. A switch-off that arrives while one is running is not
// dropped: the running one may already have seen an on in between and be
// stopping, so it is asked for one more pass instead.
func (s *server) claimPurge(channelID string) bool {
	s.purgeMu.Lock()
	defer s.purgeMu.Unlock()
	if s.purgeInFlight[channelID] {
		s.purgeRerun[channelID] = true
		return false
	}
	s.purgeInFlight[channelID] = true
	return true
}

// runPurge purges until no further pass was asked for. Deciding "no rerun"
// and releasing the claim happen under one lock: a claim arriving between the
// two would otherwise set a rerun nobody reads.
func (s *server) runPurge(channelID string) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("keep reads purge: recovered from panic", "channel_id", channelID, "panic", r)
			s.purgeMu.Lock()
			again := s.purgeRerun[channelID]
			delete(s.purgeInFlight, channelID)
			delete(s.purgeRerun, channelID)
			s.purgeMu.Unlock()
			// A switch-off that arrived during the failed pass still asked
			// for one; honour it in a fresh goroutine rather than drop it.
			if again {
				s.purgeKeptAsync(channelID)
			}
		}
		if s.onKeptPurged != nil {
			s.onKeptPurged(channelID)
		}
	}()
	for {
		s.purgeOnce(channelID)
		s.purgeMu.Lock()
		if s.purgeRerun[channelID] && s.purgeBase().Err() == nil {
			delete(s.purgeRerun, channelID)
			s.purgeMu.Unlock()
			continue
		}
		delete(s.purgeInFlight, channelID)
		delete(s.purgeRerun, channelID)
		s.purgeMu.Unlock()
		return
	}
}

// purgeOnce runs one PurgeKept pass and logs what it did. It stops as soon as
// the switch reads on again, so turning it back on halfway keeps whatever was
// not reached yet; a read that fails also stops it: when in doubt, keep.
func (s *server) purgeOnce(channelID string) {
	if s.onPurgePass != nil {
		s.onPurgePass(channelID)
	}
	base := s.purgeBase()
	stillOff := func() bool {
		if base.Err() != nil {
			return false
		}
		c, err := s.channels.Get(channelID)
		return err == nil && c != nil && !c.KeepReads
	}
	done, err := s.videos.PurgeKept(channelID, stillOff)
	if err != nil {
		slog.Error("keep reads purge failed", "channel_id", channelID,
			"deleted", done.Deleted, "ignored", done.Ignored, "inbox", done.Inbox, "err", err)
		return
	}
	if done != (videos.KeptCounts{}) {
		slog.Info("keep reads purge", "channel_id", channelID,
			"deleted", done.Deleted, "ignored", done.Ignored, "inbox", done.Inbox)
	}
}

func (s *server) purgeBase() context.Context {
	if s.background == nil {
		return context.Background()
	}
	return s.background
}
