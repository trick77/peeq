package activity

import "sort"

// UpcomingItem is one entry in the agenda's future half — a projection over
// peeq's existing schedules and queues, never stored. The httpapi handler builds
// these from the live stores (subscription due-times, pending jobs, …) and hands
// them to Merge; the split keeps the orderable logic here, pure and testable,
// and the store-fetching in the handler where the stores already live.
type UpcomingItem struct {
	// At is the exact instant for scheduled work (a channel's next_scan_at); it
	// is empty for an ordered-not-timed job (a pending download/summary, which
	// happens as soon as the worker is free, not at a clock time).
	At string `json:"at,omitempty"`
	// Kind matches Event.Kind (scan/channel_meta/download/summary/retention/ytdlp).
	Kind string `json:"kind"`
	// Approx marks an ordered or estimated item, so the copy can say "planned",
	// never "will": ordering is a projection, not a promise (the scan loop claims
	// whichever channel is oldest-due, gated on cookie status and the kill-switch).
	Approx  bool   `json:"approx"`
	Subject string `json:"subject,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// Merge orders the projected items ascending by time and caps them to max,
// returning the capped slice plus how many were dropped (the top edge's "N more"
// label). An item with an empty At — an imminent ordered job — sorts before any
// timed one, and the stable sort preserves the caller's claim order among those,
// so "up next: downloading X" sits above "scan Veritasium at 15:00", which is
// what actually happens.
func Merge(items []UpcomingItem, max int) ([]UpcomingItem, int) {
	sort.SliceStable(items, func(i, j int) bool {
		ai, aj := items[i].At, items[j].At
		switch {
		case ai == "" && aj == "":
			return false // both ordered: keep claim order via the stable sort
		case ai == "":
			return true // ordered-now sorts before anything scheduled
		case aj == "":
			return false
		default:
			return ai < aj
		}
	})

	truncated := 0
	if len(items) > max {
		truncated = len(items) - max
		items = items[:max]
	}
	return items, truncated
}
