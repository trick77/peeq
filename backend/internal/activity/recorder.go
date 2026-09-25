package activity

// Recorder records an Event for the Activity feed. Every worker and store that
// reports outcomes takes one; nil in tests that do not care, the shared *Store
// in production. Use Record rather than calling the method, so a nil recorder
// is a no-op everywhere without each package carrying its own nil check.
type Recorder interface {
	Record(Event)
}

var _ Recorder = (*Store)(nil)

// Record hands e to r when r is set. A nil r records nothing.
func Record(r Recorder, e Event) {
	if r != nil {
		r.Record(e)
	}
}
