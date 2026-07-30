package ytdlp

import (
	"sync"
	"time"
)

// Status is what peeq knows about the yt-dlp binary at a point in time:
// the version installed on disk, the newest version GitHub publishes, and
// how the last release check went.
type Status struct {
	// Installed is the version `yt-dlp --version` reports, "" when the
	// binary is missing or unrunnable.
	Installed string
	// Latest is the newest published release tag, "" until a check has
	// succeeded. A failed check leaves the last known value in place
	// rather than blanking it: a stale answer is a better basis for
	// "an update exists" than no answer at all.
	Latest string
	// CheckedAt is when Latest was last confirmed — the timestamp of the
	// last SUCCESSFUL check, not the last attempt, so it reads as the age
	// of the information rather than of the effort.
	CheckedAt time.Time
	// CheckErr describes the most recent failed check, "" when the last
	// check succeeded. Without it a permanently unreachable GitHub is
	// indistinguishable from "you are up to date", which is precisely the
	// silent failure this whole check exists to surface.
	CheckErr string
}

// UpdateAvailable reports whether a newer yt-dlp release than the
// installed one has been seen.
//
// yt-dlp versions are zero-padded calendar tags (2026.07.04), so ordering
// them lexicographically is exact — and LatestVersion rejects any tag that
// is not that shape, so the comparison can never be fed something it would
// order wrongly.
//
// The comparison is strictly "installed is older", never "installed
// differs". A nightly or self-built binary is routinely AHEAD of the last
// stable release, and telling that user to update would be wrong.
func (s Status) UpdateAvailable() bool {
	return s.Latest != "" && s.Installed != "" && s.Installed < s.Latest
}

// StatusCache holds the current Status for concurrent readers. The
// version-check ticker writes it; the HTTP handler reads it. Nothing is
// persisted: a restart simply re-checks at boot, and there is no state
// here worth surviving one.
type StatusCache struct {
	mu     sync.RWMutex
	status Status
}

// NewStatusCache returns an empty cache, whose zero Status reports no
// update available — the right answer before the first check completes.
func NewStatusCache() *StatusCache { return &StatusCache{} }

// Get returns a snapshot of the current status.
func (c *StatusCache) Get() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

// SetChecked records a successful check: both versions, the time they were
// confirmed, and the clearing of any earlier check error.
func (c *StatusCache) SetChecked(installed, latest string, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Installed = installed
	c.status.Latest = latest
	c.status.CheckedAt = at
	c.status.CheckErr = ""
}

// SetCheckErr records a failed check. Latest and CheckedAt are left
// untouched on purpose, so the pair keeps reading as "here is what we
// know, and here is how old it is" while the error explains why it is not
// getting any fresher.
func (c *StatusCache) SetCheckErr(installed, msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Installed = installed
	c.status.CheckErr = msg
}

// SetInstalled records the version now on disk without touching anything the
// release check owns. A manual update calls it so the cache does not go on
// describing a binary that has been replaced.
//
// It is NOT what clears the update indicator: the version endpoint reads the
// installed version live on every request, and the ticker re-reads it on every
// tick, so both would report the new binary with or without this. Keeping
// Installed truthful is the point — a stale value here would be wrong for any
// future reader of Status, and UpdateAvailable is computed from it.
func (c *StatusCache) SetInstalled(installed string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Installed = installed
}
