package sponsorblock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// hashPrefix mirrors the client's own prefix derivation, so the test asserts
// against an independently computed value rather than trusting the code.
func hashPrefix(videoID string) string {
	sum := sha256.Sum256([]byte(videoID))
	return hex.EncodeToString(sum[:])[:4]
}

// TestSegments_requestShape covers the privacy contract: only a four-character
// hash of the video id may appear in the request, never the id itself.
func TestSegments_requestShape(t *testing.T) {
	var gotPath, gotQuery, gotAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery, gotAgent = r.URL.Path, r.URL.RawQuery, r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, srv.Client())
	if _, err := testee.Segments(context.Background(), "dQw4w9WgXcQ", 0); err != nil {
		t.Fatalf("Segments() error = %v", err)
	}

	wantPath := "/api/skipSegments/" + hashPrefix("dQw4w9WgXcQ")
	if gotPath != wantPath {
		t.Fatalf("path = %q, want %q", gotPath, wantPath)
	}
	if strings.Contains(gotPath+gotQuery, "dQw4w9WgXcQ") {
		t.Fatalf("request %q%q leaks the video id", gotPath, gotQuery)
	}
	for _, want := range []string{"service=YouTube", "sponsor", "music_offtopic", "actionTypes"} {
		if !strings.Contains(gotQuery, want) {
			t.Fatalf("query = %q, want it to contain %q", gotQuery, want)
		}
	}
	if !strings.HasPrefix(gotAgent, "peeq/") {
		t.Fatalf("User-Agent = %q, want a peeq/<version> agent", gotAgent)
	}
}

// TestSegments_picksOwnVideoAndSorts covers the hash-prefix response: the
// endpoint answers with every video sharing the prefix, and only the matching
// one may be used.
func TestSegments_picksOwnVideoAndSorts(t *testing.T) {
	body := `[
	  {"videoID":"decoy1","segments":[{"category":"sponsor","segment":[5,10],"videoDuration":600}]},
	  {"videoID":"wanted","segments":[
	    {"category":"outro","segment":[500,600],"videoDuration":600},
	    {"category":"sponsor","segment":[30,45],"videoDuration":600}
	  ]}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, srv.Client())
	segs, err := testee.Segments(context.Background(), "wanted", 600)
	if err != nil {
		t.Fatalf("Segments() error = %v", err)
	}
	if len(segs) != 2 {
		t.Fatalf("segments = %+v, want 2", segs)
	}
	if segs[0].Category != "sponsor" || segs[0].StartTime != 30 || segs[0].EndTime != 45 {
		t.Fatalf("first segment = %+v, want the sponsor one first (sorted by start)", segs[0])
	}
	// 600 - 600 <= 1, so the outro end snaps to the video duration.
	if segs[1].Category != "outro" || segs[1].EndTime != 600 {
		t.Fatalf("second segment = %+v, want outro ending at the video duration", segs[1])
	}
}

// TestSegments_filtersUnusableSegments covers every drop rule in one pass:
// the whole-video label, a category peeq does not store, and a submission
// against a differently-cut video.
func TestSegments_filtersUnusableSegments(t *testing.T) {
	body := `[{"videoID":"v1","segments":[
	  {"category":"selfpromo","segment":[0,0],"videoDuration":600},
	  {"category":"poi_highlight","segment":[100,101],"videoDuration":600},
	  {"category":"chapter","segment":[120,180],"videoDuration":600},
	  {"category":"sponsor","segment":[200,230],"videoDuration":900},
	  {"category":"sponsor","segment":[400,400.0],"videoDuration":600},
	  {"category":"sponsor","segment":[420,410],"videoDuration":600},
	  {"category":"sponsor","segment":[300,330],"videoDuration":600}
	]}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, srv.Client())
	segs, err := testee.Segments(context.Background(), "v1", 600)
	if err != nil {
		t.Fatalf("Segments() error = %v", err)
	}
	if len(segs) != 1 {
		t.Fatalf("segments = %+v, want only the one usable segment", segs)
	}
	if segs[0].StartTime != 300 {
		t.Fatalf("kept segment = %+v, want the 300s one", segs[0])
	}
}

// TestSegments_unknownDurationKeepsSegments: peeq does not always know a
// video's duration (imports, metadata failures). That must not turn into
// "reject everything", which is what comparing against a zero duration would
// otherwise do.
func TestSegments_unknownDurationKeepsSegments(t *testing.T) {
	body := `[{"videoID":"v1","segments":[{"category":"sponsor","segment":[10,25],"videoDuration":600}]}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, srv.Client())
	segs, err := testee.Segments(context.Background(), "v1", 0)
	if err != nil {
		t.Fatalf("Segments() error = %v", err)
	}
	if len(segs) != 1 {
		t.Fatalf("segments = %+v, want the segment kept when our duration is unknown", segs)
	}
	// start <= 1 snapping must not fire for a segment starting at 10.
	if segs[0].StartTime != 10 {
		t.Fatalf("segment start = %v, want 10", segs[0].StartTime)
	}
}

// TestSegments_notFoundIsEmpty: the API answers 404 for a prefix nobody has
// submitted against. That is the common case, not an error.
func TestSegments_notFoundIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, srv.Client())
	segs, err := testee.Segments(context.Background(), "v1", 600)
	if err != nil {
		t.Fatalf("Segments() error = %v, want nil for 404", err)
	}
	if segs != nil {
		t.Fatalf("segments = %+v, want nil", segs)
	}
}

// TestSegments_noMatchingVideoIsEmpty: the prefix matched other videos but not
// ours — also an empty answer, not an error.
func TestSegments_noMatchingVideoIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"videoID":"someoneelse","segments":[{"category":"sponsor","segment":[1,2]}]}]`))
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, srv.Client())
	segs, err := testee.Segments(context.Background(), "v1", 600)
	if err != nil {
		t.Fatalf("Segments() error = %v", err)
	}
	if segs != nil {
		t.Fatalf("segments = %+v, want nil", segs)
	}
}

// TestSegments_serverErrorSurfaces: a 5xx must be reported so the worker
// leaves the video unstamped and retries it later, rather than recording
// "this video has no segments".
func TestSegments_serverErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down for maintenance", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, srv.Client())
	if _, err := testee.Segments(context.Background(), "v1", 600); err == nil {
		t.Fatal("Segments() error = nil, want an error for 503")
	}
}

// TestSegments_malformedBodySurfaces guards the decode path: a proxy or
// captive portal answering 200 with HTML must not read as "no segments".
func TestSegments_malformedBodySurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>hello</html>`))
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, srv.Client())
	if _, err := testee.Segments(context.Background(), "v1", 600); err == nil {
		t.Fatal("Segments() error = nil, want a decode error")
	}
}

// TestSegments_unreachableHostSurfaces covers the transport error path.
func TestSegments_unreachableHostSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	testee := NewClient(url, nil)
	if _, err := testee.Segments(context.Background(), "v1", 600); err == nil {
		t.Fatal("Segments() error = nil, want a transport error")
	}
}

// TestNewClient_defaults documents the zero-argument construction used in
// main.go: the public instance and a client with a timeout.
func TestNewClient_defaults(t *testing.T) {
	testee := NewClient("", nil)
	if testee.baseURL != DefaultBaseURL {
		t.Fatalf("baseURL = %q, want %q", testee.baseURL, DefaultBaseURL)
	}
	if testee.hc.Timeout != requestTimeout {
		t.Fatalf("timeout = %v, want %v", testee.hc.Timeout, requestTimeout)
	}
}

// TestWanted_matchesCategories keeps the exclusions honest: the categories
// peeq deliberately drops must not creep back in through the shared filter.
func TestWanted_matchesCategories(t *testing.T) {
	for _, c := range Categories {
		if !Wanted(c) {
			t.Fatalf("Wanted(%q) = false, want true for a canonical category", c)
		}
	}
	for _, c := range []string{"chapter", "poi_highlight", "hook", ""} {
		if Wanted(c) {
			t.Fatalf("Wanted(%q) = true, want it excluded", c)
		}
	}
	// The request must carry the same list the filter enforces.
	encoded, err := json.Marshal(Categories)
	if err != nil {
		t.Fatalf("marshal Categories: %v", err)
	}
	if strings.Contains(string(encoded), "chapter") {
		t.Fatalf("Categories = %s, want no chapter category", encoded)
	}
}

// TestSegments_invalidBaseURLSurfaces: a misconfigured instance URL must fail
// loudly at request-build time rather than being retried forever as a
// transport error.
func TestSegments_invalidBaseURLSurfaces(t *testing.T) {
	testee := NewClient("http://bad\x7fhost", nil)
	if _, err := testee.Segments(context.Background(), "v1", 600); err == nil {
		t.Fatal("Segments() error = nil, want a request-build error")
	}
}
