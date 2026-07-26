// Package settings: cookiestatus.go names the YouTube cookie health enum,
// matching the settings.cookie_status CHECK constraint in 0001_init.sql
// exactly. See videos/status.go for why these are named rather than left as
// literals.
package settings

// Cookie status enum values.
//
// CookieAbsent means no cookie has ever been supplied. CookieValid is set when
// one is accepted, either through the Settings page or by the Companion
// extension's PUT /api/machine/cookie. CookieStale and CookieBlocked are both
// written by yt-dlp failure classification, and the distinction matters to the
// user: stale means "sign in again and re-export", blocked means YouTube is
// refusing this client and re-exporting will not help.
//
// Worth knowing when reading the frontend: ui/src/shell/CookieStatus.tsx
// documented an "expired" value that has never existed here, and omitted
// CookieStale which does. Naming the set is what makes that kind of drift
// checkable.
const (
	CookieAbsent  = "absent"
	CookieValid   = "valid"
	CookieStale   = "stale"
	CookieBlocked = "blocked"
)

// CookieStatuses is the fixed enum accepted by the settings.cookie_status
// CHECK constraint.
var CookieStatuses = []string{
	CookieAbsent,
	CookieValid,
	CookieStale,
	CookieBlocked,
}
