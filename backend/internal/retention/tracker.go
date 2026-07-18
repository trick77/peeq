package retention

import (
	"sync"
	"time"
)

// defaultActiveWindow is how recently a video must have been streamed to
// still count as "currently playing". A player re-issues byte-range
// requests every few seconds during playback, so this comfortably covers a
// short pause/seek without falsely calling an idle-but-recently-closed
// stream "active".
const defaultActiveWindow = 5 * time.Minute

// StreamAccessTracker is the production NowPlayingGuard: the video stream
// handler calls RecordAccess on every request, and the sweeper calls
// IsActive to decide whether a video is currently being watched. It
// satisfies httpapi.StreamAccessRecorder (RecordAccess) and
// retention.NowPlayingGuard (IsActive) without either package depending on
// the other's types.
type StreamAccessTracker struct {
	mu     sync.Mutex
	last   map[string]time.Time
	window time.Duration
	now    func() time.Time
}

// NewStreamAccessTracker builds a tracker using the default 5-minute active
// window and the real wall clock.
func NewStreamAccessTracker() *StreamAccessTracker {
	return newStreamAccessTracker(defaultActiveWindow, time.Now)
}

// newStreamAccessTracker is the test seam: an injectable window and clock so
// active/expired behavior can be asserted deterministically.
func newStreamAccessTracker(window time.Duration, now func() time.Time) *StreamAccessTracker {
	return &StreamAccessTracker{
		last:   make(map[string]time.Time),
		window: window,
		now:    now,
	}
}

// RecordAccess stamps id as accessed right now.
func (t *StreamAccessTracker) RecordAccess(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last[id] = t.now()
}

// IsActive reports whether id was accessed within the active window.
func (t *StreamAccessTracker) IsActive(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	last, ok := t.last[id]
	if !ok {
		return false
	}
	return t.now().Sub(last) < t.window
}
