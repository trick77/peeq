package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/trick77/llmwire"
)

// The opencode identity — the client string, the session header pair, the
// ses_-shaped id and its minting — is llmwire's now, switched on with one flag
// in NewClient. What this package still owns, and therefore still tests, is
// that Config.EmulateOpenCode is wired to that flag: on, the identity reaches
// the wire on every call from one client with one id; off, none of it does.
// The shape and rotation of the id are llmwire's to test.
func TestCompleteSendsTheOpenCodeIdentity(t *testing.T) {
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		io.WriteString(w, sseStream("hi", ""))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, EmulateOpenCode: true}, srv.Client())
	for _, video := range []string{"vid-a", "vid-b"} {
		ctx := WithCall(context.Background(), CallInfo{VideoID: video})
		if _, err := c.Complete(ctx, []Message{{Role: "user", Content: "hi"}}); err != nil {
			t.Fatal(err)
		}
	}

	first := seen[0]
	if ua := first.Get("User-Agent"); ua != llmwire.OpenCodeUserAgent {
		t.Fatalf("User-Agent = %q, want %q", ua, llmwire.OpenCodeUserAgent)
	}
	if strings.HasPrefix(first.Get("User-Agent"), "Go-http-client") {
		t.Fatal("User-Agent fell back to the net/http default")
	}
	// peeq's client is stream-only, so its Accept stays SSE-specific rather than
	// the */* loom and music send from a shared streaming/non-streaming path.
	if accept := first.Get("Accept"); accept != "text/event-stream" {
		t.Fatalf("Accept = %q, want text/event-stream", accept)
	}

	id := first.Get(llmwire.HeaderSessionID)
	if !regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`).MatchString(id) {
		t.Fatalf("%s = %q, not a session id", llmwire.HeaderSessionID, id)
	}
	if aff := first.Get(llmwire.HeaderSessionAffinity); aff != id {
		t.Fatalf("%s = %q, want the same value as the session id %q", llmwire.HeaderSessionAffinity, aff, id)
	}
	// One client is one session. This used to be keyed per video, with a cache
	// to keep it so; a session that spans videos is what one running opencode
	// would present, and the affinity it buys — the same upstream node for a
	// burst of calls — does not care which video the burst belongs to.
	if other := seen[1].Get(llmwire.HeaderSessionID); other != id {
		t.Fatalf("a second video changed the session to %q; one client is one session", other)
	}
}

// Off by default: a plain endpoint gets a plain client. Neither the opencode
// string nor the session pair may leak onto the wire when nobody asked for it.
func TestCompleteWithoutEmulationSendsNoOpenCodeIdentity(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		io.WriteString(w, sseStream("hi", ""))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL}, srv.Client())
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if ua := got.Get("User-Agent"); ua != llmwire.DefaultUserAgent {
		t.Fatalf("User-Agent = %q, want %q", ua, llmwire.DefaultUserAgent)
	}
	for _, h := range []string{llmwire.HeaderSessionID, llmwire.HeaderSessionAffinity} {
		if v := got.Get(h); v != "" {
			t.Fatalf("%s = %q sent with emulation off", h, v)
		}
	}
}
