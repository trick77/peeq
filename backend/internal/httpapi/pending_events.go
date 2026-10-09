package httpapi

import (
	"encoding/json"

	"github.com/trick77/peeq/internal/sse"
)

// PublishInboxChanged tells every open Inbox that a card changed or left, so
// it refetches instead of offering a decision that was already made. videoID
// is empty when the change spans many cards (a deleted channel). The one
// definition of the "inbox" event: the handlers and the caption fetcher
// (wired in main) both publish through it. A nil hub is a no-op.
func PublishInboxChanged(hub *sse.Hub, videoID string) {
	if hub == nil {
		return
	}
	data, err := json.Marshal(struct {
		VideoID string `json:"video_id,omitempty"`
	}{videoID})
	if err != nil {
		return
	}
	hub.Publish("inbox", string(data))
}

// publishInboxChanged is PublishInboxChanged on this server's hub. The tab
// that acted has updated itself already; this is for the others.
func (s *server) publishInboxChanged(videoID string) {
	PublishInboxChanged(s.sseHub, videoID)
}
