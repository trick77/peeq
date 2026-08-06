package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/videos"
)

// embeddingsResponse mirrors embeddingsDTO on the reading side, so a rename of
// a JSON tag breaks this test rather than the player's card.
type embeddingsResponse struct {
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
	Chunks     int    `json:"chunks"`
	Tokens     int    `json:"tokens"`
	Kinds      []struct {
		Kind   string `json:"kind"`
		Count  int    `json:"count"`
		Tokens int    `json:"tokens"`
	} `json:"kinds"`
}

// getEmbeddings issues an authenticated GET against the endpoint and returns
// the recorder, leaving the status assertion to each test.
func getEmbeddings(t *testing.T, h http.Handler, cookie *http.Cookie, videoID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/videos/"+videoID+"/embeddings", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestVideoEmbeddings_indexedVideo_reportsCountsPerKind(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "indexed"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: "one", Kind: rag.KindTranscript, TokenCount: 100},
		{Ordinal: 1, Text: "two", Kind: rag.KindTranscript, TokenCount: 120},
		{Ordinal: 2, Text: "three", Kind: rag.KindChapter, TokenCount: 40},
		{Ordinal: 3, Text: "four", Kind: rag.KindSummary, TokenCount: 30},
	})

	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := getEmbeddings(t, h, cookie, "v1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp embeddingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Chunks != 4 {
		t.Errorf("chunks = %d, want 4", resp.Chunks)
	}
	if resp.Tokens != 290 {
		t.Errorf("tokens = %d, want 290", resp.Tokens)
	}
	// The index metadata comes off the video row, written by ReplaceVideoChunks.
	if resp.Model != "test-model" || resp.Dimensions != 1536 {
		t.Errorf("model/dim = %q/%d, want test-model/1536", resp.Model, resp.Dimensions)
	}
	if len(resp.Kinds) != 3 {
		t.Fatalf("kinds = %d, want 3: %+v", len(resp.Kinds), resp.Kinds)
	}
	// Largest first, ties by name — the order the card renders in.
	if resp.Kinds[0].Kind != rag.KindTranscript || resp.Kinds[0].Count != 2 || resp.Kinds[0].Tokens != 220 {
		t.Errorf("first kind = %+v, want transcript/2/220", resp.Kinds[0])
	}
	if resp.Kinds[1].Kind != rag.KindChapter || resp.Kinds[2].Kind != rag.KindSummary {
		t.Errorf("tie order = %q,%q, want chapter,summary", resp.Kinds[1].Kind, resp.Kinds[2].Kind)
	}
}

// A video nobody has indexed is not an error: the row exists and the true
// answer about it is "nothing yet", which the card renders as its own line.
func TestVideoEmbeddings_neverIndexed_returnsEmptyNot404(t *testing.T) {
	deps, _, _ := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "not indexed"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := getEmbeddings(t, h, cookie, "v1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp embeddingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Chunks != 0 || len(resp.Kinds) != 0 {
		t.Errorf("chunks/kinds = %d/%d, want 0/0", resp.Chunks, len(resp.Kinds))
	}
	// omitempty: a blank model must be absent rather than reported as "".
	if got := rec.Body.String(); jsonHasKey(t, got, "model") {
		t.Errorf("body carries a model for an unindexed video: %s", got)
	}
	// kinds is a present empty array, never null — the UI maps over it.
	if !jsonHasKey(t, rec.Body.String(), "kinds") {
		t.Errorf("kinds key missing: %s", rec.Body.String())
	}
}

func TestVideoEmbeddings_unknownVideo_404(t *testing.T) {
	deps, _, _ := searchTestDepsWithStores(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	if rec := getEmbeddings(t, h, cookie, "nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// jsonHasKey reports whether the top-level object carries key at all, which is
// what omitempty decides and a decode into a struct cannot tell you.
func jsonHasKey(t *testing.T, body, key string) bool {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	_, ok := raw[key]
	return ok
}
