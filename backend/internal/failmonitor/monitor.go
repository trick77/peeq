// Package failmonitor tracks consecutive distinct-entity failures shared
// across the download worker and scan scheduler. When enough distinct videos
// or channels fail in a row with extractor/rate-limit errors — a signal that
// the extractor is broken and affects everything, not one bad video — it fires
// a one-shot callback that engages the youtube_paused kill-switch. Any success
// resets the streak. Safe for concurrent use.
package failmonitor

import "sync"

type Monitor struct {
	mu        sync.Mutex
	threshold int
	seen      map[string]struct{}
	engaged   bool
	onEngage  func()
}

// New returns a monitor that calls onEngage once when threshold distinct
// entity ids have failed since the last Reset.
func New(threshold int, onEngage func()) *Monitor {
	if threshold < 1 {
		threshold = 1
	}
	return &Monitor{threshold: threshold, seen: make(map[string]struct{}), onEngage: onEngage}
}

// Fail records a count-worthy failure for entityID (a video id or channel id).
// Duplicate ids within a streak don't advance the count. When the distinct
// count first reaches the threshold, onEngage fires exactly once.
func (m *Monitor) Fail(entityID string) {
	m.mu.Lock()
	if _, dup := m.seen[entityID]; dup || m.engaged {
		m.mu.Unlock()
		return
	}
	m.seen[entityID] = struct{}{}
	fire := len(m.seen) >= m.threshold
	if fire {
		m.engaged = true
	}
	cb := m.onEngage
	m.mu.Unlock()
	if fire && cb != nil {
		cb()
	}
}

// Reset clears the streak and the engaged latch — called on any success or a
// manual resume, so the next broken spell gets a fresh threshold window.
func (m *Monitor) Reset() {
	m.mu.Lock()
	m.seen = make(map[string]struct{})
	m.engaged = false
	m.mu.Unlock()
}
