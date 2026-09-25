package failmonitor

// Sink is the slice of *Monitor a worker feeds: a count-worthy failure for an
// entity, and a reset on success. Narrow and nil-checked by its users, so a
// test can leave it nil.
//
// The download worker and the scan scheduler share ONE Monitor, and each
// dedups independently by its own entity kind — a video id for downloads, a
// channel id for scans — so the auto-pause threshold counts distinct failing
// entities across both. Feeding it the other kind's id (a video id from a
// scan, say) would change what the threshold means.
type Sink interface {
	Fail(entityID string)
	Reset()
}

var _ Sink = (*Monitor)(nil)
