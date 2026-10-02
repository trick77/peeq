package scan

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/ytdlp"
)

// listChannel lists a channel's recent uploads AND its recent livestreams,
// returning them as one list (uploads first, deduped by id in case an item ever
// surfaces on both tabs).
//
// The two calls are not equals. A real /videos failure fails the scan, as it
// always has. /streams failing does NOT: the tab is absent entirely for any
// channel that has never gone live, which is most of them, so a failure there
// means "no streams" and the uploads still stand. The exceptions are the two
// account-wide sentinels — a bot block or a dead cookie is not a fact about this
// tab, and swallowing it would leave the cookie status un-flipped and the next
// channel walking into the same wall.
//
// A MISSING TAB is tolerated on either side, symmetrically. A channel whose
// output is entirely livestreams has no /videos tab at all, and yt-dlp refuses
// it exactly the way it refuses /streams on a channel that never streamed.
// Failing the scan there would make the very channels this two-tab listing
// exists for unscannable forever, so an absent /videos tab means "no uploads"
// and the /streams call still runs. A deleted channel is unaffected: Classify
// maps it to a TerminalError, which IsMissingTab never matches, so
// auto-unsubscribe still sees it.
//
// baseline tightens both rules for a first pass only. The baseline snapshot is
// the one listing that must be COMPLETE: everything it fails to see counts as
// new on the next pass, so swallowing a transient /streams failure there would
// dump a channel's whole back catalogue of VODs into the inbox (and, with
// autodownload on, into the download queue). A genuinely absent tab is still
// quiet — that case is IsMissingTab, and there is nothing to miss.
//
// The second tightening covers the gap that left: a missing tab was swallowed
// unconditionally, so a pass where BOTH tabs were refused still handed back a
// confident empty listing and completed the baseline on it. See the guard
// below.
//
// The returned count is how many entries the /streams tab listed, which is what
// answers "does this channel publish through livestreams?" — deliberately not
// the post-dedup number, which would read 0 for a channel whose streams happen
// to also surface on /videos.
//
// skipStreams leaves the /streams call out for a channel recently told it has
// no such tab (see streamsTabKnownMissing). It is ignored when /videos is the
// tab that turns out to be missing: then /streams is all there is to read.
func (s *Scheduler) listChannel(ctx context.Context, ucid string, baseline, skipStreams bool) ([]ytdlp.ChannelEntry, int, streamsTab, error) {
	// answered records whether EITHER tab actually returned a listing. A
	// swallowed missing-tab error is not an answer: it says the tab could not be
	// read, which on its own is indistinguishable from the tab being empty.
	// Only the baseline guard below cares about the difference.
	var answered bool

	uploads, err := s.d.Lister.ChannelVideos(ctx, ucid, s.d.listSize)
	switch {
	case err == nil:
		answered = true
	case ytdlp.IsMissingTab(err):
		s.d.Logger.Debug("scan: channel has no videos tab", "channel_id", ucid)
		uploads = nil
	default:
		// Return before spending a second throttled call on a channel whose
		// first call already failed.
		return nil, 0, streamsNotAsked, fmt.Errorf("scan: list %s: %w", ucid, err)
	}
	if skipStreams && err == nil {
		return uploads, 0, streamsNotAsked, nil
	}
	tab := streamsNotAsked
	streams, serr := s.d.Lister.ChannelStreams(ctx, ucid, s.d.listSize)
	switch {
	case serr == nil:
		answered = true
		tab = streamsAnswered
	case errors.Is(serr, ytdlp.ErrBlocked), errors.Is(serr, ytdlp.ErrCookieExpired):
		return nil, 0, streamsNotAsked, fmt.Errorf("scan: list streams %s: %w", ucid, serr)
	case ytdlp.IsMissingTab(serr):
		s.d.Logger.Debug("scan: channel has no streams tab", "channel_id", ucid)
		tab = streamsMissing
	case baseline:
		return nil, 0, streamsNotAsked, fmt.Errorf("scan: baseline list streams %s: %w", ucid, serr)
	default:
		s.d.Logger.Warn("scan: listing streams failed, using uploads only",
			"channel_id", ucid, "err", serr)
	}

	// A baseline pass where NEITHER tab could be read learned nothing, and must
	// not be allowed to finish. Stamping baselined_at on it converts "yt-dlp
	// could not answer" into the permanent claim "this channel had nothing" —
	// and every video the channel really had is then judged against that stamp
	// forever. Undated entries are not back catalogue by definition
	// (isBackCatalogue is false for an empty PublishedAt), so the channel's
	// whole history arrives at once as undecided inbox items, or as real
	// downloads on an autodownload channel.
	//
	// The predicate is "no tab answered", NOT "a tab was missing". Most channels
	// have never gone live and legitimately have no /streams tab, and a channel
	// that publishes only livestreams has no /videos tab; either alone is a fact
	// about the channel, and blocking on it would make the very channels this
	// two-tab listing exists for unbaselineable. A tab that answers with an
	// EMPTY list is likewise a real answer — a brand-new channel with nothing in
	// it must still baseline, or its first upload would be judged with no
	// baseline at all.
	//
	// Only the first pass is guarded. Later passes have a baseline to judge
	// against, so a blind one there costs a delay rather than a wrong verdict.
	if baseline && !answered {
		return nil, 0, streamsNotAsked, fmt.Errorf("scan: baseline list %s: neither tab could be read", ucid)
	}

	if len(streams) == 0 {
		return uploads, 0, tab, nil
	}
	// Built fresh rather than appended onto uploads: the slice came from the
	// lister and is not ours to grow into.
	merged := make([]ytdlp.ChannelEntry, 0, len(uploads)+len(streams))
	merged = append(merged, uploads...)
	seen := make(map[string]struct{}, len(uploads))
	for _, e := range uploads {
		seen[e.ID] = struct{}{}
	}
	for _, e := range streams {
		if _, dup := seen[e.ID]; dup {
			continue
		}
		seen[e.ID] = struct{}{}
		merged = append(merged, e)
	}
	return merged, len(streams), tab, nil
}

// streamsTab is what one pass learned about a channel's /streams tab.
type streamsTab int

const (
	// streamsNotAsked: the call was skipped, or failed in a way that says
	// nothing about whether the tab exists.
	streamsNotAsked streamsTab = iota
	streamsAnswered
	streamsMissing
)

// streamsTabRecheck is how long a "no streams tab" answer is trusted. It is
// the longest a channel's first-ever stream can go unnoticed, traded against
// one throttled yt-dlp call per channel per scan.
const streamsTabRecheck = 7 * 24 * time.Hour

// streamsTabKnownMissing reports whether this pass may skip the /streams call.
// Never on a baseline pass, which has to see the channel whole, and never on a
// "Scan now": a scan somebody asked for looks at everything.
func (s *Scheduler) streamsTabKnownMissing(sub *channels.Subscription, baseline bool) bool {
	if baseline || sub.ScanRequestedAt != "" || sub.StreamsMissingAt == "" {
		return false
	}
	at, err := time.Parse(store.TimeLayout, sub.StreamsMissingAt)
	if err != nil {
		return false
	}
	return s.d.Now().UTC().Sub(at) < streamsTabRecheck
}

// noteStreamsTab stores what the pass learned. Best-effort: a failed write
// costs one extra call next scan, never the scan itself.
func (s *Scheduler) noteStreamsTab(sub *channels.Subscription, tab streamsTab) {
	var at string
	switch {
	case tab == streamsMissing:
		at = s.d.Now().UTC().Format(store.TimeLayout)
	case tab == streamsAnswered && sub.StreamsMissingAt != "":
		at = ""
	default:
		return
	}
	if err := s.d.Channels.SetStreamsMissing(sub.ChannelID, at); err != nil {
		s.d.Logger.Warn("scan: could not record the streams tab state", "channel_id", sub.ChannelID, "err", err)
	}
}

// isBackCatalogue reports whether an entry was published before the channel was
// first followed, i.e. whether it belongs to the back catalogue the baseline
// pass was supposed to swallow.
//
// The cutoff is baselined_at — "everything that already existed when I started
// following this channel" — and deliberately NOT last_scanned_at, which is the
// tempting reading of "what's new since the last check". last_scanned_at would
// be wrong in a way that loses data: a broadcast that started three days ago and
// only settles into a VOD today has a publish date older than the last scan, so
// it would be marked terminally 'seen' and vanish. That is exactly the silent
// loss the unfinished-stream branch above was written to prevent. Ongoing
// "new since last check" is already the ledger's job; this gate only has to
// answer the question the ledger cannot — whether a source peeq has not looked
// at before is showing us history.
//
// BOTH empty cases fail OPEN (not back catalogue) on purpose:
//   - no publish date: yt-dlp omitted every timestamp, so there is nothing to
//     judge. An unjudgeable entry reaching the inbox is a nuisance; one silently
//     marked 'seen' is a lost video, and 'seen' is terminal.
//   - no baseline: the first pass has not completed, and the baseline branch
//     owns that case anyway.
//
// backCatalogueGrace absorbs skew rather than precision: PublishedAt comes from
// approximate_date, which is derived from relative-time text ("2 weeks ago") and
// so is good to the day only for recent items. Three days is ample at the
// boundary, and it errs toward the inbox — a recoverable nuisance — rather than
// toward a terminal row.
//
// The effective window is three to four days, not exactly three: publishedAt is
// date-only (parsed as midnight UTC) while baselinedAt carries a time of day, so
// a channel baselined at 12:00 admits everything published from cutoff-day+1
// onward and still suppresses the cutoff day itself. That imprecision is
// deliberate — sharpening it would only move a boundary the input is too coarse
// to place anyway — and it stays on the fail-open side of nothing that matters.
const backCatalogueGrace = 3 * 24 * time.Hour

func isBackCatalogue(publishedAt, baselinedAt string) bool {
	if publishedAt == "" || baselinedAt == "" {
		return false
	}
	pub, err := time.Parse("2006-01-02", publishedAt)
	if err != nil {
		return false
	}
	base, err := store.ParseTime(baselinedAt)
	if err != nil {
		return false
	}
	return pub.Before(base.Add(-backCatalogueGrace))
}

// isUnfinishedStream reports whether an entry is a stream that has not settled
// into a finished video yet. Its caller records no ledger row for those, so the
// entry stays recoverable on a later pass — see the comment at that branch for
// why that matters.
//
// It is written as an ALLOWLIST of the settled states — an ordinary upload
// ("not_live", or "" when a flat listing omits the field) and a completed
// stream ("was_live") — so that anything else is deferred. That covers the
// known unsettled states ("is_upcoming", "is_live", and "post_live", the window
// where a broadcast has ended but YouTube has not finished cutting the VOD) and
// also any status yt-dlp grows later. Deferring an unfamiliar status costs a
// day; treating it as finished risks downloading half a broadcast.
//
// It overlaps passesFilters below by design. This is the predicate that decides
// whether an entry is recorded AT ALL, and that is a different question from
// whether an entry qualifies for the inbox; keeping the two separate means
// neither can be broken by editing the other.
func isUnfinishedStream(e ytdlp.ChannelEntry) bool {
	switch e.LiveStatus {
	case "", "not_live", "was_live":
		return false
	default:
		return true
	}
}

// passesFilters drops sub-min-duration entries and, redundantly, any unfinished
// stream. Shorts are excluded by construction — they have their own tab, which
// peeq never lists.
//
// The unfinished-stream check cannot be reached today (the caller matches
// isUnfinishedStream first and skips the entry). It is kept anyway, delegating
// to the same helper so the two can never disagree: this is the predicate that
// decides whether an entry belongs in the inbox, and an airing stream does not,
// whatever the calling code around it comes to look like.
//
// A zero duration (yt-dlp omitted it in flat mode) FAILS OPEN — the video is
// kept, since we'd rather offer a maybe-short video than silently drop uploads.
func passesFilters(e ytdlp.ChannelEntry, minDuration int) bool {
	if isUnfinishedStream(e) {
		return false
	}
	if e.DurationSeconds > 0 && e.DurationSeconds < minDuration {
		return false
	}
	return true
}
