// Package ytgate is the cookie and kill-switch check every background loop
// runs before a pass that talks to YouTube, and the cookie rule the scan-now
// endpoint answers with. Its own package, not part of sched, because it
// needs settings (and through it the store): sched stays stdlib-only so any
// package can take its Sleep without pulling in SQLite.
package ytgate

import (
	"context"

	"github.com/trick77/peeq/internal/settings"
)

// CookieAllows is the cookie half of the gate as a pure rule, so the scan-now
// endpoint can answer with the same rule the loops run on: a valid cookie
// always passes; the dev-only anonymous escape hatch relaxes ONLY the absent
// case, exactly like ytdlp.Runner's cookieGate. A stale or blocked cookie is
// a real cookie YouTube rejected — a signal, not an absence — and stays
// closed even in anonymous mode, or every due channel would burn a failed,
// attempt-stamping refresh per pass.
func CookieAllows(status string, allowAnonymous bool) bool {
	if status == settings.CookieValid {
		return true
	}
	return allowAnonymous && status == settings.CookieAbsent
}

// Gate is the pair of checks every background loop that talks to
// YouTube runs before each pass, and that the scan scheduler and the
// channel-metadata refresher each carried a copy of.
//
// CookieStatus is required and is called unconditionally: a nil check here
// would make a caller that forgot to wire it fail OPEN — silently removing the
// protection and calling YouTube with an absent, stale or blocked cookie. A
// nil dependency should take the process down at the first pass instead.
// Paused may be nil (never paused), which is what tests leave it at.
type Gate struct {
	// CookieStatus reports the current cookie status (settings.CookieStatus).
	CookieStatus func(ctx context.Context) string
	// AllowAnonymous is the dev-only escape hatch (config.AllowAnonymousYoutube);
	// see CookieAllows for exactly what it relaxes.
	AllowAnonymous bool
	// Paused reports the youtube_paused kill-switch (settings.YoutubePaused).
	Paused func(ctx context.Context) bool
}

// Open reports whether a pass may talk to YouTube right now: CookieAllows
// and no kill-switch. Re-read on every poll, so pasting a cookie or clearing
// the switch resumes the loop by itself.
func (g Gate) Open(ctx context.Context) bool {
	if !CookieAllows(g.CookieStatus(ctx), g.AllowAnonymous) {
		return false
	}
	if g.Paused != nil && g.Paused(ctx) {
		return false
	}
	return true
}
