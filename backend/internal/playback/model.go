package playback

// State is the now-playing pointer as the API returns it. VideoID is "" when
// nothing is playing — which covers both "never set" and "set, but the target is
// no longer playable" (see Get).
//
// Table: playback_state
type State struct {
	VideoID   string `json:"video_id"`
	UpdatedAt string `json:"updated_at,omitempty"`
}
