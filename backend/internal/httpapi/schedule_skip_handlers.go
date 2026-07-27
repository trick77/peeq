package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/trick77/peeq/internal/channelmeta"
	"github.com/trick77/peeq/internal/scan"
)

// The Up next page lists peeq's own timed housekeeping — the channel scans and
// metadata refreshes that are due soon — and until now it could only be read.
// Downloads and summaries have a Cancel; a scheduled row had nothing, so there
// was no way to say "not this one".
//
// Skip once is what these two endpoints do, and they do it by writing the same
// schedule column the scheduler itself reads. That is the whole mechanism:
// neither loop holds an in-memory schedule (the scan scheduler polls ClaimDue,
// the refresher polls ClaimDueMetadata), so pushing the column out IS the skip.
// Nothing new is persisted, and there is no separate notion of a "skipped"
// occurrence to keep consistent with the projection.
//
// What is deliberately NOT touched is as important. Backoff and
// MarkMetaRefreshed move only next_scan_at / next_meta_refresh_at, leaving
// baselined_at and last_scanned_at exactly as they were. A repeatedly skipped
// channel therefore never looks freshly scanned, and when it is finally scanned
// the baseline it compares against is still the old one — so its back catalogue
// cannot arrive as though it were new. That was the open question on the issue,
// and the answer falls out of using the existing writes rather than inventing a
// skip table.
//
// Undo is the same endpoint with the instant to restore. Both store methods are
// unconditional updates, so handing back the previous value is a complete
// inverse; there is no separate undo path to keep in step. It is deliberately
// NOT guarded against writing an instant later than the one now stored: the
// guard's only effect would be to make undo fail silently in exactly the case
// it is needed, when a scheduler pass has moved the column while the undo
// affordance was on screen. A client-supplied instant can only shift when peeq
// looks at a channel the user is already subscribed to, which "Check now" and
// skipping twice can do anyway.

// skipTimeLayout is the SQLite datetime text form the schedule columns are
// stored in — the same layout the two background loops write and compare.
const skipTimeLayout = "2006-01-02 15:04:05"

// skipAnchor is the instant a skip measures its next slot from: the later of
// now and the occurrence being skipped.
//
// Measuring from now alone is not enough to make a skip a skip. Neither
// due-soon query has a horizon, so Up next lists occurrences right up to a full
// cycle out; a scan already scheduled 20h away, rescheduled from now, would come
// back at the same slot only ~12h out — making Skip advance the very thing it
// was asked to drop. Anchoring on the stored instant makes "strictly later"
// structural: both packages search strictly forward from the anchor, so the
// answer is always at least one interval past the occurrence being skipped.
//
// It is also what keeps a skipped channel on its slot. The stored instant sits
// on the slot, so the next occurrence after it is exactly one cycle later —
// tomorrow at the same time for a scan, next week for a refresh — however many
// times in a row the row is skipped.
//
// A stored value that will not parse falls back to now rather than failing the
// request: a bogus column should not make the row unskippable.
func skipAnchor(now time.Time, scheduled string) time.Time {
	if at, err := time.Parse(skipTimeLayout, scheduled); err == nil && at.After(now) {
		return at
	}
	return now
}

// skipRequest is the optional body. Empty (or absent) means "skip": compute the
// next slot from the owning package's own cadence. Populated means "undo":
// restore this exact instant, which is the value a previous skip handed back.
type skipRequest struct {
	At string `json:"at"`
}

// skipResponse reports where the schedule landed and where it was. previous_at
// is what makes undo possible without the client having to have been watching
// the projection when it skipped.
type skipResponse struct {
	Status     string `json:"status"`
	At         string `json:"at"`
	PreviousAt string `json:"previous_at"`
}

// skipTarget is what the two handlers work out before they diverge: the parsed
// request, and the channel it applies to.
func (s *server) skipTarget(w http.ResponseWriter, r *http.Request) (id string, at string, ok bool) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return "", "", false
	}
	var req skipRequest
	// An absent body is the ordinary skip, so EOF is not an error here — only
	// malformed JSON is.
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return "", "", false
	}
	if req.At != "" {
		// Parse rather than trust. The value goes into a schedule column the
		// two background loops compare against datetime('now'), and text that
		// is not in that form would not sort or compare — it would quietly
		// remove the channel from its own rotation.
		if _, err := time.Parse(skipTimeLayout, req.At); err != nil {
			writeJSONError(w, http.StatusBadRequest, "at must be a UTC timestamp of the form 2006-01-02 15:04:05")
			return "", "", false
		}
	}
	return r.PathValue("id"), req.At, true
}

// handleChannelSkipScan pushes one channel's next scan out by a normal scan
// interval, or restores a previous instant when the body carries one.
func (s *server) handleChannelSkipScan(w http.ResponseWriter, r *http.Request) {
	id, at, ok := s.skipTarget(w, r)
	if !ok {
		return
	}
	sub, err := s.channels.GetSubscription(id)
	if err != nil {
		serverError(w, r, err, "skip scan failed")
		return
	}
	if sub == nil {
		writeJSONError(w, http.StatusBadRequest, "channel is not subscribed")
		return
	}

	next := at
	if next == "" {
		rank, count, err := s.channels.SubscriptionRank(id)
		if err != nil {
			serverError(w, r, err, "skip scan failed")
			return
		}
		next = scan.NextScanAt(skipAnchor(time.Now(), sub.NextScanAt), rank, count)
	}
	if err := s.channels.Backoff(id, next); err != nil {
		serverError(w, r, err, "skip scan failed")
		return
	}
	// Clear any outstanding "Check now" marker. A user who asked for a scan and
	// then skipped the row it produced is no longer waiting for it, and leaving
	// the marker set would make some later automatic pass announce itself on
	// Activity as the answer to a request that was called off.
	//
	// Only on a skip, not on an undo: an undo restores the state before the
	// skip, and re-setting the marker is not something this endpoint can do
	// (RequestScan would also move the schedule, which is the opposite of what
	// an undo wants). The cost is that undoing a skip loses the receipt, not
	// the scan itself — worth noting, not worth a second column.
	if at == "" && sub.ScanRequestedAt != "" {
		if err := s.channels.ClearScanRequest(id, sub.ScanRequestedAt); err != nil {
			slog.Warn("skip scan: clear scan request failed", "channel_id", id, "err", err)
		}
	}
	writeJSON(w, skipResponse{Status: "skipped", At: next, PreviousAt: sub.NextScanAt})
}

// handleChannelSkipMeta is handleChannelSkipScan for the weekly metadata
// refresh.
func (s *server) handleChannelSkipMeta(w http.ResponseWriter, r *http.Request) {
	id, at, ok := s.skipTarget(w, r)
	if !ok {
		return
	}
	sub, err := s.channels.GetSubscription(id)
	if err != nil {
		serverError(w, r, err, "skip metadata refresh failed")
		return
	}
	if sub == nil {
		writeJSONError(w, http.StatusBadRequest, "channel is not subscribed")
		return
	}
	// A channel with no rotation slot has nothing to skip. Every row the
	// projection shows has one (MetaDueSoon excludes NULLs), so this is only
	// reachable directly — and writing a slot here would schedule a refresh
	// rather than skip one, which is the opposite of what was asked.
	if sub.NextMetaRefreshAt == "" {
		writeJSONError(w, http.StatusBadRequest, "channel has no scheduled metadata refresh")
		return
	}

	next := at
	if next == "" {
		rank, count, err := s.channels.SubscriptionRank(id)
		if err != nil {
			serverError(w, r, err, "skip metadata refresh failed")
			return
		}
		next = channelmeta.NextRefreshAt(skipAnchor(time.Now(), sub.NextMetaRefreshAt), rank, count)
	}
	if err := s.channels.MarkMetaRefreshed(id, next); err != nil {
		serverError(w, r, err, "skip metadata refresh failed")
		return
	}
	writeJSON(w, skipResponse{Status: "skipped", At: next, PreviousAt: sub.NextMetaRefreshAt})
}
