package httpapi

import "encoding/json"

// publishInboxChanged tells every open Inbox that this video's card changed or
// left, so a second tab or device refetches instead of offering a decision
// that was already made. The tab that acted has updated itself already.
func (s *server) publishInboxChanged(videoID string) {
	if s.sseHub == nil {
		return
	}
	data, err := json.Marshal(map[string]string{"video_id": videoID})
	if err != nil {
		return
	}
	s.sseHub.Publish("inbox", string(data))
}
