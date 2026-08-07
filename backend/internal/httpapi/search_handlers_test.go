package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/videos"
)

// searchTestDeps builds Deps wired for the search/reprocess API: dev auth
// plus videos + rag stores sharing one test database.
func searchTestDeps(t *testing.T) Deps {
	t.Helper()
	deps, _ := searchTestDepsWithDB(t)
	return deps
}

// searchTestDepsWithDB is searchTestDeps for the tests that also need the raw
// handle — e.g. to install a trigger that blocks one specific column write.
func searchTestDepsWithDB(t *testing.T) (Deps, *sql.DB) {
	t.Helper()
	deps, db, _ := searchTestDepsWithStores(t)
	return deps, db
}

// searchTestDepsWithStores also hands back the concrete *rag.Store. Deps.Rag is
// the RagStore interface, which is deliberately just the two read methods the
// search endpoint calls — seeding chunks goes through ReplaceVideoChunks, which
// is not on it and should not be.
func searchTestDepsWithStores(t *testing.T) (Deps, *sql.DB, *rag.Store) {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	ragStore := rag.NewStore(db)
	return Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		Settings:       settings.New(db),
		Videos:         videos.New(db),
		Rag:            ragStore,
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}, db, ragStore
}

// fakeEmbedder is a stub SearchEmbedder that returns a fixed vector (or
// records that it was never supposed to be called).
type fakeEmbedder struct {
	called bool
	vec    []float32
	err    error
}

func (f *fakeEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	f.called = true
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(inputs))
	for i := range inputs {
		out[i] = f.vec
	}
	return out, nil
}

// A name distinct from the real deployments, so a test asserting on the trace
// cannot pass by accidentally matching a hardcoded "text-embedding-3-small".
func (f *fakeEmbedder) Model() string { return "test-embed-model" }

// spySummaryJobs is a stub SummaryEnqueuer that records the last enqueued id.
type spySummaryJobs struct {
	lastID string
	nextID int64
	err    error
}

func (s *spySummaryJobs) Enqueue(videoID string) (int64, error) {
	s.lastID = videoID
	if s.err != nil {
		return 0, s.err
	}
	s.nextID++
	return s.nextID, nil
}

func dim1536(near float32) []float32 {
	v := make([]float32, 1536)
	v[0] = near
	return v
}

// seedChunks writes rows into the rag store's transcript_chunks/vec_chunks/
// fts_chunks tables via ReplaceVideoChunks, so both FTS and semantic search
// have something to find. The vectors themselves are irrelevant to the FTS
// path; a fixed dummy vector satisfies ReplaceVideoChunks' equal-length
// rows/vectors requirement.
func seedChunks(t *testing.T, rs *rag.Store, videoID string, rows []rag.ChunkRow) {
	t.Helper()
	vecs := make([][]float32, len(rows))
	for i := range rows {
		vecs[i] = dim1536(1.0)
	}
	if err := rs.ReplaceVideoChunks(context.Background(), videoID, rag.IndexMeta{Model: "test-model", Dim: 1536, Rev: rag.ChunkRecipeRev}, rows, vecs); err != nil {
		t.Fatalf("seedChunks(%s): %v", videoID, err)
	}
}

// TestSearchGroupsByVideo seeds two videos with chunks/vectors, issues a
// query whose embedding is nearest v1's chunk, and asserts the response
// groups hits by video with match details.
func TestSearchGroupsByVideo(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "iPhone review"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{ID: "v2", URL: "u2", Title: "unrelated"}); err != nil {
		t.Fatalf("seed v2: %v", err)
	}

	ctx := context.Background()
	v1Vec := dim1536(1.0)
	v2Vec := dim1536(-1.0)
	if err := ragStore.ReplaceVideoChunks(ctx, "v1", rag.IndexMeta{Model: "test-model", Dim: 1536, Rev: rag.ChunkRecipeRev},
		[]rag.ChunkRow{{Ordinal: 0, Text: "talking about the new iphone camera", StartSeconds: 10, TokenCount: 5}},
		[][]float32{v1Vec}); err != nil {
		t.Fatalf("seed v1 chunks: %v", err)
	}
	if err := ragStore.ReplaceVideoChunks(ctx, "v2", rag.IndexMeta{Model: "test-model", Dim: 1536, Rev: rag.ChunkRecipeRev},
		[]rag.ChunkRow{{Ordinal: 0, Text: "something else entirely", StartSeconds: 20, TokenCount: 5}},
		[][]float32{v2Vec}); err != nil {
		t.Fatalf("seed v2 chunks: %v", err)
	}

	embedder := &fakeEmbedder{vec: dim1536(1.0)}
	deps.Embedder = embedder
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// mode=ask: the hybrid contract this test pins lives there now. Find mode
	// is keyword-only and deliberately never calls the embedder.
	req := httptest.NewRequest(http.MethodGet, "/api/search?q=iphone&mode=ask", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/search status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !embedder.called {
		t.Fatalf("expected embedder to be called for non-blank query")
	}

	var resp struct {
		Results []struct {
			Video struct {
				ID string `json:"id"`
			} `json:"video"`
			Matches []struct {
				StartSeconds int     `json:"start_seconds"`
				Snippet      string  `json:"snippet"`
				Distance     float64 `json:"distance"`
			} `json:"matches"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v, body = %s", err, rec.Body.String())
	}
	if len(resp.Results) == 0 {
		t.Fatalf("expected at least one result, got none; body = %s", rec.Body.String())
	}
	if resp.Results[0].Video.ID != "v1" {
		t.Fatalf("results[0].video.id = %q, want v1", resp.Results[0].Video.ID)
	}
	if len(resp.Results[0].Matches) == 0 {
		t.Fatalf("expected matches on results[0]")
	}
	if resp.Results[0].Matches[0].StartSeconds != 10 {
		t.Fatalf("matches[0].start_seconds = %d, want 10", resp.Results[0].Matches[0].StartSeconds)
	}
	if resp.Results[0].Matches[0].Snippet == "" {
		t.Fatalf("expected non-empty snippet")
	}
}

// TestSearchBlankQueryReturnsEmpty asserts a blank q short-circuits to an
// empty result set without ever calling the embedder.
func TestSearchBlankQueryReturnsEmpty(t *testing.T) {
	deps := searchTestDeps(t)
	embedder := &fakeEmbedder{vec: dim1536(1.0)}
	deps.Embedder = embedder
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Results []any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("results = %v, want empty", resp.Results)
	}
	if embedder.called {
		t.Fatalf("embedder must not be called for a blank query")
	}
}

// TestSearchDegradesToFTSWhenEmbedFails asserts that a failing embedder no
// longer 502s the whole search: FTS is the floor and keeps working, so a
// transcript-kind keyword hit still comes back with 200.
func TestSearchDegradesToFTSWhenEmbedFails(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "physics talk"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: "quantum entanglement basics", Kind: "transcript", StartSeconds: 5},
		{Ordinal: 1, Text: "a summary of the whole talk", Kind: "summary", StartSeconds: 0},
	})
	deps.Embedder = &fakeEmbedder{err: errors.New("embed endpoint down")}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=entanglement", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (FTS-only, not 502), body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"kind":"transcript"`) {
		t.Errorf("body missing transcript kind: %s", rec.Body.String())
	}
}

// TestSearchSummaryHitHasKindAndZeroTs asserts a summary chunk that matches
// is tagged kind "summary" (rather than defaulting to "transcript").
func TestSearchSummaryHitHasKindAndZeroTs(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "wildlife doc"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: "a summary mentioning platypus", Kind: "summary", StartSeconds: 0},
	})
	deps.Embedder = &fakeEmbedder{vec: dim1536(1.0)}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=platypus", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"kind":"summary"`) {
		t.Errorf("body missing summary kind: %s", rec.Body.String())
	}
}

// TestSearchUnavailable_returns503 asserts that without Rag/Embedder wired
// the endpoint fails closed rather than panicking.
func TestSearchUnavailable_returns503(t *testing.T) {
	deps := searchTestDeps(t)
	deps.Rag = nil
	deps.Embedder = nil
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/api/search?q=iphone", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestReprocessEnqueues asserts POST .../reprocess resets summary_status
// to pending and hands the video id to SummaryJobs, returning 202.
func TestReprocessEnqueues(t *testing.T) {
	deps := searchTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	// The reprocess guard requires media + a subtitle to be present, so
	// seed a normal downloaded-with-subtitle video (the positive path).
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{
		MediaPath: "/media/v1.mp4",
	}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
	}
	// Reprocess gates on the stored transcript.
	if err := deps.Videos.SetTranscript("v1", videos.TranscriptSourceDownload, "WEBVTT\n"); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	if err := deps.Videos.SetSummary("v1", "The old summary.",
		`[{"ts":0,"title":"Intro","source":"mimo"}]`, `[{"ts":5,"text":"Old point."}]`); err != nil {
		t.Fatalf("seed summary: %v", err)
	}
	if err := deps.Videos.SetCategory("v1", "gaming"); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	// Stamp sponsorblock_refreshed_at to now (via a segment write), so the video
	// is NOT a stale-claim candidate going in. Reprocess must reset it back to
	// the never-fetched sentinel and make it claimable again.
	if err := deps.Videos.SetSponsorblockSegments("v1", `[{"category":"sponsor","start":1,"end":2}]`); err != nil {
		t.Fatalf("seed sponsorblock segments: %v", err)
	}
	before, err := deps.Videos.ClaimSponsorblockStale(10)
	if err != nil {
		t.Fatalf("claim sponsorblock stale (before): %v", err)
	}
	if containsCandidate(before, "v1") {
		t.Fatalf("v1 should not be a stale sponsorblock candidate before reprocess")
	}
	spy := &spySummaryJobs{}
	deps.SummaryJobs = spy
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/videos/v1/reprocess", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}
	if spy.lastID != "v1" {
		t.Fatalf("SummaryJobs.Enqueue called with %q, want v1", spy.lastID)
	}
	got, err := deps.Videos.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get video: %v", err)
	}
	if got.SummaryStatus != "pending" {
		t.Fatalf("summary_status = %q, want pending", got.SummaryStatus)
	}
	// The worker skips classification for a video that already has a category,
	// so the reset here is what keeps Reprocess a working way to correct a
	// wrong one.
	if got.Category != videos.UncategorizedCategory {
		t.Fatalf("category = %q, want it cleared so the worker re-classifies", got.Category)
	}
	// The summarize pipeline is resumable and skips the summary step whenever
	// summary <> '', so leaving the old text in place would make this endpoint
	// a no-op: the job would run and the same summary would come back.
	if got.Summary != "" || got.Chapters != "" || got.KeyPoints != "" {
		t.Fatalf("expected the stored analysis cleared, got summary=%q chapters=%q key_points=%q",
			got.Summary, got.Chapters, got.KeyPoints)
	}
	// Reprocess also forces a fresh SponsorBlock read: clearing the refresh
	// sentinel makes the (recently-stamped) video a stale-claim candidate again.
	after, err := deps.Videos.ClaimSponsorblockStale(10)
	if err != nil {
		t.Fatalf("claim sponsorblock stale (after): %v", err)
	}
	if !containsCandidate(after, "v1") {
		t.Fatalf("v1 should be a stale sponsorblock candidate after reprocess (refresh sentinel not reset)")
	}
}

// containsCandidate reports whether id is among the sponsorblock candidates.
func containsCandidate(cands []videos.SponsorblockCandidate, id string) bool {
	for _, c := range cands {
		if c.ID == id {
			return true
		}
	}
	return false
}

// TestReprocess_missingVideo404 asserts unknown ids 404 rather than
// silently enqueueing.
func TestReprocess_missingVideo404(t *testing.T) {
	deps := searchTestDeps(t)
	deps.SummaryJobs = &spySummaryJobs{}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/videos/missing/reprocess", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestReprocess_noJobsConfigured503 asserts the endpoint fails closed when
// SummaryJobs isn't wired.
func TestReprocess_noJobsConfigured503(t *testing.T) {
	deps := searchTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	// Media + subtitle present so the new reprocess guard doesn't shadow
	// the 503-on-unconfigured-SummaryJobs path this test exercises.
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{
		MediaPath: "/media/v1.mp4",
	}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
	}
	// Reprocess gates on the stored transcript.
	if err := deps.Videos.SetTranscript("v1", videos.TranscriptSourceDownload, "WEBVTT\n"); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/videos/v1/reprocess", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestReprocess_tombstonedWithoutSubtitleReturns409 covers a row tombstoned
// before tombstones started keeping the .vtt: subtitle_path is blank, so there
// is no transcript to summarize and re-enqueuing would only flip its valid, kept
// summary to no_transcript.
func TestReprocess_tombstonedWithoutSubtitleReturns409(t *testing.T) {
	deps := searchTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := deps.Videos.SetSummaryStatus("v1", "done", ""); err != nil {
		t.Fatalf("seed summary status: %v", err)
	}
	if err := deps.Videos.Tombstone("v1"); err != nil {
		t.Fatalf("tombstone v1: %v", err)
	}
	spy := &spySummaryJobs{}
	deps.SummaryJobs = spy
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/videos/v1/reprocess", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
	if spy.lastID != "" {
		t.Fatalf("SummaryJobs.Enqueue called with %q, want not called", spy.lastID)
	}
	got, err := deps.Videos.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get video: %v", err)
	}
	if got.SummaryStatus != "done" {
		t.Fatalf("summary_status = %q, want unchanged 'done'", got.SummaryStatus)
	}
}

// TestReprocess_missingSubtitleReturns409 asserts a video that still has
// its media file but lost its subtitle (e.g. partial cleanup) is also
// rejected: there is no transcript to (re)summarize either way.
func TestReprocess_missingSubtitleReturns409(t *testing.T) {
	deps := searchTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{
		MediaPath: "/media/v1.mp4",
	}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
	}
	if err := deps.Videos.SetSummaryStatus("v1", "done", ""); err != nil {
		t.Fatalf("seed summary status: %v", err)
	}
	deps.SummaryJobs = &spySummaryJobs{}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/videos/v1/reprocess", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
}

// TestReprocess_downloadInFlightReturns409 covers the one status that still
// disqualifies a video with a transcript: a re-download of a tombstoned video
// keeps the old subtitle_path while yt-dlp fetches a replacement, so
// summarizing now would read a .vtt being rewritten under it — and the
// download's own success path enqueues a summary job anyway.
func TestReprocess_downloadInFlightReturns409(t *testing.T) {
	for _, status := range []string{videos.StatusQueued, videos.StatusDownloading} {
		t.Run(status, func(t *testing.T) {
			deps := searchTestDeps(t)
			if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1"}); err != nil {
				t.Fatalf("seed v1: %v", err)
			}
			if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{
				MediaPath: "/media/v1.mp4",
			}); err != nil {
				t.Fatalf("seed downloaded: %v", err)
			}
			// Reprocess gates on the stored transcript.
			if err := deps.Videos.SetTranscript("v1", videos.TranscriptSourceDownload, "WEBVTT\n"); err != nil {
				t.Fatalf("seed transcript: %v", err)
			}
			if err := deps.Videos.Tombstone("v1"); err != nil {
				t.Fatalf("tombstone v1: %v", err)
			}
			if err := deps.Videos.SetStatus("v1", status, ""); err != nil {
				t.Fatalf("seed %s status: %v", status, err)
			}
			spy := &spySummaryJobs{}
			deps.SummaryJobs = spy
			h := New(deps)
			cookie := loginAndGetCookie(t, h)

			req := httptest.NewRequest(http.MethodPost, "/api/videos/v1/reprocess", nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409, body = %s", rec.Code, rec.Body.String())
			}
			if spy.lastID != "" {
				t.Fatalf("SummaryJobs.Enqueue called with %q, want not called", spy.lastID)
			}
		})
	}
}

// TestReprocess_tombstonedWithSubtitleReturns202 is the case keeping the .vtt
// exists for: the file is gone but the transcript is not, so the analysis —
// summary, category, chunks, embeddings — can still be rebuilt from it. Losing
// the media must not cost the video its place in search.
func TestReprocess_tombstonedWithSubtitleReturns202(t *testing.T) {
	deps := searchTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{
		MediaPath: "/media/v1.mp4",
	}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
	}
	// Reprocess gates on the stored transcript.
	if err := deps.Videos.SetTranscript("v1", videos.TranscriptSourceDownload, "WEBVTT\n"); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	if err := deps.Videos.SetSummaryStatus("v1", "done", ""); err != nil {
		t.Fatalf("seed summary status: %v", err)
	}
	if err := deps.Videos.Tombstone("v1"); err != nil {
		t.Fatalf("tombstone v1: %v", err)
	}
	spy := &spySummaryJobs{}
	deps.SummaryJobs = spy
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/videos/v1/reprocess", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}
	if spy.lastID != "v1" {
		t.Fatalf("SummaryJobs.Enqueue called with %q, want v1", spy.lastID)
	}
}

// TestReprocess_downloadedWithSubtitleReturns202 is the positive
// companion: a normal downloaded video with a subtitle present must still
// be enqueued for (re)summarization.
func TestReprocess_downloadedWithSubtitleReturns202(t *testing.T) {
	deps := searchTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{
		MediaPath: "/media/v1.mp4",
	}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
	}
	// Reprocess gates on the stored transcript.
	if err := deps.Videos.SetTranscript("v1", videos.TranscriptSourceDownload, "WEBVTT\n"); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	if err := deps.Videos.SetSummaryStatus("v1", "done", ""); err != nil {
		t.Fatalf("seed summary status: %v", err)
	}
	spy := &spySummaryJobs{}
	deps.SummaryJobs = spy
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/videos/v1/reprocess", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}
	if spy.lastID != "v1" {
		t.Fatalf("SummaryJobs.Enqueue called with %q, want v1", spy.lastID)
	}
}

// TestReprocess_categoryResetFailure500 asserts the endpoint fails loudly
// when it cannot clear the category. Swallowing that error would return 202
// while leaving the old category pinned — the worker skips classification for
// a video that already has one, so the user's correction would be silently
// dropped.
func TestReprocess_categoryResetFailure500(t *testing.T) {
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	deps := Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		Settings:       settings.New(db),
		Videos:         videos.New(db),
		Rag:            rag.NewStore(db),
		SummaryJobs:    &spySummaryJobs{},
		DevAuthClaims:  auth.Claims{Subject: "dev-tester", PreferredUsername: "dev"},
	}
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{
		MediaPath: "/media/v1.mp4"}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
	}
	// Reprocess gates on the stored transcript.
	if err := deps.Videos.SetTranscript("v1", videos.TranscriptSourceDownload, "WEBVTT\n"); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	// Block only the category UPDATE, so the handler reaches the reset with
	// every earlier write having succeeded.
	if _, err := db.Exec(`CREATE TRIGGER no_category BEFORE UPDATE OF category ON videos
		BEGIN SELECT RAISE(ABORT, 'category writes blocked'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	req := httptest.NewRequest(http.MethodPost, "/api/videos/v1/reprocess", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestReprocess_clearSummaryFailureIs500 asserts the endpoint fails loudly
// when it cannot wipe the stored analysis. Reporting 202 there would be a lie:
// the summarize pipeline skips the summary step whenever summary <> ”, so the
// job would run and hand back the exact text the user asked to be redone.
func TestReprocess_clearSummaryFailureIs500(t *testing.T) {
	deps, db := searchTestDepsWithDB(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{
		MediaPath: "/media/v1.mp4",
	}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
	}
	// Reprocess gates on the stored transcript.
	if err := deps.Videos.SetTranscript("v1", videos.TranscriptSourceDownload, "WEBVTT\n"); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	if err := deps.Videos.SetSummary("v1", "the old summary", "", ""); err != nil {
		t.Fatalf("seed summary: %v", err)
	}
	deps.SummaryJobs = &spySummaryJobs{}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// Block exactly the summary column, leaving the summary_status write above
	// it working — closing the db would fail the earlier Get instead.
	if _, err := db.Exec(`CREATE TRIGGER no_summary BEFORE UPDATE OF summary ON videos
		BEGIN SELECT RAISE(ABORT, 'summary writes blocked'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/videos/v1/reprocess", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestReprocess_sponsorblockResetFailureIs500 asserts the endpoint fails loudly
// when it cannot clear the SponsorBlock refresh sentinel. A 202 there would be a
// half-truth: the summary would be redone, but the video would keep whatever
// segments it already had — the worker's stale-claim query only reconsiders a
// video whose sentinel was cleared, so nothing would ever re-read it. Failing
// closed keeps Reprocess meaning "redo all of it".
func TestReprocess_sponsorblockResetFailureIs500(t *testing.T) {
	deps, db := searchTestDepsWithDB(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{
		MediaPath: "/media/v1.mp4",
	}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
	}
	// Reprocess gates on the stored transcript.
	if err := deps.Videos.SetTranscript("v1", videos.TranscriptSourceDownload, "WEBVTT\n"); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	spy := &spySummaryJobs{}
	deps.SummaryJobs = spy
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// Block exactly the sentinel column, leaving the summary_status, summary and
	// category writes ahead of it working. The trigger has to go in after the
	// seed: SetDownloaded stamps sponsorblock_refreshed_at itself, so an earlier
	// trigger would abort the seed instead of the call under test.
	if _, err := db.Exec(`CREATE TRIGGER no_sponsorblock BEFORE UPDATE OF sponsorblock_refreshed_at ON videos
		BEGIN SELECT RAISE(ABORT, 'sponsorblock refresh writes blocked'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/videos/v1/reprocess", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
	// The reset runs before the enqueue, so a failure must not leave a job
	// queued — that job would burn an LLM call and report success.
	if spy.lastID != "" {
		t.Fatalf("enqueued %q, want no job after the reset failed", spy.lastID)
	}
}

// failingRag is a RagStore whose chosen read fails; the other delegates to the
// real store. It is what the interface bought: the search handler's two
// degraded paths are deliberately fail-SOFT — a broken FTS or a broken semantic
// retrieve logs and falls through to whatever the other lane returned, rather
// than failing the request. Before the interface there was no way to reach
// either branch from a handler test, and neither was covered.
type failingRag struct {
	real      RagStore
	searchFTS error
	retrieve  error
}

func (f *failingRag) SearchFTS(ctx context.Context, match string, n int) ([]rag.Hit, error) {
	if f.searchFTS != nil {
		return nil, f.searchFTS
	}
	return f.real.SearchFTS(ctx, match, n)
}

func (f *failingRag) Retrieve(ctx context.Context, q []float32, k int) ([]rag.Hit, error) {
	if f.retrieve != nil {
		return nil, f.retrieve
	}
	return f.real.Retrieve(ctx, q, k)
}

func (f *failingRag) RetrieveWithin(ctx context.Context, q []float32, k int, maxDistance float64) ([]rag.Hit, error) {
	if f.retrieve != nil {
		return nil, f.retrieve
	}
	return f.real.RetrieveWithin(ctx, q, k, maxDistance)
}

// The filtered pair is what askLanes actually calls, so the injected failures
// have to reach it — a fake that only broke the unfiltered methods would let
// every degraded-path test pass while exercising nothing.

func (f *failingRag) SearchFTSFiltered(ctx context.Context, match string, n int, flt rag.Filter) ([]rag.Hit, error) {
	if f.searchFTS != nil {
		return nil, f.searchFTS
	}
	return f.real.SearchFTSFiltered(ctx, match, n, flt)
}

func (f *failingRag) RetrieveWithinFiltered(ctx context.Context, q []float32, k int, maxDistance float64, flt rag.Filter) ([]rag.Hit, error) {
	if f.retrieve != nil {
		return nil, f.retrieve
	}
	return f.real.RetrieveWithinFiltered(ctx, q, k, maxDistance, flt)
}

// CountVideos is the inventory path, which is fail-soft in its own way: a count
// that errors is dropped rather than shown, so it always delegates here.
func (f *failingRag) CountVideos(ctx context.Context, flt rag.Filter) (rag.LibraryCount, error) {
	return f.real.CountVideos(ctx, flt)
}

// HasChunks belongs to the ignore path, not the search path, so it always
// delegates: no test here breaks it.
func (f *failingRag) HasChunks(ctx context.Context, videoID string) (bool, error) {
	return f.real.HasChunks(ctx, videoID)
}

// ChunkStats belongs to the player's index card, not the search path, so it
// delegates for the same reason HasChunks does.
func (f *failingRag) ChunkStats(ctx context.Context, videoID string) ([]rag.KindCount, error) {
	return f.real.ChunkStats(ctx, videoID)
}

// TestSearch_ragDegradedStillServes pins the fail-soft contract from both
// sides: whichever lane breaks, the other one's hits still come back 200.
func TestSearch_ragDegradedStillServes(t *testing.T) {
	t.Run("ftsBroken_semanticStillAnswers", func(t *testing.T) {
		deps, _, ragStore := searchTestDepsWithStores(t)
		if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "physics talk"}); err != nil {
			t.Fatalf("seed v1: %v", err)
		}
		seedChunks(t, ragStore, "v1", []rag.ChunkRow{
			{Ordinal: 0, Text: "a chunk about entropy", StartSeconds: 5},
		})
		deps.Rag = &failingRag{real: deps.Rag, searchFTS: errors.New("fts exploded")}
		deps.Embedder = &fakeEmbedder{vec: dim1536(1.0)}

		h := New(deps)
		cookie := loginAndGetCookie(t, h)
		rec := doReq(t, h, cookie, http.MethodGet, "/api/search?q=entropy&mode=ask", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("search with broken FTS = %d, want 200 (fail-soft), body = %s",
				rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "v1") {
			t.Fatalf("semantic lane should still have answered: %s", rec.Body.String())
		}
	})

	t.Run("semanticBroken_ftsStillAnswers", func(t *testing.T) {
		deps, _, ragStore := searchTestDepsWithStores(t)
		if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "physics talk"}); err != nil {
			t.Fatalf("seed v1: %v", err)
		}
		seedChunks(t, ragStore, "v1", []rag.ChunkRow{
			{Ordinal: 0, Text: "a chunk about entropy", StartSeconds: 5},
		})
		deps.Rag = &failingRag{real: deps.Rag, retrieve: errors.New("vec index gone")}
		deps.Embedder = &fakeEmbedder{vec: dim1536(1.0)}

		h := New(deps)
		cookie := loginAndGetCookie(t, h)
		rec := doReq(t, h, cookie, http.MethodGet, "/api/search?q=entropy&mode=ask", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("search with broken semantic = %d, want 200 (fail-soft), body = %s",
				rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "v1") {
			t.Fatalf("FTS lane should still have answered: %s", rec.Body.String())
		}
	})
}

// TestSearchFindModeIsKeywordOnly pins the defining property of Find: it is a
// real full-text search, so it never reaches for the embedder. That is what
// makes it instant and free, and what lets it honestly return nothing.
func TestSearchFindModeIsKeywordOnly(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "physics talk"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: "a chunk about entropy and thermodynamics", StartSeconds: 5},
	})
	embedder := &fakeEmbedder{vec: dim1536(1.0)}
	deps.Embedder = embedder
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// Default mode is find; no explicit mode= needed.
	rec := doReq(t, h, cookie, http.MethodGet, "/api/search?q=entropy", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if embedder.called {
		t.Fatal("find mode must not call the embedder")
	}
	if !strings.Contains(rec.Body.String(), "v1") {
		t.Fatalf("keyword hit missing: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"mode":"find"`) {
		t.Fatalf("response should echo the mode: %s", rec.Body.String())
	}
}

// TestSearchFindModeReturnsNothingWhenWordsAbsent is the honest-empty contract.
// A vector search cannot do this — KNN always returns its k nearest — which is
// why a query about something the library never covered used to come back full
// of unrelated results.
func TestSearchFindModeReturnsNothingWhenWordsAbsent(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "physics talk"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: "a chunk about entropy and thermodynamics", StartSeconds: 5},
	})
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/search?q=electrolytes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var resp struct {
		Results []any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 0 {
		t.Fatalf("want no results for a word the library never uses, got %s", rec.Body.String())
	}
}

// TestSearchFindModeHonoursOperators asserts the operators a full-text search
// is expected to support actually reach FTS5 rather than being flattened into
// ANDed terms.
func TestSearchFindModeHonoursOperators(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	for _, v := range []struct{ id, title, text string }{
		{"v1", "sodium", "sodium losses during long efforts"},
		{"v2", "potassium", "potassium and muscle contraction"},
	} {
		if err := deps.Videos.Upsert(videos.Video{ID: v.id, URL: "u", Title: v.title}); err != nil {
			t.Fatalf("seed %s: %v", v.id, err)
		}
		seedChunks(t, ragStore, v.id, []rag.ChunkRow{{Ordinal: 0, Text: v.text, StartSeconds: 1}})
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// ANDing these two would match nothing; OR must reach FTS5 intact.
	rec := doReq(t, h, cookie, http.MethodGet, "/api/search?q=sodium+OR+potassium", nil)
	body := rec.Body.String()
	if !strings.Contains(body, "v1") || !strings.Contains(body, "v2") {
		t.Fatalf("OR should match both videos: %s", body)
	}

	// NOT must exclude.
	rec = doReq(t, h, cookie, http.MethodGet, "/api/search?q=contraction+NOT+sodium", nil)
	body = rec.Body.String()
	if strings.Contains(body, `"id":"v1"`) {
		t.Fatalf("NOT should have excluded v1: %s", body)
	}
	if !strings.Contains(body, `"id":"v2"`) {
		t.Fatalf("NOT dropped the video it should have kept: %s", body)
	}

	// Prefix. This one is worth exercising against real FTS5 rather than only
	// asserting the emitted string: `"sodi" *` is the one form ParseFTSQuery
	// produces that nothing else here parses, and a rejection would surface to
	// the user not as an error but as "none of your transcripts contain those
	// words" — the failure that looks exactly like a correct empty result.
	rec = doReq(t, h, cookie, http.MethodGet, "/api/search?q=sodi*", nil)
	body = rec.Body.String()
	if !strings.Contains(body, `"id":"v1"`) {
		t.Fatalf("prefix should have matched sodium: %s", body)
	}

	// A quoted phrase stays adjacent.
	rec = doReq(t, h, cookie, http.MethodGet, `/api/search?q=%22muscle+contraction%22`, nil)
	body = rec.Body.String()
	if !strings.Contains(body, `"id":"v2"`) {
		t.Fatalf("phrase should have matched v2: %s", body)
	}
	if strings.Contains(body, `"id":"v1"`) {
		t.Fatalf("phrase should not have matched v1: %s", body)
	}
}

// TestSearchSpreadsAcrossVideos pins what maxMatchesPerVideo is for: one chatty
// video must not consume the whole response. The chunks below are engineered so
// bm25 ranks every one of v1's above v2's (bm25 favours short documents), so if
// the retrieval budget were spent before grouping — the candidate list cut to k
// and only then capped per video — v2 would never appear at all.
func TestSearchSpreadsAcrossVideos(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	for _, id := range []string{"v1", "v2"} {
		if err := deps.Videos.Upsert(videos.Video{ID: id, URL: "u", Title: id}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	chatty := make([]rag.ChunkRow, 25)
	for i := range chatty {
		chatty[i] = rag.ChunkRow{Ordinal: i, Text: "electrolytes", StartSeconds: i * 10}
	}
	seedChunks(t, ragStore, "v1", chatty)
	seedChunks(t, ragStore, "v2", []rag.ChunkRow{
		{Ordinal: 0, Text: "electrolytes " + strings.Repeat("filler words here ", 60), StartSeconds: 3},
	})

	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := doReq(t, h, cookie, http.MethodGet, "/api/search?q=electrolytes", nil)

	var resp struct {
		Results []struct {
			Video   struct{ ID string } `json:"video"`
			Matches []struct {
				StartSeconds int `json:"start_seconds"`
			} `json:"matches"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v, body = %s", err, rec.Body.String())
	}
	if len(resp.Results) != 2 {
		t.Fatalf("want both videos represented, got %d: %s", len(resp.Results), rec.Body.String())
	}
	for _, r := range resp.Results {
		if len(r.Matches) > maxMatchesPerVideo {
			t.Errorf("%s contributed %d matches, cap is %d", r.Video.ID, len(r.Matches), maxMatchesPerVideo)
		}
	}
}

// TestSearchSnippetIsCentredOnTheMatch is the user-visible half of the FTS5
// snippet() change: the preview must contain the searched word. It previously
// returned the chunk's first 160 characters, which for a ~600-token chunk
// usually does not include the match at all.
func TestSearchSnippetIsCentredOnTheMatch(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "long talk"}); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	head := strings.Repeat("filler about training and recovery ", 12)
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: head + "the electrolytes you replace matter " + head, StartSeconds: 872},
	})
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/search?q=electrolytes", nil)
	var resp struct {
		Results []struct {
			Matches []struct {
				Snippet string `json:"snippet"`
			} `json:"matches"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) == 0 || len(resp.Results[0].Matches) == 0 {
		t.Fatalf("expected a hit: %s", rec.Body.String())
	}
	snip := resp.Results[0].Matches[0].Snippet
	if !strings.Contains(strings.ToLower(snip), "electrolytes") {
		t.Errorf("snippet lacks the searched term: %q", snip)
	}
	if !strings.Contains(snip, rag.HighlightStart) {
		t.Errorf("snippet is not highlighted: %q", snip)
	}
}

// Reprocess throws away the stored analysis the index was built from, so it
// must also mark the index stale. Embedding is gated on the content recipe now:
// without this the worker would skip embedding entirely and leave the OLD
// summary chunk indexed against a video whose summary has been wiped.
func TestReprocessMarksIndexStale(t *testing.T) {
	deps, db, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{
		ID: "v1", URL: "u1", Title: "t", Status: videos.StatusDownloaded,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE videos SET media_path='m.mp4' WHERE id='v1'`); err != nil {
		t.Fatal(err)
	}
	if err := deps.Videos.SetTranscript("v1", videos.TranscriptSourceDownload, "WEBVTT\n"); err != nil {
		t.Fatalf("seed transcript: %v", err)
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{{Ordinal: 0, Text: "old summary", StartSeconds: 0}})
	if _, err := db.Exec(`UPDATE videos SET embed_rev=? WHERE id='v1'`, rag.ChunkRecipeRev); err != nil {
		t.Fatal(err)
	}
	deps.SummaryJobs = &spySummaryJobs{}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/reprocess", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reprocess = %d, want 202; body = %s", rec.Code, rec.Body.String())
	}

	var rev int
	if err := db.QueryRow(`SELECT embed_rev FROM videos WHERE id='v1'`).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	if rev != 0 {
		t.Errorf("embed_rev = %d after reprocess, want 0 so the index is rebuilt", rev)
	}
}

// A chapter chunk contains the transcript of its own span, so the same sentence
// is indexed twice. Both copies match the same query; showing both would fill a
// video's four slots with two renderings of one moment instead of four
// different places the topic came up.
func TestSearchCollapsesDuplicateMoments(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "long talk"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Two chunks covering the same second, as the transcript window and the
	// chapter over it would, plus one genuinely elsewhere.
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: "electrolytes matter here", Kind: rag.KindTranscript, StartSeconds: 100},
		{Ordinal: 1, Text: "Chapter: Minerals\nelectrolytes matter here", Kind: rag.KindChapter, StartSeconds: 105},
		{Ordinal: 2, Text: "electrolytes again much later", Kind: rag.KindTranscript, StartSeconds: 900},
	})
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/search?q=electrolytes", nil)
	var resp struct {
		Results []struct {
			Matches []struct {
				StartSeconds int `json:"start_seconds"`
			} `json:"matches"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("want 1 video, got %d", len(resp.Results))
	}
	got := resp.Results[0].Matches
	if len(got) != 2 {
		t.Fatalf("want 2 distinct moments, got %d: %+v", len(got), got)
	}
	if abs(got[0].StartSeconds-got[1].StartSeconds) < minMomentGapSeconds {
		t.Errorf("moments %d and %d are the same moment twice", got[0].StartSeconds, got[1].StartSeconds)
	}
}

// A summary hit describes the whole video rather than a point in it, so it must
// survive alongside a transcript moment even though it carries no timestamp.
func TestSearchKeepsSummaryAlongsideAnEarlyMoment(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "talk"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: "electrolytes right at the start", Kind: rag.KindTranscript, StartSeconds: 5},
		{Ordinal: 1, Text: "a summary mentioning electrolytes", Kind: rag.KindSummary, StartSeconds: 0},
	})
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/search?q=electrolytes", nil)
	body := rec.Body.String()
	if !strings.Contains(body, `"kind":"summary"`) {
		t.Errorf("summary hit was suppressed by a nearby transcript moment: %s", body)
	}
	if !strings.Contains(body, `"kind":"transcript"`) {
		t.Errorf("transcript hit missing: %s", body)
	}
}

// TestSearchAskHonoursTheDistanceBound is the endpoint-level half of the fix
// this feature exists for. rag.TestRetrieveWithinBoundsDistance covers the
// bound in the store; nothing covered it through the handler, and the field
// that carries it (Deps.SearchMaxDistance) now resolves its zero value to the
// default rather than to "disabled", so an assembly that forgets the field gets
// the safe reading; only an explicit negative opts out.
func TestSearchAskHonoursTheDistanceBound(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	unit := func(i int) []float32 {
		v := make([]float32, 1536)
		v[i] = 1
		return v
	}
	for _, v := range []struct {
		id, text string
		vec      []float32
	}{
		{"near", "aligned with the query vector", unit(0)},
		{"far", "unidentified aerial phenomena", unit(5)},
	} {
		if err := deps.Videos.Upsert(videos.Video{ID: v.id, URL: "u", Title: v.id}); err != nil {
			t.Fatalf("seed %s: %v", v.id, err)
		}
		if err := ragStore.ReplaceVideoChunks(context.Background(), v.id,
			rag.IndexMeta{Model: "test-model", Dim: 1536, Rev: rag.ChunkRecipeRev},
			[]rag.ChunkRow{{Ordinal: 0, Text: v.text, Kind: rag.KindTranscript, StartSeconds: 1}},
			[][]float32{v.vec}); err != nil {
			t.Fatalf("seed chunks %s: %v", v.id, err)
		}
	}
	// A query word neither chunk contains, so the keyword lane abstains and the
	// response is the semantic lane alone.
	deps.Embedder = &fakeEmbedder{vec: unit(0)}
	deps.SearchMaxDistance = rag.DefaultMaxDistance
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	body := doReq(t, h, cookie, http.MethodGet, "/api/search?q=zzqqxx&mode=ask", nil).Body.String()
	if !strings.Contains(body, `"id":"near"`) {
		t.Errorf("the on-topic video was dropped: %s", body)
	}
	if strings.Contains(body, `"id":"far"`) {
		t.Errorf("an orthogonal chunk survived the bound: %s", body)
	}

	// A NEGATIVE value is the explicit opt-out, and it has to still work — this
	// half also proves the assertion above is not vacuous, since it shows the
	// far chunk IS reachable when nothing bounds it.
	deps.SearchMaxDistance = -1
	body = doReq(t, New(deps), cookie, http.MethodGet, "/api/search?q=zzqqxx&mode=ask", nil).Body.String()
	if !strings.Contains(body, `"id":"far"`) {
		t.Errorf("a negative maxDistance should disable the cutoff: %s", body)
	}
}

// The whole point of inverting the sentinel: a Deps assembly that never mentions
// SearchMaxDistance must get the bound, not lose it. This is the exact mistake
// searchTestDepsWithStores made for the life of #238.
func TestSearchUnsetDistanceBoundStillBounds(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	unit := func(i int) []float32 {
		v := make([]float32, 1536)
		v[i] = 1
		return v
	}
	for _, v := range []struct {
		id  string
		vec []float32
	}{{"near", unit(0)}, {"far", unit(5)}} {
		if err := deps.Videos.Upsert(videos.Video{ID: v.id, URL: "u", Title: v.id}); err != nil {
			t.Fatalf("seed %s: %v", v.id, err)
		}
		if err := ragStore.ReplaceVideoChunks(context.Background(), v.id,
			rag.IndexMeta{Model: "test-model", Dim: 1536, Rev: rag.ChunkRecipeRev},
			[]rag.ChunkRow{{Ordinal: 0, Text: "unrelated wording", Kind: rag.KindTranscript, StartSeconds: 1}},
			[][]float32{v.vec}); err != nil {
			t.Fatalf("seed chunks %s: %v", v.id, err)
		}
	}
	// Deliberately NOT setting deps.SearchMaxDistance.
	deps.Embedder = &fakeEmbedder{vec: unit(0)}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// A query word neither chunk contains, so only the vector lane can answer.
	rec := doReq(t, h, cookie, http.MethodGet, "/api/search?q=zqxjkv&mode=ask", nil)
	body := rec.Body.String()
	if strings.Contains(body, `"id":"far"`) {
		t.Errorf("the orthogonal chunk survived an unset bound: %s", body)
	}
	if !strings.Contains(body, `"id":"near"`) {
		t.Errorf("the aligned chunk should still be returned: %s", body)
	}
}

// End to end through the endpoint: a natural question whose strict tiers match
// nothing must not let the OR recall floor bury a semantically close passage.
// This is the query shape the whole feature exists for.
func TestSearchAskOrFloorDoesNotBuryTheSemanticHit(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	unit := func(i int) []float32 {
		v := make([]float32, 1536)
		v[i] = 1
		return v
	}
	// "shares" contains the question's function-ish content word "sport" and
	// nothing else about it; "ontopic" contains none of the question's words but
	// sits right next to the query vector.
	for _, v := range []struct {
		id, text string
		vec      []float32
	}{
		{"shares", "a sport documentary about nothing in particular", unit(7)},
		{"ontopic", "sodium replacement during long efforts", unit(0)},
	} {
		if err := deps.Videos.Upsert(videos.Video{ID: v.id, URL: "u", Title: v.id}); err != nil {
			t.Fatalf("seed %s: %v", v.id, err)
		}
		if err := ragStore.ReplaceVideoChunks(context.Background(), v.id,
			rag.IndexMeta{Model: "test-model", Dim: 1536, Rev: rag.ChunkRecipeRev},
			[]rag.ChunkRow{{Ordinal: 0, Text: v.text, Kind: rag.KindTranscript, StartSeconds: 1}},
			[][]float32{v.vec}); err != nil {
			t.Fatalf("seed chunks %s: %v", v.id, err)
		}
	}
	deps.Embedder = &fakeEmbedder{vec: unit(0)}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// Strict tiers cannot match — no chunk holds every word — so the ladder
	// relaxes to the OR floor, which "sport" satisfies. The second query has no
	// stopwords at all, so its ladder is only TWO rungs and the OR floor is the
	// SECOND one: weighing a rung by its position in the ladder would hand it
	// the content-tier weight and bury the semantic hit again.
	for name, q := range map[string]string{
		"question with stopwords": "did+someone+talk+about+electrolytes+in+endurance+sport",
		"bare terms, no stopword": "electrolytes+endurance+sport",
	} {
		t.Run(name, func(t *testing.T) {
			rec := doReq(t, h, cookie, http.MethodGet, "/api/search?q="+q+"&mode=ask", nil)
			var resp struct {
				Results []struct {
					Video struct {
						ID string `json:"id"`
					} `json:"video"`
				} `json:"results"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v, body = %s", err, rec.Body.String())
			}
			if len(resp.Results) == 0 {
				t.Fatalf("expected results: %s", rec.Body.String())
			}
			if resp.Results[0].Video.ID != "ontopic" {
				t.Errorf("results[0] = %q, want the semantically close video ahead of the "+
					"chunk that merely shares a word: %s", resp.Results[0].Video.ID, rec.Body.String())
			}
		})
	}
}

// angled builds a unit vector at the given cosine similarity to unit(0), so a
// test can seed a chunk at a chosen L2 distance: L2 = sqrt(2 - 2*cos).
func angled(cos float64) []float32 {
	v := make([]float32, 1536)
	v[0] = float32(cos)
	v[1] = float32(math.Sqrt(1 - cos*cos))
	return v
}

// TestSearchAskStopsAtTheSpread is the "unrelated videos in Matches" report.
// All three chunks clear the absolute bound — in a library about one broad
// subject they always do — so the absolute bound alone cannot tell the second
// genuinely relevant chunk from the third merely-nearest one. Only the
// per-query spread can, and without it the response pads out to k with whatever
// was least distant among the irrelevant.
func TestSearchAskStopsAtTheSpread(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	for _, v := range []struct {
		id  string
		cos float64
	}{
		{"onpoint", 1.0},   // distance 0
		{"related", 0.995}, // distance ~0.10, inside the spread
		{"nearest", 0.875}, // distance ~0.50: past the spread, well inside the bound
	} {
		if err := deps.Videos.Upsert(videos.Video{ID: v.id, URL: "u", Title: v.id}); err != nil {
			t.Fatalf("seed %s: %v", v.id, err)
		}
		if err := ragStore.ReplaceVideoChunks(context.Background(), v.id,
			rag.IndexMeta{Model: "test-model", Dim: 1536, Rev: rag.ChunkRecipeRev},
			[]rag.ChunkRow{{Ordinal: 0, Text: "a passage", Kind: rag.KindTranscript, StartSeconds: 1}},
			[][]float32{angled(v.cos)}); err != nil {
			t.Fatalf("seed chunks %s: %v", v.id, err)
		}
	}
	// A word no chunk contains, so this is the semantic lane alone.
	deps.Embedder = &fakeEmbedder{vec: angled(1.0)}
	deps.SearchMaxDistance = rag.DefaultMaxDistance
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	body := doReq(t, h, cookie, http.MethodGet, "/api/search?q=zzqqxx&mode=ask", nil).Body.String()
	for _, want := range []string{`"id":"onpoint"`, `"id":"related"`} {
		if !strings.Contains(body, want) {
			t.Errorf("%s was dropped — the spread cut into the evidence: %s", want, body)
		}
	}
	if strings.Contains(body, `"id":"nearest"`) {
		t.Errorf("a merely-nearest chunk padded the response: %s", body)
	}
}

// The ladder shapes that have to be tested together: a natural question, whose
// terms are mostly stopwords, and bare terms, which skip rungs entirely. A past
// weighting bug survived CI because only the first shape was covered.
func TestSearchAskFindsTheTopicInBothQueryShapes(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", Title: "on transients"}); err != nil {
		t.Fatal(err)
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: "the transients vanish between plates", StartSeconds: 12},
	})
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	for _, q := range []string{"tell+me+more+about+transients", "transients"} {
		body := doReq(t, h, cookie, http.MethodGet, "/api/search?q="+q+"&mode=ask", nil).Body.String()
		if !strings.Contains(body, `"id":"v1"`) {
			t.Errorf("query %q missed the only matching video: %s", q, body)
		}
	}
}

// The reported bug: "what are transients and what material do we have on them?"
// returned two videos in Ask where the plain keyword search returned six.
//
// The ladder stopped at the first rung returning ANY row. Every function word
// went into the strict rung, which matched nothing; the next rung — "transients"
// AND "material" — matched exactly one chunk in one video, and the ladder took
// that for an answer. The floor that finds the rest never ran.
func TestSearchAskDoesNotStopOnAOneVideoRung(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	// One video says both content words, so the content-ANDed rung matches it and
	// nothing else. Nine more say only one of them — the videos a reader searching
	// "transients" would see, and the ones Ask was dropping.
	if err := deps.Videos.Upsert(videos.Video{ID: "both", URL: "u", Title: "both words"}); err != nil {
		t.Fatal(err)
	}
	seedChunks(t, ragStore, "both", []rag.ChunkRow{
		{Ordinal: 0, Text: "the material here concerns transients", StartSeconds: 10},
	})
	for i := range 9 {
		id := fmt.Sprintf("only%d", i)
		if err := deps.Videos.Upsert(videos.Video{ID: id, URL: "u", Title: id}); err != nil {
			t.Fatal(err)
		}
		seedChunks(t, ragStore, id, []rag.ChunkRow{
			{Ordinal: 0, Text: "these transients decay quickly", StartSeconds: 30},
		})
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	body := doReq(t, h, cookie, http.MethodGet,
		"/api/search?q=what+are+transients+and+what+material+do+we+have+on+them&mode=ask", nil).Body.String()

	if !strings.Contains(body, `"id":"both"`) {
		t.Errorf("the precise match was dropped: %s", body)
	}
	found := 0
	for i := range 9 {
		if strings.Contains(body, fmt.Sprintf(`"id":"only%d"`, i)) {
			found++
		}
	}
	// Before this fix exactly one video came back. The floor has to contribute the
	// rest rather than being skipped.
	if found < 5 {
		t.Errorf("only %d of 9 single-word videos came back; the ladder stopped early: %s", found, body)
	}
}

func TestDistinctVideosCountsVideosNotHits(t *testing.T) {
	hits := []rag.Hit{
		{VideoID: "a", Ordinal: 0}, {VideoID: "a", Ordinal: 1},
		{VideoID: "b", Ordinal: 0}, {VideoID: "a", Ordinal: 2},
	}
	if got := distinctVideos(hits); got != 2 {
		t.Errorf("distinctVideos = %d, want 2", got)
	}
	if got := distinctVideos(nil); got != 0 {
		t.Errorf("distinctVideos(nil) = %d, want 0", got)
	}
}

// The keyword ladder is up to four separate sqlite queries reported as one
// ftsMs, and they are not comparable: the floor rung matches any ONE content
// word and is by far the widest query the ladder can build. When the ladder
// costs seconds, "which rung" is the entire question, and a single total cannot
// answer it.
func TestAskLanesTimesEachRungSeparately(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "Cramp"}); err != nil {
		t.Fatal(err)
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: "the electrolytes you replace matter", StartSeconds: 10},
	})
	testee := &server{rag: ragStore, videos: deps.Videos}
	r := httptest.NewRequest(http.MethodGet, "/api/search/answer?q=electrolytes", nil)

	_, diag := testee.askLanes(r, "electrolytes", "", rag.Filter{}, nil)

	if len(diag.rungs) == 0 {
		t.Fatal("no rung was recorded at all")
	}
	// Shape is w<weight>=<hits>h/<videos>v/<ms>ms — the ms is the point.
	for _, rung := range diag.rungs {
		if !strings.HasSuffix(rung, "ms") {
			t.Errorf("rung %q carries no duration; the ladder's cost cannot be attributed", rung)
		}
	}
}

// A rung that queried sqlite and matched nothing used to leave no trace: the
// loop skipped the append. An empty rung costs the same scan as a full one, so
// on a ladder measured in seconds those are exactly the ones worth seeing.
func TestAskLanesRecordsARungThatMatchedNothing(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "Cramp"}); err != nil {
		t.Fatal(err)
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: "the electrolytes you replace matter", StartSeconds: 10},
	})
	testee := &server{rag: ragStore, videos: deps.Videos}
	// Two content words where only one is in the library: the strict rung ANDs
	// both and matches nothing, the looser rungs find the one that is there.
	r := httptest.NewRequest(http.MethodGet, "/api/search/answer?q=electrolytes+zirconium", nil)

	_, diag := testee.askLanes(r, "electrolytes zirconium", "", rag.Filter{}, nil)

	var empty int
	for _, rung := range diag.rungs {
		if strings.Contains(rung, "=0h/0v/") {
			empty++
		}
	}
	if empty == 0 {
		t.Errorf("rungs = %v, want the rung that ran and matched nothing recorded too", diag.rungs)
	}
}

// A rung that ERRORS is the single one most worth seeing: ftsMs is measured
// from the top of the ladder, so it counts every second that rung burned before
// failing. Leaving it out printed a total no rung could account for — and the
// comment above the loop claims every rung that ran is recorded, which was
// simply false for this path.
func TestAskLanesRecordsARungThatErrored(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", Title: "Cramp"}); err != nil {
		t.Fatal(err)
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: "the electrolytes you replace matter", StartSeconds: 10},
	})
	testee := &server{
		rag:    &failingRag{real: ragStore, searchFTS: errors.New("fts index is corrupt")},
		videos: deps.Videos,
	}
	r := httptest.NewRequest(http.MethodGet, "/api/search/answer?q=electrolytes", nil)

	_, diag := testee.askLanes(r, "electrolytes", "", rag.Filter{}, nil)

	if len(diag.rungs) != 1 {
		t.Fatalf("rungs = %v, want the errored rung recorded and the ladder abandoned", diag.rungs)
	}
	if !strings.Contains(diag.rungs[0], "=err/") {
		t.Errorf("rung %q does not say it errored", diag.rungs[0])
	}
	if !strings.HasSuffix(diag.rungs[0], "ms") {
		t.Errorf("rung %q carries no duration, which is the whole point for an error", diag.rungs[0])
	}
}
