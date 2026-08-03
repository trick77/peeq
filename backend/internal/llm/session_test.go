package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var sessionIDPattern = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9a-zA-Z]{14}$`)

func TestNewSessionIDShape(t *testing.T) {
	id := newSessionID()
	if !sessionIDPattern.MatchString(id) {
		t.Fatalf("session id %q does not match ses_<12 hex><14 base62>", id)
	}
	if other := newSessionID(); other == id {
		t.Fatalf("consecutive session ids collided: %q", id)
	}
}

func TestChatSessionIDIsStablePerVideo(t *testing.T) {
	first := chatSessionID("video-a")
	if again := chatSessionID("video-a"); again != first {
		t.Fatalf("session id changed for the same video: %q then %q", first, again)
	}
	if other := chatSessionID("video-b"); other == first {
		t.Fatalf("different videos share a session id: %q", other)
	}
	if !sessionIDPattern.MatchString(first) {
		t.Fatalf("video session id %q does not match expected shape", first)
	}
}

func TestChatSessionIDFallsBackToProcessID(t *testing.T) {
	if got := chatSessionID(""); got != processSessionID {
		t.Fatalf("call without a video used %q, want the per-process id %q", got, processSessionID)
	}
}

func TestChatSessionIDCacheIsBounded(t *testing.T) {
	sessionCache.Lock()
	sessionCache.byVideo = map[string]string{}
	sessionCache.order = nil
	sessionCache.Unlock()

	for i := 0; i < sessionCacheLimit+10; i++ {
		chatSessionID(string(rune('a'+i%26)) + string(rune(i)))
	}

	sessionCache.Lock()
	defer sessionCache.Unlock()
	if len(sessionCache.byVideo) > sessionCacheLimit {
		t.Fatalf("cache grew to %d entries, limit is %d", len(sessionCache.byVideo), sessionCacheLimit)
	}
	if len(sessionCache.order) > sessionCacheLimit {
		t.Fatalf("order slice grew to %d entries, limit is %d", len(sessionCache.order), sessionCacheLimit)
	}
}

// TestChatUserAgentValue pins the exact User-Agent string. The header test below
// compares against the constant, so it would happily pass on any value; the
// upstream cares about this specific client string, so assert the literal.
func TestChatUserAgentValue(t *testing.T) {
	const want = "opencode/1.18.11 ai-sdk/openai-compatible/3.0.20 ai-sdk/provider-utils/5.0.18 runtime/bun/1.3.14"
	if chatUserAgent != want {
		t.Fatalf("chatUserAgent = %q, want %q", chatUserAgent, want)
	}
}

func TestCompleteSendsSessionAndUserAgentHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		io.WriteString(w, sseStream("hi", ""))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL}, srv.Client())
	ctx := WithCall(context.Background(), CallInfo{VideoID: "vid-headers"})
	if _, err := c.Complete(ctx, []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}

	if ua := got.Get("User-Agent"); ua != chatUserAgent {
		t.Fatalf("User-Agent = %q, want %q", ua, chatUserAgent)
	}
	if strings.HasPrefix(got.Get("User-Agent"), "Go-http-client") {
		t.Fatal("User-Agent fell back to the net/http default")
	}
	// peeq's client is stream-only, so its Accept stays SSE-specific rather than
	// the */* loom and music send from a shared streaming/non-streaming path.
	if accept := got.Get("Accept"); accept != "text/event-stream" {
		t.Fatalf("Accept = %q, want text/event-stream", accept)
	}
	want := chatSessionID("vid-headers")
	if id := got.Get("X-Session-Id"); id != want {
		t.Fatalf("X-Session-Id = %q, want %q", id, want)
	}
	if affinity := got.Get("x-session-affinity"); affinity != want {
		t.Fatalf("x-session-affinity = %q, want %q", affinity, want)
	}
}
