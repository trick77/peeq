package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/ship/kit/sse"
)

// noFlushWriter exposes only the ResponseWriter interface: no Flush, no
// Unwrap, so sse.Open refuses it with ErrNoFlush before sending anything.
type noFlushWriter struct{ http.ResponseWriter }

// A writer that cannot flush would sit on every event, so both streams
// answer 500 instead of a 200 that never delivers.
func TestSSEStreams_writerWithoutFlushIs500(t *testing.T) {
	cases := map[string]struct {
		s    *server
		path string
		h    func(*server, http.ResponseWriter, *http.Request)
	}{
		"downloads": {&server{sseHub: sse.NewHub[Event](sse.HubOptions{})}, "/api/downloads/stream", (*server).handleDownloadsStream},
		"answer":    {&server{rag: &failingRag{}}, "/api/search/answer?q=x", (*server).handleAnswer},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// The handler alone: behind kit's AccessLog, whose StatusWriter
			// always has Flush, the refusal comes later (a failed flush after
			// the 200). A net/http writer always flushes, so only a test
			// writer reaches this.
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { tc.h(tc.s, w, r) })
			rec := httptest.NewRecorder()
			h.ServeHTTP(noFlushWriter{rec}, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct == "text/event-stream" {
				t.Fatal("refusal was sent as an event stream")
			}
		})
	}
}
