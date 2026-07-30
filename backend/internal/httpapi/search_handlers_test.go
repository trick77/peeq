package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
		MediaPath:       "/media/v1.mp4",
		SubtitleRelPath: "v1.en.vtt",
	}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
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
		MediaPath:       "/media/v1.mp4",
		SubtitleRelPath: "v1.en.vtt",
	}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
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
				MediaPath:       "/media/v1.mp4",
				SubtitleRelPath: "v1.en.vtt",
			}); err != nil {
				t.Fatalf("seed downloaded: %v", err)
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
		MediaPath:       "/media/v1.mp4",
		SubtitleRelPath: "v1.en.vtt",
	}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
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
		MediaPath:       "/media/v1.mp4",
		SubtitleRelPath: "v1.en.vtt",
	}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
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
		MediaPath: "/media/v1.mp4", SubtitleRelPath: "v1.en.vtt",
	}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
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
		MediaPath:       "/media/v1.mp4",
		SubtitleRelPath: "v1.en.vtt",
	}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
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
		MediaPath:       "/media/v1.mp4",
		SubtitleRelPath: "v1.en.vtt",
	}); err != nil {
		t.Fatalf("seed downloaded: %v", err)
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
		`UPDATE videos SET media_path='m.mp4', subtitle_path='s.vtt' WHERE id='v1'`); err != nil {
		t.Fatal(err)
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
