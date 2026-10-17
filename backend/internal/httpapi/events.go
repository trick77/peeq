package httpapi

// Event is one value on the shared sse.Hub: the SSE event name and its JSON
// payload, written to /api/downloads/stream as is. main publishes progress,
// summary and activity events; PublishInboxChanged publishes inbox.
type Event struct {
	Name string
	Data string
}
