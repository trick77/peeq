package activity

// Event is one recorded piece of automatic work. Subject is the display name
// frozen at write time (this log outlives the video/channel it names), so it is
// never re-joined.
//
// Table: activity_events
type Event struct {
	ID        int64  `json:"id"`
	At        string `json:"at"`
	Kind      string `json:"kind"`
	Outcome   string `json:"outcome"`
	SubjectID string `json:"subject_id,omitempty"`
	Subject   string `json:"subject,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Detail    string `json:"detail,omitempty"`
}
