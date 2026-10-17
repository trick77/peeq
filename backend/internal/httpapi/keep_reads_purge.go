package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/trick77/peeq/internal/videos"
)

// handleKeepReadsImpact answers what turning the channel's keep_reads off
// would take, so the confirm dialog can name real numbers before anything goes.
func (s *server) handleKeepReadsImpact(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil || s.videos == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")
	c, err := s.channels.Get(id)
	if err != nil {
		serverError(w, r, err, "load channel failed")
		return
	}
	if c == nil {
		writeJSONError(w, http.StatusNotFound, "channel not found")
		return
	}
	counts, err := s.videos.KeptImpact(id)
	if err != nil {
		serverError(w, r, err, "count kept videos failed")
		return
	}
	writeJSON(w, counts)
}

// purgeKeptAsync carries out a keep_reads switch-off in the background: the
// channel's tombstones and kept inbox reads are purged, its inbox videos lose
// their index (videos.Store.PurgeKept). A channel can hold hundreds, which is
// too long to hold a request open for; the PUT answers at once.
//
// No Activity row: the log is for automatic work, and this is a click. The
// channel page re-reads its counts instead.
//
// It stops as soon as the switch reads on again, so turning it back on halfway
// keeps whatever was not reached yet. A read that fails also stops it: when in
// doubt, keep.
//
// A switch-off that arrives while one is running is not dropped: the running
// one may already have seen an on in between and be stopping. It asks for one
// more pass instead, which the goroutine runs before it exits.
func (s *server) purgeKeptAsync(channelID string) {
	s.purgeMu.Lock()
	if s.purgeInFlight[channelID] {
		s.purgeRerun[channelID] = true
		s.purgeMu.Unlock()
		return
	}
	s.purgeInFlight[channelID] = true
	s.purgeMu.Unlock()
	base := s.background
	if base == nil {
		base = context.Background()
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("keep reads purge: recovered from panic", "channel_id", channelID, "panic", r)
			}
			s.purgeMu.Lock()
			delete(s.purgeInFlight, channelID)
			delete(s.purgeRerun, channelID)
			s.purgeMu.Unlock()
			if s.onKeptPurged != nil {
				s.onKeptPurged(channelID)
			}
		}()
		stillOff := func() bool {
			if base.Err() != nil {
				return false
			}
			c, err := s.channels.Get(channelID)
			return err == nil && c != nil && !c.KeepReads
		}
		for {
			PurgeKeptOnce(channelID, s.videos, stillOff)
			s.purgeMu.Lock()
			again := s.purgeRerun[channelID]
			delete(s.purgeRerun, channelID)
			s.purgeMu.Unlock()
			if !again || base.Err() != nil {
				return
			}
		}
	}()
}

// PurgeKeptOnce runs one switch-off purge for a channel and logs what it did.
// main.go calls it at boot for every channel whose switch is off, which is
// what finishes a purge a shutdown or a failed write cut short.
func PurgeKeptOnce(channelID string, vs *videos.Store, stillOff func() bool) {
	done, err := vs.PurgeKept(channelID, stillOff)
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
