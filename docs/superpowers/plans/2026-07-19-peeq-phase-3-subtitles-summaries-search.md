# Peeq Phase 3 — Subtitles, Summaries & Search Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every downloaded video an audio-language subtitle track, an AI summary + timestamped chapters + key-points, and semantic transcript search — surfaced in the player and a global search view.

**Architecture:** yt-dlp gains subtitle flags at the existing cookie-gated download choke point. A new single-concurrency ticker-worker (twin of the download worker) parses each VTT, runs map-reduce MiMo summarization into three artifacts, and embeds transcript chunks into a `sqlite-vec` `vec0` table. A lean MiMo chat client and a ported loom `rag/` stack (chunk → embed → KNN) back the AI. The player fills its three stubbed panels + captions; a new `/api/search` does vec0 KNN retrieval. AI is required at boot.

**Tech Stack:** Go 1.25 · `net/http` · `ncruces/go-sqlite3` v0.23.3 + `sqlite-vec-go-bindings/ncruces` v0.1.7-alpha.2 · OpenAI-compatible MiMo chat + embeddings (fakes in CI) · React 19 · Vite · Tailwind v4 · Vitest · yt-dlp (fake in CI).

## Prerequisite (separate PR, merged before this plan starts)

**`chore/env-prefix-backend`** — a mechanical rename of **every** existing env var `PEEQ_*` → `BACKEND_*` (and reserve `UI_*` for any future frontend-exposed var; none exist today), so a future app rename never touches env config (matches loom's existing `BACKEND_EMBED_*`). Touches `backend/internal/config/config.go`, `.env.example`, `compose.yaml`, `compose.dev.yaml`, `hack/dev.sh`, `.github/workflows/*.yaml`, `README.md`, and any test that sets env. Merge to `master`, then rebase this branch onto it. Every env var in this plan is already written in the `BACKEND_` form. (Non-env peeq-branded identifiers — DB file, cookie, image, binary, wordmark — are intentionally left as-is; this PR is env-prefix only.)

## Global Constraints

- Module `github.com/trick77/peeq`; entrypoint `backend/cmd/peeq`. Env prefix `BACKEND_` (all vars, post-prerequisite-PR).
- `CGO_ENABLED=0`; pins **do not bump**: `ncruces/go-sqlite3` v0.23.3, `sqlite-vec-go-bindings/ncruces` v0.1.7-alpha.2.
- Backend tests must pass with `-race`; `gofmt` clean; English comments; conventional commits; `.yaml` (not `.yml`).
- **Migrations are SQUASHED into `backend/internal/store/migrations/0001_init.sql`** for Phase 3 (user override of append-only). No new numbered migration. Dev DBs are recreated (`rm ./data/peeq.db*`).
- **Hard invariants:** no YouTube/subtitle call without a valid cookie; 20s throttle floor + jitter — both already live via the single `ytdlp.Runner` exec choke point; subtitles ride the existing download invocation (no new call path). Media path access via `media.SafeMediaPath` only (reject `..`/absolute/symlink-escape).
- **AI is integral:** `BACKEND_MIMO_BASE_URL`, `BACKEND_EMBED_BASE_URL`, `BACKEND_EMBED_MODEL` are required — `config.Load` returns an error if any is empty (fatal, like `BACKEND_SESSION_SECRET`).
- MiMo chat: model hardcoded `mimo-v2.5-pro`, `reasoning_effort=high` on every call.
- **Tests use fakes only:** `httptest` servers for MiMo/embeddings; the existing `backend/internal/ytdlp/testdata/fake-ytdlp.sh` for yt-dlp. Never a real LLM, real embeddings endpoint, or the real yt-dlp binary.
- Branch: `feat/phase-3-subtitles-summaries-search` (already created). PR to `master`.

## File Structure

**New packages/files**
- `backend/internal/llm/client.go` (+ `client_test.go`) — MiMo OpenAI-compatible chat client.
- `backend/internal/rag/chunk.go` (+ `chunk_test.go`) — text chunking (copied from loom).
- `backend/internal/rag/embed.go` (+ `embed_test.go`) — OpenAI-compatible embeddings client.
- `backend/internal/rag/store.go` (+ `store_test.go`) — `transcript_chunks` + `vec_chunks` writes/deletes/KNN + dim reconcile.
- `backend/internal/subtitles/vtt.go` (+ `vtt_test.go`) — VTT → transcript + cue index, auto-caption dedup.
- `backend/internal/summaryjobs/store.go` (+ `store_test.go`) — the summary job queue (mirrors `jobs`).
- `backend/internal/summarize/summarizer.go` (+ `summarizer_test.go`) — map-reduce → 3 artifacts.
- `backend/internal/summarize/worker.go` (+ `worker_test.go`) — the summarization/embedding ticker-worker.
- `backend/internal/httpapi/search_handlers.go` (+ `search_handlers_test.go`) — `GET /api/search`.
- `backend/internal/httpapi/subtitles_handlers.go` (+ test) — `GET /api/videos/{id}/subtitles`.
- `ui/src/api/search.ts` (+ test), `ui/src/views/Search.tsx` (+ test).

**Modified files**
- `backend/internal/store/migrations/0001_init.sql` — new columns + `transcript_chunks`, `vec_chunks`, `summary_jobs`.
- `backend/internal/config/config.go` — new required env vars.
- `backend/internal/videos/store.go` — new columns on `Video` + setters.
- `backend/internal/ytdlp/{download.go,meta.go}` — subtitle flags + `language`/chapters capture.
- `backend/internal/download/worker.go` — resolve sub-lang, persist subtitle/chapters, enqueue summary job on success.
- `backend/internal/httpapi/{server.go,videos_handlers.go}` — new routes + video JSON fields + resummarize.
- `backend/cmd/peeq/main.go` — construct clients/stores/worker, boot dim-guard, start goroutine, wire routes.
- `ui/src/api/{types.ts,videos.ts,index.ts}`, `ui/src/views/Player.tsx`, `ui/src/shell/Rail.tsx`, `ui/src/App.tsx`, `ui/src/index.css`.
- `.env.example`, `README.md`, `AGENTS.md`.

---

## Task 1: Schema — new columns and tables (squashed into 0001_init.sql)

**Files:**
- Modify: `backend/internal/store/migrations/0001_init.sql`
- Test: `backend/internal/store/migrate_schema_test.go` (create)

**Interfaces:**
- Produces: `videos` columns `audio_language, subtitle_path, summary, chapters, key_points, summary_status, summary_error, embed_model, embed_dim`; tables `transcript_chunks`, `vec_chunks` (`float[1536]`), `summary_jobs`.

- [ ] **Step 1: Write the failing test**

```go
package store

import (
	"path/filepath"
	"testing"
)

func TestSchemaHasPhase3Objects(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	// New videos columns exist.
	for _, col := range []string{"audio_language", "subtitle_path", "summary", "chapters", "key_points", "summary_status", "summary_error", "embed_model", "embed_dim"} {
		var cnt int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('videos') WHERE name = ?`, col,
		).Scan(&cnt); err != nil || cnt != 1 {
			t.Fatalf("videos.%s missing (cnt=%d err=%v)", col, cnt, err)
		}
	}
	// New tables exist and vec_chunks accepts a 1536-dim vector.
	if _, err := db.Exec(`INSERT INTO summary_jobs (video_id) VALUES ('x')`); err == nil {
		t.Fatal("expected FK failure inserting summary_job for missing video")
	}
	if _, err := db.Exec(`SELECT rowid FROM vec_chunks LIMIT 0`); err != nil {
		t.Fatalf("vec_chunks not queryable: %v", err)
	}
	if _, err := db.Exec(`SELECT ordinal, start_seconds FROM transcript_chunks LIMIT 0`); err != nil {
		t.Fatalf("transcript_chunks not queryable: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/store/ -run TestSchemaHasPhase3Objects -v`
Expected: FAIL (columns/tables absent).

- [ ] **Step 3: Edit `0001_init.sql`** — append the new `videos` columns to the `CREATE TABLE videos (...)` block (before the closing `)`), after `downloaded_at`:

```sql
    audio_language           TEXT NOT NULL DEFAULT '',
    subtitle_path            TEXT NOT NULL DEFAULT '',
    summary                  TEXT NOT NULL DEFAULT '',
    chapters                 TEXT NOT NULL DEFAULT '[]',
    key_points               TEXT NOT NULL DEFAULT '[]',
    summary_status           TEXT NOT NULL DEFAULT 'pending' CHECK (summary_status IN ('pending','running','done','error','no_transcript')),
    summary_error            TEXT NOT NULL DEFAULT '',
    embed_model              TEXT NOT NULL DEFAULT '',
    embed_dim                INTEGER NOT NULL DEFAULT 0
```

(Add a comma after `downloaded_at` so the block stays valid.) Then append at the end of the file:

```sql
-- transcript_chunks: one embedded transcript window per video. id IS the rowid,
-- so it bridges directly to vec_chunks.rowid (vec0 requires an INTEGER rowid).
CREATE TABLE transcript_chunks (
    id            INTEGER PRIMARY KEY,
    video_id      TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    ordinal       INTEGER NOT NULL,
    text          TEXT NOT NULL,
    start_seconds INTEGER NOT NULL DEFAULT 0,
    token_count   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_transcript_chunks_video ON transcript_chunks(video_id, ordinal);

-- vec_chunks: embeddings, keyed 1:1 to transcript_chunks.id via rowid. Peeq is
-- single-user so there are no partition/metadata columns. The dimension is fixed
-- at DDL time; a model/dim change invalidates the whole table (see rag.Store
-- reconcile). vec0 cannot appear in triggers/FK cascades, so the store deletes
-- matching rows in the same transaction as transcript_chunks deletes.
CREATE VIRTUAL TABLE vec_chunks USING vec0(
    embedding float[1536]
);

-- summary_jobs: offline summarization+embedding queue (twin of download_jobs).
CREATE TABLE summary_jobs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    video_id     TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    state        TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','running','done','failed')),
    attempts     INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    last_error   TEXT NOT NULL DEFAULT '',
    enqueued_at  TEXT NOT NULL DEFAULT (datetime('now')),
    started_at   TEXT,
    finished_at  TEXT
);
CREATE INDEX idx_summary_jobs_state ON summary_jobs(state);
CREATE INDEX idx_summary_jobs_video ON summary_jobs(video_id);
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./internal/store/ -run TestSchemaHasPhase3Objects -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/store/migrations/0001_init.sql backend/internal/store/migrate_schema_test.go
git commit -m "feat(store): phase-3 schema — subtitle/summary columns, transcript_chunks, vec_chunks, summary_jobs"
```

---

## Task 2: Config — required MiMo + embeddings env vars

**Files:**
- Modify: `backend/internal/config/config.go`
- Test: `backend/internal/config/config_test.go` (add cases; create if absent)

**Interfaces:**
- Produces: `Config` fields `MimoBaseURL, MimoAPIKey, EmbedBaseURL, EmbedAPIKey, EmbedModel string`, `EmbedDim int`, `DefaultSubLang string`. `Load` errors when any required one is empty.

- [ ] **Step 1: Write the failing test**

```go
func TestLoadRequiresAIEndpoints(t *testing.T) {
	base := map[string]string{
		"BACKEND_SESSION_SECRET": "s", "BACKEND_AUTH_MODE": "dev", "BACKEND_ADDR": "127.0.0.1:8080",
		"BACKEND_MIMO_BASE_URL": "http://mimo", "BACKEND_EMBED_BASE_URL": "http://emb", "BACKEND_EMBED_MODEL": "e5",
	}
	setEnv := func(m map[string]string) {
		os.Clearenv()
		for k, v := range m {
			os.Setenv(k, v)
		}
	}
	setEnv(base)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if cfg.MimoBaseURL != "http://mimo" || cfg.EmbedModel != "e5" || cfg.EmbedDim != 1536 || cfg.DefaultSubLang != "en" {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
	for _, drop := range []string{"BACKEND_MIMO_BASE_URL", "BACKEND_EMBED_BASE_URL", "BACKEND_EMBED_MODEL"} {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		delete(m, drop)
		setEnv(m)
		if _, err := Load(); err == nil {
			t.Fatalf("expected error when %s missing", drop)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/config/ -run TestLoadRequiresAIEndpoints -v`
Expected: FAIL (fields absent).

- [ ] **Step 3: Implement.** Add fields to `Config`:

```go
	MimoBaseURL    string
	MimoAPIKey     string
	EmbedBaseURL   string
	EmbedAPIKey    string
	EmbedModel     string
	EmbedDim       int
	DefaultSubLang string
```

In `Load`, after the existing `cfg := Config{...}` literal, populate them (add an `import "strconv"`):

```go
	cfg.MimoBaseURL = env("BACKEND_MIMO_BASE_URL", "")
	cfg.MimoAPIKey = env("BACKEND_MIMO_API_KEY", "")
	cfg.EmbedBaseURL = env("BACKEND_EMBED_BASE_URL", "")
	cfg.EmbedAPIKey = env("BACKEND_EMBED_API_KEY", "")
	cfg.EmbedModel = env("BACKEND_EMBED_MODEL", "")
	cfg.DefaultSubLang = env("BACKEND_DEFAULT_SUB_LANG", "en")
	cfg.EmbedDim = 1536
	if v := env("BACKEND_EMBED_DIM", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("BACKEND_EMBED_DIM must be a positive integer")
		}
		cfg.EmbedDim = n
	}
```

Add validation just before the `switch cfg.AuthMode` block (so it runs for every auth mode):

```go
	if cfg.MimoBaseURL == "" {
		return Config{}, fmt.Errorf("BACKEND_MIMO_BASE_URL is required")
	}
	if cfg.EmbedBaseURL == "" {
		return Config{}, fmt.Errorf("BACKEND_EMBED_BASE_URL is required")
	}
	if cfg.EmbedModel == "" {
		return Config{}, fmt.Errorf("BACKEND_EMBED_MODEL is required")
	}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/config/ -v`
Expected: PASS. (If pre-existing config tests set env, ensure they also set the three new required vars — update them.)

- [ ] **Step 5: Commit**

```bash
git add backend/internal/config/
git commit -m "feat(config): require MiMo + embeddings endpoints; add EmbedDim + DefaultSubLang"
```

---

## Task 3: MiMo chat client

**Files:**
- Create: `backend/internal/llm/client.go`, `backend/internal/llm/client_test.go`

**Interfaces:**
- Produces: `llm.Config{BaseURL, APIKey string}`; `llm.NewClient(cfg Config, hc *http.Client) *Client`; `llm.Message{Role, Content string}`; `func (c *Client) Complete(ctx context.Context, messages []Message) (string, error)`. Sends `model="mimo-v2.5-pro"`, `reasoning_effort="high"`, `stream=false`; returns the first choice's `message.content`.

- [ ] **Step 1: Write the failing test**

```go
package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompleteSendsModelAndEffortAndReturnsContent(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("missing auth header")
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		io.WriteString(w, `{"choices":[{"message":{"content":"hello world"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	out, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello world" {
		t.Fatalf("content = %q", out)
	}
	if gotBody["model"] != "mimo-v2.5-pro" {
		t.Fatalf("model = %v", gotBody["model"])
	}
	if gotBody["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v", gotBody["reasoning_effort"])
	}
	if gotBody["stream"] != false {
		t.Fatalf("stream = %v", gotBody["stream"])
	}
	_ = strings.TrimSpace
}

func TestCompleteErrorsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewClient(Config{BaseURL: srv.URL}, srv.Client())
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/llm/ -v`
Expected: FAIL (package/type absent).

- [ ] **Step 3: Implement `client.go`**

```go
// Package llm is peeq's lean OpenAI-compatible chat client for MiMo. It targets
// mimo-v2.5-pro at reasoning_effort=high on every call (an offline summarization
// job, so latency is free and quality is the priority). Modeled on loom's
// llm/client.go, minus loom's tool/vision/streaming machinery.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	model           = "mimo-v2.5-pro"
	reasoningEffort = "high"
	defaultTimeout  = 5 * time.Minute
	maxErrorBody    = 4 << 10
)

// Config configures the chat client. BaseURL is the OpenAI-compatible root
// (the client appends /chat/completions). APIKey is optional.
type Config struct {
	BaseURL string
	APIKey  string
}

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client calls an OpenAI-compatible /chat/completions endpoint.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient builds a Client. hc is optional (a 5-minute-timeout client is used
// when nil).
func NewClient(cfg Config, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{baseURL: strings.TrimRight(cfg.BaseURL, "/"), apiKey: cfg.APIKey, http: hc}
}

type chatRequest struct {
	Model           string    `json:"model"`
	Messages        []Message `json:"messages"`
	ReasoningEffort string    `json:"reasoning_effort"`
	Stream          bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

// Complete runs a single non-streaming chat completion and returns the first
// choice's content.
func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
	body, err := json.Marshal(chatRequest{Model: model, Messages: messages, ReasoningEffort: reasoningEffort, Stream: false})
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("chat request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return "", fmt.Errorf("chat failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("chat response had no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/llm/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/llm/
git commit -m "feat(llm): lean MiMo chat client (mimo-v2.5-pro, reasoning_effort=high)"
```

---

## Task 4: rag — chunk + embed client (ported from loom, single-user)

**Files:**
- Create: `backend/internal/rag/chunk.go`, `backend/internal/rag/chunk_test.go`, `backend/internal/rag/embed.go`, `backend/internal/rag/embed_test.go`

**Interfaces:**
- Produces: `rag.TextChunk{Ordinal int; Text string; TokenCount int}`; `rag.Chunk(text string, opts ChunkOptions) []TextChunk`; `rag.DefaultChunkOptions() ChunkOptions`; `rag.EmbedConfig{BaseURL, APIKey, Model string}`; `rag.NewEmbedClient(cfg, hc) *EmbedClient`; `func (c *EmbedClient) Embed(ctx, inputs []string) ([][]float32, error)`.

- [ ] **Step 1: Write the failing test**

```go
package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChunkProducesOrderedOverlappingChunks(t *testing.T) {
	words := make([]byte, 0)
	for i := 0; i < 4000; i++ {
		words = append(words, 'a', ' ')
	}
	chunks := Chunk(string(words), DefaultChunkOptions())
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if c.Ordinal != i {
			t.Fatalf("ordinal %d != %d", c.Ordinal, i)
		}
	}
	if len(Chunk("   ", DefaultChunkOptions())) != 0 {
		t.Fatal("whitespace-only should yield no chunks")
	}
}

func TestEmbedReturnsVectorsInInputOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 1, "embedding": []float32{0.2, 0.2}},
				{"index": 0, "embedding": []float32{0.1, 0.1}},
			},
		})
	}))
	defer srv.Close()
	c := NewEmbedClient(EmbedConfig{BaseURL: srv.URL, Model: "e5"}, srv.Client())
	vecs, err := c.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || vecs[0][0] != 0.1 || vecs[1][0] != 0.2 {
		t.Fatalf("vectors misaligned: %v", vecs)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/rag/ -v`
Expected: FAIL (package absent).

- [ ] **Step 3: Implement.** Copy `../loom/backend/internal/rag/chunk.go` **verbatim** into `backend/internal/rag/chunk.go` (it is self-contained and single-user-agnostic — no changes needed). Create `backend/internal/rag/embed.go` by porting `../loom/backend/internal/rag/embed.go` with a **simpler public signature** — `Embed` returns `([][]float32, error)` (drop loom's `EmbedResult`/usage tracking, which peeq doesn't record):

```go
package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	defaultEmbedTimeout = 1 * time.Minute
	maxEmbedErrorBody   = 4 << 10
)

// EmbedConfig configures the OpenAI-compatible embedding client.
type EmbedConfig struct {
	BaseURL string
	APIKey  string
	Model   string
}

// EmbedClient generates embeddings via an OpenAI-compatible /embeddings endpoint.
type EmbedClient struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// NewEmbedClient builds an EmbedClient. hc is optional.
func NewEmbedClient(cfg EmbedConfig, hc *http.Client) *EmbedClient {
	if hc == nil {
		hc = &http.Client{Timeout: defaultEmbedTimeout}
	}
	return &EmbedClient{baseURL: strings.TrimRight(cfg.BaseURL, "/"), apiKey: cfg.APIKey, model: cfg.Model, http: hc}
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed returns one vector per input, aligned to input order. Empty input yields
// no vectors and no request.
func (c *EmbedClient) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Model: c.model, Input: inputs})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxEmbedErrorBody))
		return nil, fmt.Errorf("embedding failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var parsed embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}
	if len(parsed.Data) != len(inputs) {
		return nil, fmt.Errorf("embedding count mismatch: got %d, want %d", len(parsed.Data), len(inputs))
	}
	sort.Slice(parsed.Data, func(i, j int) bool { return parsed.Data[i].Index < parsed.Data[j].Index })
	out := make([][]float32, len(parsed.Data))
	for i, d := range parsed.Data {
		out[i] = d.Embedding
	}
	return out, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/rag/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/rag/chunk.go backend/internal/rag/chunk_test.go backend/internal/rag/embed.go backend/internal/rag/embed_test.go
git commit -m "feat(rag): port loom chunking + embeddings client (single-user)"
```

---

## Task 5: rag — vec store (transcript_chunks + vec_chunks), KNN, dim reconcile

**Files:**
- Create: `backend/internal/rag/store.go`, `backend/internal/rag/store_test.go`

**Interfaces:**
- Consumes: schema from Task 1.
- Produces:
  - `rag.NewStore(db *sql.DB) *Store`
  - `rag.ChunkRow{Ordinal int; Text string; StartSeconds int; TokenCount int}`
  - `func (s *Store) ReplaceVideoChunks(ctx context.Context, videoID, model string, dim int, rows []ChunkRow, vectors [][]float32) error` — deletes any existing chunks+vecs for the video, inserts new ones, all in one tx (rowid bridge). Sets `videos.embed_model`/`embed_dim`.
  - `func (s *Store) DeleteVideoChunks(ctx context.Context, videoID string) error` — chunk + vec delete in one tx.
  - `rag.Hit{VideoID string; Ordinal int; Text string; StartSeconds int; Distance float64}`
  - `func (s *Store) Retrieve(ctx context.Context, queryEmbedding []float32, k int) ([]Hit, error)`
  - `func (s *Store) BuiltDim(ctx context.Context) (int, error)` — the vec_chunks embedding dimension from `pragma_table_info`/`vec0` introspection; used by the boot reconcile.

- [ ] **Step 1: Write the failing test**

```go
package rag

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

func newDB(t *testing.T) *sqlDB { /* helper below */ return nil }

func TestReplaceRetrieveAndDelete(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	// vec_chunks in 0001 is float[1536]; use matching-length vectors here.
	dim := 1536
	mk := func(v float32) []float32 {
		out := make([]float32, dim)
		out[0] = v
		return out
	}
	if _, err := db.Exec(`INSERT INTO videos (id, url) VALUES ('v1','u'),('v2','u')`); err != nil {
		t.Fatal(err)
	}
	s := NewStore(db)
	ctx := context.Background()
	rows := []ChunkRow{{Ordinal: 0, Text: "titanium frame", StartSeconds: 108, TokenCount: 2}}
	if err := s.ReplaceVideoChunks(ctx, "v1", "e5", dim, rows, [][]float32{mk(0.9)}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceVideoChunks(ctx, "v2", "e5", dim, []ChunkRow{{Ordinal: 0, Text: "battery life", StartSeconds: 303, TokenCount: 2}}, [][]float32{mk(0.1)}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Retrieve(ctx, mk(0.9), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].VideoID != "v1" || hits[0].StartSeconds != 108 {
		t.Fatalf("nearest neighbor wrong: %+v", hits)
	}
	// embed_model/dim recorded
	var m string
	var d int
	db.QueryRow(`SELECT embed_model, embed_dim FROM videos WHERE id='v1'`).Scan(&m, &d)
	if m != "e5" || d != dim {
		t.Fatalf("embed meta not recorded: %s %d", m, d)
	}
	// delete drops both tables
	if err := s.DeleteVideoChunks(ctx, "v1"); err != nil {
		t.Fatal(err)
	}
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM transcript_chunks WHERE video_id='v1'`).Scan(&cnt)
	if cnt != 0 {
		t.Fatalf("chunks not deleted: %d", cnt)
	}
}
```

(Delete the unused `newDB`/`sqlDB` stub — it's a leftover; the test uses `store.Open` directly.)

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/rag/ -run TestReplaceRetrieveAndDelete -v`
Expected: FAIL.

- [ ] **Step 3: Implement `store.go`.** Reuse loom's `vecLiteral` (copy the helper). Single-user: no scope columns.

```go
package rag

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// Store persists transcript chunks and their embeddings and retrieves nearest
// neighbors. Peeq is single-user, so there is no scope. vec0 forbids triggers/FK
// cascades, so every delete of transcript_chunks also deletes the matching
// vec_chunks rows in the SAME transaction.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// ChunkRow is one transcript window to index.
type ChunkRow struct {
	Ordinal      int
	Text         string
	StartSeconds int
	TokenCount   int
}

// Hit is one retrieved chunk with its cosine/L2 distance (smaller == closer).
type Hit struct {
	VideoID      string
	Ordinal      int
	Text         string
	StartSeconds int
	Distance     float64
}

// vecLiteral encodes a float32 vector as the JSON-array text sqlite-vec accepts.
func vecLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// deleteVideoTx removes a video's chunk + vec rows within tx. vec_chunks.rowid ==
// transcript_chunks.id, so the ids are gathered first, then both tables purged.
func deleteVideoTx(ctx context.Context, tx *sql.Tx, videoID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM transcript_chunks WHERE video_id = ?`, videoID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM vec_chunks WHERE rowid = ?`, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM transcript_chunks WHERE video_id = ?`, videoID); err != nil {
		return err
	}
	return nil
}

// ReplaceVideoChunks atomically replaces a video's transcript chunks and
// embeddings and records the embedding model/dim on the video row.
func (s *Store) ReplaceVideoChunks(ctx context.Context, videoID, model string, dim int, rows []ChunkRow, vectors [][]float32) error {
	if len(rows) != len(vectors) {
		return fmt.Errorf("rag: %d rows but %d vectors", len(rows), len(vectors))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteVideoTx(ctx, tx, videoID); err != nil {
		return err
	}
	for i, r := range rows {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO transcript_chunks (video_id, ordinal, text, start_seconds, token_count) VALUES (?,?,?,?,?)`,
			videoID, r.Ordinal, r.Text, r.StartSeconds, r.TokenCount)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vec_chunks (rowid, embedding) VALUES (?, ?)`, id, vecLiteral(vectors[i])); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE videos SET embed_model = ?, embed_dim = ? WHERE id = ?`, model, dim, videoID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteVideoChunks removes a video's chunks + embeddings (used on full delete).
func (s *Store) DeleteVideoChunks(ctx context.Context, videoID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteVideoTx(ctx, tx, videoID); err != nil {
		return err
	}
	return tx.Commit()
}

// Retrieve returns up to k chunks nearest to queryEmbedding across all videos.
func (s *Store) Retrieve(ctx context.Context, queryEmbedding []float32, k int) ([]Hit, error) {
	if k <= 0 {
		k = 10
	}
	const q = `
		SELECT c.video_id, c.ordinal, c.text, c.start_seconds, v.distance
		FROM vec_chunks v
		JOIN transcript_chunks c ON c.id = v.rowid
		WHERE v.embedding MATCH ? AND k = ?
		ORDER BY v.distance`
	rows, err := s.db.QueryContext(ctx, q, vecLiteral(queryEmbedding), k)
	if err != nil {
		return nil, fmt.Errorf("retrieve: %w", err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.VideoID, &h.Ordinal, &h.Text, &h.StartSeconds, &h.Distance); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// BuiltDim reports the dimension the vec_chunks table was created with, so the
// boot reconcile can detect an embed-model/dim change that invalidates it.
func (s *Store) BuiltDim(ctx context.Context) (int, error) {
	// vec0 exposes its columns via pragma table_info; the embedding column type
	// is reported as e.g. "float[1536]". Parse the bracketed dimension.
	rows, err := s.db.QueryContext(ctx, `SELECT type FROM pragma_table_info('vec_chunks') WHERE name = 'embedding'`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			return 0, err
		}
		open := strings.IndexByte(typ, '[')
		close := strings.IndexByte(typ, ']')
		if open >= 0 && close > open {
			return strconv.Atoi(typ[open+1 : close])
		}
	}
	return 0, fmt.Errorf("rag: could not determine vec_chunks dimension")
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/rag/ -v`
Expected: PASS. (If `BuiltDim`'s pragma parse fails on this sqlite-vec build, adjust to whatever `pragma_table_info('vec_chunks')` actually reports — write a quick sub-test printing the type, then fix the parse. Do not skip the test.)

- [ ] **Step 5: Commit**

```bash
git add backend/internal/rag/store.go backend/internal/rag/store_test.go
git commit -m "feat(rag): vec store — chunk+embedding writes, KNN retrieve, same-tx vec cleanup, dim introspection"
```

---

## Task 6: subtitles — VTT parser with auto-caption dedup

**Files:**
- Create: `backend/internal/subtitles/vtt.go`, `backend/internal/subtitles/vtt_test.go`

**Interfaces:**
- Produces: `subtitles.Cue{StartSeconds int; Text string}`; `subtitles.Parsed{Transcript string; Cues []Cue}`; `func ParseVTT(r io.Reader) (Parsed, error)`. Strips WebVTT headers, timestamps, positioning, and inline tags; collapses YouTube auto-caption rolling duplicates so each line appears once.

- [ ] **Step 1: Write the failing test**

```go
package subtitles

import (
	"strings"
	"testing"
)

const sample = `WEBVTT

00:00:01.000 --> 00:00:03.000
Hello and <c>welcome</c>

00:00:03.000 --> 00:00:05.000
Hello and welcome
to the show

00:01:48.500 --> 00:01:50.000 align:start position:0%
Let's start with the frame
`

func TestParseVTTDedupsAndTimestamps(t *testing.T) {
	p, err := ParseVTT(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Transcript, "<c>") {
		t.Fatal("tags not stripped")
	}
	// "Hello and welcome" must not appear twice back-to-back (rolling dup).
	if strings.Count(p.Transcript, "Hello and welcome") > 1 {
		t.Fatalf("rolling duplicate not collapsed:\n%s", p.Transcript)
	}
	if !strings.Contains(p.Transcript, "to the show") {
		t.Fatal("new text dropped")
	}
	// A cue at 1:48 => 108s exists.
	found := false
	for _, c := range p.Cues {
		if c.StartSeconds == 108 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a cue at 108s; got %+v", p.Cues)
	}
}

func TestParseVTTEmpty(t *testing.T) {
	p, err := ParseVTT(strings.NewReader("WEBVTT\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Transcript != "" || len(p.Cues) != 0 {
		t.Fatalf("expected empty parse, got %+v", p)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/subtitles/ -v`
Expected: FAIL.

- [ ] **Step 3: Implement `vtt.go`**

```go
// Package subtitles parses WebVTT into a clean plain transcript plus a cue index
// (start-second -> text) for timestamp mapping. It strips WebVTT structure and
// inline tags and collapses YouTube auto-caption rolling duplicates (each line is
// re-emitted with the next word appended, so naive concatenation triples length).
package subtitles

import (
	"bufio"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Cue is one subtitle line with its start time in whole seconds.
type Cue struct {
	StartSeconds int
	Text         string
}

// Parsed is the result of ParseVTT.
type Parsed struct {
	Transcript string
	Cues       []Cue
}

var (
	timingRe = regexp.MustCompile(`^(\d{2,}):(\d{2}):(\d{2})[.,](\d{3})\s*-->`)
	tagRe    = regexp.MustCompile(`<[^>]*>`)
)

// ParseVTT reads WebVTT and returns the deduplicated transcript + cue index.
func ParseVTT(r io.Reader) (Parsed, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var cues []Cue
	var curStart = -1
	var curLines []string
	var last string // last emitted line, for rolling-dup collapse

	flush := func() {
		if curStart < 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(curLines, " "))
		curLines = curLines[:0]
		if text == "" {
			return
		}
		// Collapse a rolling duplicate: if this cue's text is the previous line
		// with more appended (or identical), keep only the longer form.
		if last != "" && (text == last || strings.HasPrefix(text, last)) {
			// replace the previous cue's text with the extended one
			if len(cues) > 0 {
				cues[len(cues)-1].Text = text
			}
			last = text
			return
		}
		if last != "" && strings.HasPrefix(last, text) {
			// this cue is a prefix of the last one — drop it as a partial repeat
			return
		}
		cues = append(cues, Cue{StartSeconds: curStart, Text: text})
		last = text
	}

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if m := timingRe.FindStringSubmatch(line); m != nil {
			flush()
			h, _ := strconv.Atoi(m[1])
			mnt, _ := strconv.Atoi(m[2])
			s, _ := strconv.Atoi(m[3])
			curStart = h*3600 + mnt*60 + s
			continue
		}
		if curStart < 0 {
			continue // header / NOTE / cue-id lines before the first timing
		}
		clean := strings.TrimSpace(tagRe.ReplaceAllString(line, ""))
		if clean == "" {
			continue
		}
		curLines = append(curLines, clean)
	}
	flush()
	if err := sc.Err(); err != nil {
		return Parsed{}, err
	}

	texts := make([]string, len(cues))
	for i, c := range cues {
		texts[i] = c.Text
	}
	return Parsed{Transcript: strings.Join(texts, " "), Cues: cues}, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/subtitles/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/subtitles/
git commit -m "feat(subtitles): WebVTT parser with tag stripping + auto-caption dedup"
```

---

## Task 7: summaryjobs — the summarization queue store

**Files:**
- Create: `backend/internal/summaryjobs/store.go`, `backend/internal/summaryjobs/store_test.go`

**Interfaces:**
- Consumes: `summary_jobs` table (Task 1).
- Produces (mirrors `jobs.Store`): `summaryjobs.New(db) *Store`; `Job{ID int64; VideoID, State string; Attempts, MaxAttempts int; LastError string}`; `(s *Store) Enqueue(videoID string) (int64, error)`; `ClaimNext() (*Job, error)`; `Finish(id int64, state, lastErr string) error`; `Fail(id int64, attempts int, lastErr string) error`; `ResetOrphans() error`.

- [ ] **Step 1: Write the failing test**

```go
package summaryjobs

import (
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

func TestEnqueueClaimFinishResetOrphans(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	store.Migrate(db)
	db.Exec(`INSERT INTO videos (id, url) VALUES ('v1','u')`)
	s := New(db)

	if _, err := s.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimNext()
	if err != nil || job == nil || job.VideoID != "v1" || job.State != "running" {
		t.Fatalf("claim: %+v %v", job, err)
	}
	if n, _ := s.ClaimNext(); n != nil {
		t.Fatal("second claim should be nil (job already running)")
	}
	if err := s.Finish(job.ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	// orphan reset: a stuck running job returns to pending.
	db.Exec(`INSERT INTO summary_jobs (video_id, state) VALUES ('v1','running')`)
	if err := s.ResetOrphans(); err != nil {
		t.Fatal(err)
	}
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM summary_jobs WHERE state='pending'`).Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("orphan not reset, pending=%d", cnt)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/summaryjobs/ -v`
Expected: FAIL.

- [ ] **Step 3: Implement `store.go`** (structurally a trimmed `jobs.Store`; the atomic claim uses the same `UPDATE ... WHERE id = (SELECT ... LIMIT 1)` pattern):

```go
// Package summaryjobs persists the offline summarization+embedding queue
// (summary_jobs). It mirrors internal/jobs but is simpler: no priority, no
// log tail, no cancel — a summary either completes or fails with bounded retries.
package summaryjobs

import (
	"database/sql"
	"fmt"
)

type Job struct {
	ID          int64
	VideoID     string
	State       string
	Attempts    int
	MaxAttempts int
	LastError   string
}

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

// Enqueue inserts a pending job for videoID and returns its id.
func (s *Store) Enqueue(videoID string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO summary_jobs (video_id) VALUES (?)`, videoID)
	if err != nil {
		return 0, fmt.Errorf("summaryjobs: enqueue: %w", err)
	}
	return res.LastInsertId()
}

// ClaimNext atomically moves the oldest pending job to running and returns it,
// or (nil, nil) when the queue is empty.
func (s *Store) ClaimNext() (*Job, error) {
	row := s.db.QueryRow(`
		UPDATE summary_jobs SET state='running', started_at=datetime('now'), attempts=attempts+1
		WHERE id = (SELECT id FROM summary_jobs WHERE state='pending' ORDER BY enqueued_at, id LIMIT 1)
		RETURNING id, video_id, state, attempts, max_attempts, last_error`)
	var j Job
	err := row.Scan(&j.ID, &j.VideoID, &j.State, &j.Attempts, &j.MaxAttempts, &j.LastError)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("summaryjobs: claim: %w", err)
	}
	return &j, nil
}

// Finish records a terminal state ('done' or 'failed').
func (s *Store) Finish(id int64, state, lastErr string) error {
	_, err := s.db.Exec(`UPDATE summary_jobs SET state=?, last_error=?, finished_at=datetime('now') WHERE id=?`, state, lastErr, id)
	return err
}

// Fail requeues a job as pending after a retryable error, unless it has
// exhausted max_attempts, in which case it is marked failed.
func (s *Store) Fail(id int64, attempts int, lastErr string) error {
	_, err := s.db.Exec(`
		UPDATE summary_jobs
		SET state = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'pending' END,
		    last_error = ?
		WHERE id = ?`, lastErr, id)
	return err
}

// ResetOrphans returns jobs left 'running' by a crashed process to 'pending'.
func (s *Store) ResetOrphans() error {
	_, err := s.db.Exec(`UPDATE summary_jobs SET state='pending', started_at=NULL WHERE state='running'`)
	return err
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/summaryjobs/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/summaryjobs/
git commit -m "feat(summaryjobs): summarization queue store (enqueue/claim/finish/fail/reset)"
```

---

## Task 8: videos store — Phase 3 columns + setters

**Files:**
- Modify: `backend/internal/videos/store.go`
- Test: `backend/internal/videos/store_phase3_test.go` (create)

**Interfaces:**
- Consumes: `videos` new columns (Task 1).
- Produces: `Video` gains `AudioLanguage, SubtitlePath, Summary, Chapters, KeyPoints, SummaryStatus, SummaryError, EmbedModel string; EmbedDim int`. Setters: `SetSubtitle(id, relPath, audioLang string) error`; `SetSummaryStatus(id, status, errMsg string) error`; `SetSummary(id, summary, chaptersJSON, keyPointsJSON string) error`; `EnqueueSummaryStatus`... plus `Get`/`List`/`scanVideo` updated to read the new columns. `DownloadedResult` gains `SubtitleRelPath, AudioLanguage, ChaptersJSON string`; `SetDownloaded` persists them.

- [ ] **Step 1: Write the failing test**

```go
package videos

import (
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

func TestPhase3Setters(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	store.Migrate(db)
	s := New(db)
	if err := s.Upsert(Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSubtitle("v1", "UC/v1/v1.en.vtt", "en"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSummaryStatus("v1", "running", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSummary("v1", "prose", `[{"ts":0,"title":"Intro","source":"mimo"}]`, `[{"ts":12,"text":"wow"}]`); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.AudioLanguage != "en" || got.SubtitlePath != "UC/v1/v1.en.vtt" {
		t.Fatalf("subtitle fields: %+v", got)
	}
	if got.Summary != "prose" || got.SummaryStatus != "running" {
		t.Fatalf("summary fields: %+v", got)
	}
	if got.Chapters == "" || got.KeyPoints == "" {
		t.Fatal("chapters/key_points not stored")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/videos/ -run TestPhase3Setters -v`
Expected: FAIL.

- [ ] **Step 3: Implement.**
  1. Add the fields to `type Video struct` (after the existing ones): `AudioLanguage, SubtitlePath, Summary, Chapters, KeyPoints, SummaryStatus, SummaryError, EmbedModel string` and `EmbedDim int`.
  2. Extend `videoColumns` const to append: `, audio_language, subtitle_path, summary, chapters, key_points, summary_status, summary_error, embed_model, embed_dim`.
  3. In `scanVideo`, add the matching `&v.AudioLanguage, &v.SubtitlePath, &v.Summary, &v.Chapters, &v.KeyPoints, &v.SummaryStatus, &v.SummaryError, &v.EmbedModel, &v.EmbedDim` scan targets in the **same order**.
  4. Add setters:

```go
// SetSubtitle records the downloaded subtitle relpath and resolved audio
// language.
func (s *Store) SetSubtitle(id, relPath, audioLang string) error {
	_, err := s.db.Exec(`UPDATE videos SET subtitle_path=?, audio_language=? WHERE id=?`, relPath, audioLang, id)
	return err
}

// SetSummaryStatus updates the summarization lifecycle state and error.
func (s *Store) SetSummaryStatus(id, status, errMsg string) error {
	_, err := s.db.Exec(`UPDATE videos SET summary_status=?, summary_error=? WHERE id=?`, status, errMsg, id)
	return err
}

// SetSummary persists the three artifacts and marks the summary done.
func (s *Store) SetSummary(id, summary, chaptersJSON, keyPointsJSON string) error {
	_, err := s.db.Exec(
		`UPDATE videos SET summary=?, chapters=?, key_points=?, summary_status='done', summary_error='' WHERE id=?`,
		summary, chaptersJSON, keyPointsJSON, id)
	return err
}
```

  5. Extend `DownloadedResult` with `SubtitleRelPath, AudioLanguage, ChaptersJSON string`, and in `SetDownloaded` add them to the UPDATE (subtitle_path, audio_language, and chapters when `ChaptersJSON != ""`). Keep existing behavior otherwise. If `ChaptersJSON` is empty, do not overwrite `chapters` (leave the `'[]'` default for the summarizer to fill).

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/videos/ -v`
Expected: PASS (existing videos tests still green).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/videos/
git commit -m "feat(videos): phase-3 columns + subtitle/summary setters"
```

---

## Task 9: yt-dlp — subtitle flags + language capture

**Files:**
- Modify: `backend/internal/ytdlp/download.go`, `backend/internal/ytdlp/meta.go`
- Test: `backend/internal/ytdlp/download_subs_test.go` (create); reuse `testdata/fake-ytdlp.sh` pattern

**Interfaces:**
- Consumes: `DownloadReq`.
- Produces: `DownloadReq` gains `SubLang string`; `Result` gains `SubtitleRelPath, AudioLanguage string` and `ChaptersJSON string` (yt-dlp's own chapters as `[{ts,title,source:"yt-dlp"}]` JSON, or `""`). `download.go` adds `--write-subs --write-auto-subs --sub-langs <SubLang> --convert-subs vtt`. `meta.go`'s info struct gains `Language` (captured into the video's `audio_language` at Add — surfaced via the existing metadata result; add a `Language` field to whatever struct `Metadata` returns).

- [ ] **Step 1: Write the failing test.** Extend the fake yt-dlp script to also write a `.en.vtt` and include `language` + a `chapters` array in the info-json, then assert args + result.

```go
package ytdlp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadRequestsSubtitlesAndCapturesLanguage(t *testing.T) {
	// fakeYtdlp writes <id>.mp4, <id>.info.json (with channel_id, language,
	// chapters) and <id>.en.vtt into the -o dir, recording argv to a file.
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	runner := newFakeRunnerWithSubs(t, dir, argsFile) // helper: builds a Runner whose exec runs the fake script; see existing download_test.go for the pattern
	res, err := runner.Download(context.Background(), DownloadReq{
		URL: "https://youtu.be/abc", VideoID: "abc", Format: "apple-1080p", SubLang: "en",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	argv, _ := os.ReadFile(argsFile)
	for _, want := range []string{"--write-subs", "--write-auto-subs", "--sub-langs", "en", "--convert-subs", "vtt"} {
		if !strings.Contains(string(argv), want) {
			t.Fatalf("missing arg %q in %s", want, argv)
		}
	}
	if res.SubtitleRelPath == "" || !strings.HasSuffix(res.SubtitleRelPath, ".vtt") {
		t.Fatalf("subtitle relpath = %q", res.SubtitleRelPath)
	}
	if res.AudioLanguage != "en" {
		t.Fatalf("audio language = %q", res.AudioLanguage)
	}
	if !strings.Contains(res.ChaptersJSON, "yt-dlp") {
		t.Fatalf("expected yt-dlp chapters json, got %q", res.ChaptersJSON)
	}
}
```

(Model `newFakeRunnerWithSubs` and the fake script on the existing `download_test.go` + `testdata/fake-ytdlp.sh`; extend the script to `printf` a minimal `WEBVTT` file to `<id>.<lang>.vtt` and add `"language":"en"` and a `"chapters":[{"start_time":0,"title":"Intro"}]` to the info-json it writes.)

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/ytdlp/ -run TestDownloadRequestsSubtitles -v`
Expected: FAIL.

- [ ] **Step 3: Implement.**
  1. `DownloadReq`: add `SubLang string`.
  2. In `Download`, after the existing `args = append(args, ...)` block, add (only when `req.SubLang != ""`, else use the request as-is with no sub-lang — but the worker always sets it):

```go
	subLang := req.SubLang
	if subLang == "" {
		subLang = "en"
	}
	args = append(args, "--write-subs", "--write-auto-subs", "--sub-langs", subLang, "--convert-subs", "vtt")
```

  Insert these into the same `args` slice **before** `watchURL` (the URL must stay last). Re-order so `watchURL` is appended last.
  3. `downloadInfoJSON`: add `Language string \`json:"language"\`` (chapters already present).
  4. `Result`: add `SubtitleRelPath, AudioLanguage, ChaptersJSON string`.
  5. In `finalizeDownload`: after moving the staging dir, locate the `.vtt` (glob `<finalDir>/<videoID>*.vtt`; take the first). Set `Result.SubtitleRelPath` to the mediaDir-relative path (`filepath.Rel(mediaDir, vttPath)`), `Result.AudioLanguage = info.Language`. Build `ChaptersJSON` from `info.Chapters` **excluding** SponsorBlock chapters (skip titles with `sponsorblockChapterPrefix`) as `[{"ts":int(start_time),"title":..,"source":"yt-dlp"}]`; if none, leave `""`.

```go
	// non-SponsorBlock chapters become the provisional yt-dlp TOC
	type chapterOut struct {
		TS     int    `json:"ts"`
		Title  string `json:"title"`
		Source string `json:"source"`
	}
	var chs []chapterOut
	for _, c := range info.Chapters {
		if strings.HasPrefix(c.Title, sponsorblockChapterPrefix) {
			continue
		}
		chs = append(chs, chapterOut{TS: int(c.StartTime), Title: c.Title, Source: "yt-dlp"})
	}
	if len(chs) > 0 {
		if b, err := json.Marshal(chs); err == nil {
			result.ChaptersJSON = string(b)
		}
	}
```

  6. `meta.go`: add `Language` to the info struct `Metadata` parses and expose it on the metadata result type the caller uses at Add (so `handleDownloadsPost` can store `audio_language`). If plumbing the field to the handler is noisy, it is acceptable to leave `audio_language` unset at Add and rely solely on the post-download `info.Language` (Task 10 updates it via `SetDownloaded`/`SetSubtitle`) — pick the post-download path if simpler and note it.

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/ytdlp/ -v`
Expected: PASS (existing download tests still green — they don't set SubLang; the default keeps args valid).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/ytdlp/
git commit -m "feat(ytdlp): request audio-language VTT subtitles; capture language + yt-dlp chapters"
```

---

## Task 10: download worker — resolve sub-lang, persist subtitle, enqueue summary job

**Files:**
- Modify: `backend/internal/download/worker.go`
- Test: `backend/internal/download/worker_subs_test.go` (create)

**Interfaces:**
- Consumes: `videos.DownloadedResult` (Task 8), `ytdlp.Result` (Task 9), `summaryjobs.Store.Enqueue` (Task 7), `config.DefaultSubLang`.
- Produces: `download.Deps` gains `SummaryJobs interface{ Enqueue(videoID string) (int64, error) }` and `DefaultSubLang string`. On success, the worker resolves `req.SubLang` from the video's `AudioLanguage` (if already known) else `DefaultSubLang`, passes subtitle/chapters into `SetDownloaded`, and enqueues a summary job. Summary enqueue happens for **every** successful download (initial or re-download).

- [ ] **Step 1: Write the failing test.** Use a fake Runner returning a `ytdlp.Result` with `SubtitleRelPath`/`AudioLanguage`/`ChaptersJSON`, and a spy `SummaryJobs` capturing enqueued video ids; assert the video row got the subtitle fields and a summary job was enqueued.

```go
func TestSucceedPersistsSubtitleAndEnqueuesSummary(t *testing.T) {
	// ... set up jobs+videos stores, a fake Runner whose Download returns:
	//     &ytdlp.Result{MediaPath: ..., SubtitleRelPath: "UC/v1/v1.en.vtt", AudioLanguage: "en", ChaptersJSON: `[{"ts":0,"title":"Intro","source":"yt-dlp"}]`}
	// enqueue a download job for v1, run one worker tick, then assert:
	//   videos.Get("v1").SubtitlePath == "UC/v1/v1.en.vtt"
	//   the spy SummaryJobs recorded Enqueue("v1")
}
```

(Model the harness on the existing `download` worker tests — reuse their store setup + single-tick driving. Keep the assertion body concrete when writing it.)

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/download/ -run TestSucceedPersistsSubtitle -v`
Expected: FAIL.

- [ ] **Step 3: Implement.**
  1. Add to `Deps`: `SummaryJobs SummaryEnqueuer` and `DefaultSubLang string`, with a small interface `type SummaryEnqueuer interface { Enqueue(videoID string) (int64, error) }` (Optional: when nil, skip enqueue — but production always sets it).
  2. Where `process` builds the `ytdlp.DownloadReq`, set `SubLang`:

```go
	subLang := video.AudioLanguage
	if subLang == "" {
		subLang = w.deps.DefaultSubLang
	}
	req := ytdlp.DownloadReq{URL: video.URL, VideoID: video.ID, Format: /*existing*/, CustomFormat: /*existing*/, LimitRate: /*existing*/, SubLang: subLang}
```

  3. In `succeed`, extend the `videos.DownloadedResult` it builds with `SubtitleRelPath: res.SubtitleRelPath, AudioLanguage: res.AudioLanguage, ChaptersJSON: res.ChaptersJSON`. After the successful `SetDownloaded`, enqueue the summary job:

```go
	if w.deps.SummaryJobs != nil {
		if _, err := w.deps.SummaryJobs.Enqueue(video.ID); err != nil {
			w.deps.Logger.Error("download worker: enqueue summary job failed", "video_id", video.ID, "err", err)
		}
	}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/download/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/download/
git commit -m "feat(download): resolve sub-lang, persist subtitle/chapters, enqueue summary job on success"
```

---

## Task 11: summarize — map-reduce summarizer (3 artifacts)

**Files:**
- Create: `backend/internal/summarize/summarizer.go`, `backend/internal/summarize/summarizer_test.go`

**Interfaces:**
- Consumes: `llm.Client.Complete`, `subtitles.Cue`.
- Produces:
  - `summarize.Chapter{TS int; Title, Source string}` (subtopics omitted in P3 for simplicity — note this narrowing vs the spec; a flat chapter list ships, nested subtopics deferred).
  - `summarize.KeyPoint{TS int; Text string}`
  - `summarize.Artifacts{Summary string; Chapters []Chapter; KeyPoints []KeyPoint}`
  - `summarize.Completer interface { Complete(ctx, []llm.Message) (string, error) }` (so tests fake it)
  - `summarize.New(c Completer) *Summarizer`
  - `func (s *Summarizer) Run(ctx context.Context, transcript string, cues []subtitles.Cue, ytdlpChapters []Chapter) (Artifacts, error)` — map-reduce: chunk transcript (via `rag.Chunk`), summarize each chunk, reduce chunk-summaries into `Summary`; then a second reduce call returns chapters+key-points as JSON. If `ytdlpChapters` is non-empty, use them verbatim and skip chapter generation.

- [ ] **Step 1: Write the failing test** — fake Completer returns canned JSON for the reduce call.

```go
package summarize

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/subtitles"
)

type fakeCompleter struct{ replies []string; i int }

func (f *fakeCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	r := f.replies[f.i%len(f.replies)]
	f.i++
	return r, nil
}

func TestRunProducesThreeArtifactsAndPrefersYtdlpChapters(t *testing.T) {
	cues := []subtitles.Cue{{StartSeconds: 0, Text: "intro"}, {StartSeconds: 108, Text: "titanium frame"}}
	transcript := strings.Repeat("word ", 2000)
	fc := &fakeCompleter{replies: []string{
		"chunk summary",                                    // map calls
		"Overall prose summary.",                           // reduce: summary
		`{"key_points":[{"ts":108,"text":"weight drop"}]}`, // reduce: key points
	}}
	s := New(fc)
	got, err := s.Run(context.Background(), transcript, cues, []Chapter{{TS: 0, Title: "Intro", Source: "yt-dlp"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary == "" {
		t.Fatal("empty summary")
	}
	if len(got.Chapters) != 1 || got.Chapters[0].Source != "yt-dlp" {
		t.Fatalf("expected yt-dlp chapters preserved: %+v", got.Chapters)
	}
	if len(got.KeyPoints) != 1 || got.KeyPoints[0].TS != 108 {
		t.Fatalf("key points: %+v", got.KeyPoints)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/summarize/ -run TestRunProducesThreeArtifacts -v`
Expected: FAIL.

- [ ] **Step 3: Implement `summarizer.go`.** Map each chunk → a chunk summary; join them; reduce to the prose summary; a JSON reduce call for key-points (and chapters when yt-dlp didn't supply them). Parse JSON defensively (strip ```json fences). Full code:

```go
// Package summarize turns a transcript into three artifacts via map-reduce over
// chunks, dodging the model's context window: summarize each chunk, then reduce
// the chunk summaries. Chapters prefer yt-dlp's own metadata when present.
package summarize

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/subtitles"
)

type Chapter struct {
	TS     int    `json:"ts"`
	Title  string `json:"title"`
	Source string `json:"source"`
}

type KeyPoint struct {
	TS   int    `json:"ts"`
	Text string `json:"text"`
}

type Artifacts struct {
	Summary   string
	Chapters  []Chapter
	KeyPoints []KeyPoint
}

// Completer is the subset of llm.Client the summarizer needs.
type Completer interface {
	Complete(ctx context.Context, messages []llm.Message) (string, error)
}

type Summarizer struct{ c Completer }

func New(c Completer) *Summarizer { return &Summarizer{c: c} }

func (s *Summarizer) Run(ctx context.Context, transcript string, cues []subtitles.Cue, ytdlpChapters []Chapter) (Artifacts, error) {
	chunks := rag.Chunk(transcript, rag.DefaultChunkOptions())
	if len(chunks) == 0 {
		return Artifacts{}, fmt.Errorf("summarize: empty transcript")
	}

	// MAP: summarize each chunk.
	var chunkSummaries []string
	for _, ch := range chunks {
		out, err := s.c.Complete(ctx, []llm.Message{
			{Role: "system", Content: "You summarize one section of a video transcript in 2-3 sentences. Be concrete."},
			{Role: "user", Content: ch.Text},
		})
		if err != nil {
			return Artifacts{}, fmt.Errorf("summarize map: %w", err)
		}
		chunkSummaries = append(chunkSummaries, strings.TrimSpace(out))
	}
	joined := strings.Join(chunkSummaries, "\n\n")

	// REDUCE 1: prose summary.
	summary, err := s.c.Complete(ctx, []llm.Message{
		{Role: "system", Content: "Combine these section summaries of one video into a single cohesive summary of 2-4 short paragraphs."},
		{Role: "user", Content: joined},
	})
	if err != nil {
		return Artifacts{}, fmt.Errorf("summarize reduce: %w", err)
	}

	// REDUCE 2: key points (and chapters if yt-dlp didn't provide them). Provide
	// the cue index so the model can attach timestamps.
	cueIndex := formatCues(cues)
	wantChapters := len(ytdlpChapters) == 0
	kpPrompt := "From the video, extract notable/surprising/quotable moments as JSON " +
		`{"key_points":[{"ts":<seconds>,"text":"..."}]}`
	if wantChapters {
		kpPrompt = "From the video, produce a timestamped chapter list AND key points as JSON " +
			`{"chapters":[{"ts":<seconds>,"title":"..."}],"key_points":[{"ts":<seconds>,"text":"..."}]}`
	}
	raw, err := s.c.Complete(ctx, []llm.Message{
		{Role: "system", Content: kpPrompt + " Use only timestamps that appear in the cue index. Output JSON only."},
		{Role: "user", Content: "SUMMARY:\n" + summary + "\n\nCUE INDEX (seconds: text):\n" + cueIndex},
	})
	if err != nil {
		return Artifacts{}, fmt.Errorf("summarize keypoints: %w", err)
	}

	var parsed struct {
		Chapters  []Chapter  `json:"chapters"`
		KeyPoints []KeyPoint `json:"key_points"`
	}
	_ = json.Unmarshal([]byte(stripFences(raw)), &parsed) // tolerate malformed JSON: leave empty

	art := Artifacts{Summary: strings.TrimSpace(summary), KeyPoints: parsed.KeyPoints}
	if wantChapters {
		for i := range parsed.Chapters {
			parsed.Chapters[i].Source = "mimo"
		}
		art.Chapters = parsed.Chapters
	} else {
		art.Chapters = ytdlpChapters
	}
	return art, nil
}

func formatCues(cues []subtitles.Cue) string {
	var b strings.Builder
	for _, c := range cues {
		fmt.Fprintf(&b, "%d: %s\n", c.StartSeconds, c.Text)
	}
	return b.String()
}

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/summarize/ -run TestRunProducesThreeArtifacts -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/summarize/summarizer.go backend/internal/summarize/summarizer_test.go
git commit -m "feat(summarize): map-reduce summarizer producing summary + chapters + key points"
```

---

## Task 12: summarize — the worker

**Files:**
- Create: `backend/internal/summarize/worker.go`, `backend/internal/summarize/worker_test.go`

**Interfaces:**
- Consumes: `summaryjobs.Store`, `videos.Store`, `rag.Store`, `rag.EmbedClient`, `summarize.Summarizer`, `subtitles.ParseVTT`, `media.SafeMediaPath`.
- Produces: `summarize.WorkerDeps{Jobs *summaryjobs.Store; Videos *videos.Store; Rag *rag.Store; Summarizer *Summarizer; Embedder Embedder; MediaDir string; EmbedModel string; EmbedDim int; PollInterval time.Duration; Logger *slog.Logger}`; `Embedder interface { Embed(ctx, []string) ([][]float32, error) }`; `NewWorker(WorkerDeps) *Worker`; `func (w *Worker) Run(ctx context.Context)`. Loop: reset orphans → claim job → load video → resolve `subtitle_path` via `SafeMediaPath` → if no subtitle/empty transcript set `summary_status='no_transcript'` and finish → else set `running`, parse VTT, summarize, embed chunks (mapped to cue start-seconds), `ReplaceVideoChunks`, `SetSummary`, finish `done`. Panic-recover per job.

- [ ] **Step 1: Write the failing test** — wire fakes; drive one tick via a cancellable ctx or a single `processOne` helper. Cover: (a) happy path persists summary + chunks; (b) a video with empty `subtitle_path` → `no_transcript`, no embed/summarize calls.

```go
func TestWorkerNoTranscriptShortCircuits(t *testing.T) {
	// video with subtitle_path='' -> claim -> summary_status becomes 'no_transcript',
	// Summarizer/Embedder never called, job finished 'done'.
}

func TestWorkerHappyPathPersistsSummaryAndChunks(t *testing.T) {
	// write a real .vtt under MediaDir/<rel>, set subtitle_path=rel,
	// fake Summarizer via a fake Completer, fake Embedder returns dim-length vectors,
	// run one tick, assert videos.Get.SummaryStatus=='done', Summary!='',
	// transcript_chunks rows > 0.
}
```

Provide a test seam: expose `func (w *Worker) processOne(ctx) (bool, error)` (claims+processes a single job; returns false when queue empty) so the tests don't need goroutine timing. `Run` calls `processOne` in its loop.

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/summarize/ -run TestWorker -v`
Expected: FAIL.

- [ ] **Step 3: Implement `worker.go`.**

```go
package summarize

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/subtitles"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/videos"
)

// Embedder is the subset of rag.EmbedClient the worker needs.
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

type WorkerDeps struct {
	Jobs         *summaryjobs.Store
	Videos       *videos.Store
	Rag          *rag.Store
	Summarizer   *Summarizer
	Embedder     Embedder
	MediaDir     string
	EmbedModel   string
	EmbedDim     int
	PollInterval time.Duration
	Logger       *slog.Logger
}

type Worker struct{ d WorkerDeps }

func NewWorker(d WorkerDeps) *Worker {
	if d.PollInterval <= 0 {
		d.PollInterval = 2 * time.Second
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Worker{d: d}
}

func (w *Worker) Run(ctx context.Context) {
	if err := w.d.Jobs.ResetOrphans(); err != nil {
		w.d.Logger.Error("summarize worker: reset orphans", "err", err)
	}
	for {
		if ctx.Err() != nil {
			return
		}
		did, err := w.processOne(ctx)
		if err != nil {
			w.d.Logger.Error("summarize worker: process", "err", err)
		}
		if !did {
			t := time.NewTimer(w.d.PollInterval)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	}
}

// processOne claims and processes a single job. Returns false when the queue is
// empty. Panics are recovered so one bad job never kills the loop.
func (w *Worker) processOne(ctx context.Context) (did bool, err error) {
	job, err := w.d.Jobs.ClaimNext()
	if err != nil || job == nil {
		return false, err
	}
	defer func() {
		if r := recover(); r != nil {
			w.d.Logger.Error("summarize worker: recovered", "job_id", job.ID, "panic", r)
			_ = w.d.Jobs.Fail(job.ID, job.Attempts, "panic")
			w.d.Videos.SetSummaryStatus(job.VideoID, "error", "internal error")
		}
	}()

	video, err := w.d.Videos.Get(job.VideoID)
	if err != nil || video == nil {
		_ = w.d.Jobs.Finish(job.ID, "failed", "video missing")
		return true, err
	}

	// No subtitles => clean terminal no_transcript state.
	if video.SubtitlePath == "" {
		w.d.Videos.SetSummaryStatus(video.ID, "no_transcript", "")
		_ = w.d.Jobs.Finish(job.ID, "done", "")
		return true, nil
	}

	w.d.Videos.SetSummaryStatus(video.ID, "running", "")

	safe, err := media.SafeMediaPath(w.d.MediaDir, video.SubtitlePath)
	if err != nil {
		return true, w.failJob(job, video.ID, "unsafe subtitle path")
	}
	f, err := os.Open(safe)
	if err != nil {
		w.d.Videos.SetSummaryStatus(video.ID, "no_transcript", "")
		_ = w.d.Jobs.Finish(job.ID, "done", "")
		return true, nil
	}
	parsed, perr := subtitles.ParseVTT(f)
	f.Close()
	if perr != nil {
		return true, w.failJob(job, video.ID, "parse vtt: "+perr.Error())
	}
	if parsed.Transcript == "" {
		w.d.Videos.SetSummaryStatus(video.ID, "no_transcript", "")
		_ = w.d.Jobs.Finish(job.ID, "done", "")
		return true, nil
	}

	// yt-dlp chapters already stored on the video (source=yt-dlp) are preferred.
	ytChapters := decodeChapters(video.Chapters)
	art, err := w.d.Summarizer.Run(ctx, parsed.Transcript, parsed.Cues, ytChapters)
	if err != nil {
		return true, w.failJob(job, video.ID, err.Error())
	}

	if err := w.embedAndStore(ctx, video.ID, parsed); err != nil {
		return true, w.failJob(job, video.ID, err.Error())
	}

	chJSON := encodeChapters(art.Chapters)
	kpJSON := encodeKeyPoints(art.KeyPoints)
	if err := w.d.Videos.SetSummary(video.ID, art.Summary, chJSON, kpJSON); err != nil {
		return true, w.failJob(job, video.ID, err.Error())
	}
	_ = w.d.Jobs.Finish(job.ID, "done", "")
	return true, nil
}

func (w *Worker) failJob(job *summaryjobs.Job, videoID, msg string) error {
	w.d.Videos.SetSummaryStatus(videoID, "error", msg)
	return w.d.Jobs.Fail(job.ID, job.Attempts, msg)
}

// embedAndStore chunks the transcript, maps each chunk to the nearest earlier
// cue start-second, embeds, and replaces the video's chunks+vectors.
func (w *Worker) embedAndStore(ctx context.Context, videoID string, parsed subtitles.Parsed) error {
	chunks := rag.Chunk(parsed.Transcript, rag.DefaultChunkOptions())
	if len(chunks) == 0 {
		return errors.New("no chunks")
	}
	texts := make([]string, len(chunks))
	rows := make([]rag.ChunkRow, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
		rows[i] = rag.ChunkRow{Ordinal: c.Ordinal, Text: c.Text, TokenCount: c.TokenCount, StartSeconds: cueStartFor(c.Text, parsed.Cues)}
	}
	vecs, err := w.d.Embedder.Embed(ctx, texts)
	if err != nil {
		return err
	}
	return w.d.Rag.ReplaceVideoChunks(ctx, videoID, w.d.EmbedModel, w.d.EmbedDim, rows, vecs)
}

// cueStartFor finds the start-second of the first cue whose text opens this
// chunk (chunks are built from the joined cue texts, so the chunk's first words
// match some cue). Falls back to 0.
func cueStartFor(chunkText string, cues []subtitles.Cue) int {
	head := chunkText
	if len(head) > 24 {
		head = head[:24]
	}
	for _, c := range cues {
		if len(c.Text) >= len(head) && c.Text[:min(len(head), len(c.Text))] == head {
			return c.StartSeconds
		}
	}
	return 0
}

func min(a, b int) int { if a < b { return a }; return b }
```

Add `chapters.go` helpers in the same package (`decodeChapters(string) []Chapter`, `encodeChapters([]Chapter) string`, `encodeKeyPoints([]KeyPoint) string`) using `encoding/json`, returning `"[]"` on empty/marshal-error.

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/summarize/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/summarize/
git commit -m "feat(summarize): summarization+embedding worker with no_transcript short-circuit"
```

---

## Task 13: HTTP — subtitles endpoint

**Files:**
- Create: `backend/internal/httpapi/subtitles_handlers.go`, `backend/internal/httpapi/subtitles_handlers_test.go`
- Modify: `backend/internal/httpapi/server.go` (route)

**Interfaces:**
- Consumes: `Deps.Videos`, `Deps.MediaDir`, `media.SafeMediaPath`.
- Produces: `GET /api/videos/{id}/subtitles` → serves the VTT with `Content-Type: text/vtt; charset=utf-8`; 404 when `subtitle_path` empty; path-safe.

- [ ] **Step 1: Write the failing test**

```go
func TestSubtitlesEndpointServesVTTAndGuardsPath(t *testing.T) {
	// video with subtitle_path pointing at a real .vtt under MediaDir -> 200 text/vtt
	// video with subtitle_path='' -> 404
	// a subtitle_path attempting traversal -> 404/400 (SafeMediaPath rejects)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/httpapi/ -run TestSubtitlesEndpoint -v`
Expected: FAIL.

- [ ] **Step 3: Implement handler** (mirror `handleVideoThumbnail`):

```go
func (s *Server) handleVideoSubtitles(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := s.deps.Videos.Get(id)
	if err != nil || v == nil || v.SubtitlePath == "" {
		http.NotFound(w, r)
		return
	}
	path, err := media.SafeMediaPath(s.deps.MediaDir, v.SubtitlePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	http.ServeFile(w, r, path)
}
```

Register in `server.go` next to the thumbnail route:

```go
	mux.Handle("GET /api/videos/{id}/subtitles", s.requireAuth(http.HandlerFunc(s.handleVideoSubtitles)))
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/httpapi/ -run TestSubtitlesEndpoint -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/httpapi/subtitles_handlers.go backend/internal/httpapi/subtitles_handlers_test.go backend/internal/httpapi/server.go
git commit -m "feat(httpapi): path-safe GET /api/videos/{id}/subtitles"
```

---

## Task 14: HTTP — search endpoint + resummarize + video JSON fields

**Files:**
- Create: `backend/internal/httpapi/search_handlers.go`, `backend/internal/httpapi/search_handlers_test.go`
- Modify: `backend/internal/httpapi/{server.go,videos_handlers.go}`

**Interfaces:**
- Consumes: `Deps` gains `Rag *rag.Store`, `Embedder interface{ Embed(ctx, []string) ([][]float32, error) }`, `SummaryJobs interface{ Enqueue(string) (int64, error) }`.
- Produces:
  - `GET /api/search?q=&k=` → `{"results":[{"video":{...},"matches":[{"start_seconds":int,"snippet":string,"distance":float}]}]}`. Blank `q` → `{"results":[]}` with no embed call.
  - `POST /api/videos/{id}/resummarize` → resets `summary_status='pending'` and enqueues a summary job; 202.
  - `GET /api/videos/{id}` and `GET /api/videos` JSON gain `summary, chapters, key_points, summary_status, audio_language, has_subtitles`.

- [ ] **Step 1: Write the failing test**

```go
func TestSearchGroupsByVideo(t *testing.T) {
	// seed 2 videos + chunks/vecs via rag.Store, fake Embedder returns a query
	// vector near video v1's chunk; GET /api/search?q=iphone -> results[0].video.id == "v1"
	// with a match carrying start_seconds + snippet.
}
func TestSearchBlankQueryReturnsEmpty(t *testing.T) { /* q="" -> {"results":[]}, embedder not called */ }
func TestResummarizeEnqueues(t *testing.T) { /* POST -> 202, spy SummaryJobs recorded id, summary_status='pending' */ }
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd backend && go test ./internal/httpapi/ -run 'TestSearch|TestResummarize' -v`
Expected: FAIL.

- [ ] **Step 3: Implement.**
  Add to `Deps`: `Rag *rag.Store`, `Embedder SearchEmbedder`, `SummaryJobs SummaryEnqueuer` with local interfaces:

```go
type SearchEmbedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}
type SummaryEnqueuer interface {
	Enqueue(videoID string) (int64, error)
}
```

  `search_handlers.go`:

```go
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"results": []any{}})
		return
	}
	if s.deps.Rag == nil || s.deps.Embedder == nil {
		http.Error(w, "search unavailable", http.StatusServiceUnavailable)
		return
	}
	k := 20
	vecs, err := s.deps.Embedder.Embed(r.Context(), []string{q})
	if err != nil || len(vecs) == 0 {
		http.Error(w, "embed failed", http.StatusBadGateway)
		return
	}
	hits, err := s.deps.Rag.Retrieve(r.Context(), vecs[0], k)
	if err != nil {
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}
	// group hits by video, preserving best-distance order
	type match struct {
		StartSeconds int     `json:"start_seconds"`
		Snippet      string  `json:"snippet"`
		Distance     float64 `json:"distance"`
	}
	type group struct {
		Video   any     `json:"video"`
		Matches []match `json:"matches"`
	}
	order := []string{}
	byVideo := map[string]*group{}
	for _, h := range hits {
		g := byVideo[h.VideoID]
		if g == nil {
			v, _ := s.deps.Videos.Get(h.VideoID)
			if v == nil {
				continue
			}
			g = &group{Video: videoJSON(v, s.deps.MediaDir)}
			byVideo[h.VideoID] = g
			order = append(order, h.VideoID)
		}
		g.Matches = append(g.Matches, match{StartSeconds: h.StartSeconds, Snippet: snippet(h.Text), Distance: h.Distance})
	}
	out := make([]*group, 0, len(order))
	for _, id := range order {
		out = append(out, byVideo[id])
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

func snippet(s string) string {
	if len(s) <= 160 {
		return s
	}
	return s[:160] + "…"
}
```

  Add `handleResummarize`:

```go
func (s *Server) handleResummarize(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := s.deps.Videos.Get(id)
	if err != nil || v == nil {
		http.NotFound(w, r)
		return
	}
	if s.deps.SummaryJobs == nil {
		http.Error(w, "summaries unavailable", http.StatusServiceUnavailable)
		return
	}
	_ = s.deps.Videos.SetSummaryStatus(id, "pending", "")
	if _, err := s.deps.SummaryJobs.Enqueue(id); err != nil {
		http.Error(w, "enqueue failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
```

  Extend the video JSON encoder used by `handleGetVideo`/`handleListVideos` (find the existing `videoJSON`/marshal helper in `videos_handlers.go`) to add: `"summary","chapters" (raw json.RawMessage from v.Chapters),"key_points" (raw),"summary_status","audio_language","has_subtitles": v.SubtitlePath != ""`. Chapters/key_points are stored as JSON text — emit them as `json.RawMessage` so the client gets arrays, not strings.

  Routes in `server.go`:

```go
	mux.Handle("GET /api/search", s.requireAuth(http.HandlerFunc(s.handleSearch)))
	mux.Handle("POST /api/videos/{id}/resummarize", s.requireAuth(http.HandlerFunc(s.handleResummarize)))
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd backend && go test ./internal/httpapi/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/httpapi/
git commit -m "feat(httpapi): semantic search, resummarize, and summary fields on video JSON"
```

---

## Task 15: main.go — wire clients, worker, boot dim-guard, routes

**Files:**
- Modify: `backend/cmd/peeq/main.go`
- Test: covered by build + existing boot behavior; add `backend/cmd/peeq/` nothing new (integration exercised manually). A focused unit test is not practical for `main`; rely on `go build` + `go vet`.

**Interfaces:**
- Consumes: everything above.
- Produces: constructed `llm.Client`, `rag.EmbedClient`, `rag.Store`, `summaryjobs.Store`, `summarize.Summarizer` + `summarize.Worker`; download worker `Deps` gets `SummaryJobs` + `DefaultSubLang`; `httpapi.Deps` gets `Rag`, `Embedder`, `SummaryJobs`; a boot dim-guard runs before starting the summarize worker.

- [ ] **Step 1: Implement.** After the existing store constructions (`videosStore`, `jobsStore`, ...), add:

```go
	summaryJobsStore := summaryjobs.New(db)
	ragStore := rag.NewStore(db)
	embedClient := rag.NewEmbedClient(rag.EmbedConfig{
		BaseURL: cfg.EmbedBaseURL, APIKey: cfg.EmbedAPIKey, Model: cfg.EmbedModel,
	}, nil)
	mimoClient := llm.NewClient(llm.Config{BaseURL: cfg.MimoBaseURL, APIKey: cfg.MimoAPIKey}, nil)
	summarizer := summarize.New(mimoClient)

	// Boot dim-guard: if the configured embedding dim differs from the vec_chunks
	// table's built dim, the whole vector table is invalid. Log loudly and rebuild
	// (drop all chunks; the summarize worker re-embeds on the next resummarize /
	// download). Recreating the dev DB is the intended remedy for a dim change.
	if builtDim, err := ragStore.BuiltDim(ctx); err == nil && builtDim != cfg.EmbedDim {
		slog.Warn("embedding dimension mismatch; vector table is stale",
			"built", builtDim, "configured", cfg.EmbedDim,
			"action", "recreate the database (rm ./data/peeq.db*) to rebuild vec_chunks at the new dimension")
	}
```

  Add `SummaryJobs: summaryJobsStore` and `DefaultSubLang: cfg.DefaultSubLang` to the `download.New(download.Deps{...})` literal.

  Construct + start the summarize worker alongside the download worker goroutine:

```go
	summarizeWorker := summarize.NewWorker(summarize.WorkerDeps{
		Jobs: summaryJobsStore, Videos: videosStore, Rag: ragStore,
		Summarizer: summarizer, Embedder: embedClient, MediaDir: cfg.MediaDir,
		EmbedModel: cfg.EmbedModel, EmbedDim: cfg.EmbedDim,
	})
	go func() {
		slog.Info("summarize worker started")
		summarizeWorker.Run(ctx)
	}()
```

  Add to the `httpapi.Deps{...}` literal: `Rag: ragStore, Embedder: embedClient, SummaryJobs: summaryJobsStore`.

- [ ] **Step 2: Build + vet**

Run: `cd backend && go build ./... && go vet ./...`
Expected: no errors.

- [ ] **Step 3: Full backend test with race**

Run: `cd backend && go test ./... -race`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add backend/cmd/peeq/main.go
git commit -m "feat(main): wire MiMo + embeddings clients, summarize worker, dim-guard, search routes"
```

---

## Task 16: Frontend — API types + client (videos fields, search, subtitles)

**Files:**
- Modify: `ui/src/api/types.ts`, `ui/src/api/videos.ts`, `ui/src/api/index.ts`
- Create: `ui/src/api/search.ts`, `ui/src/api/search.test.ts`

**Interfaces:**
- Produces: `Video` type gains `summary: string; chapters: Chapter[]; key_points: KeyPoint[]; summary_status: string; audio_language: string; has_subtitles: boolean`; types `Chapter{ts:number;title:string;source:string}`, `KeyPoint{ts:number;text:string}`; `SearchResult{video:Video;matches:{start_seconds:number;snippet:string;distance:number}[]}`; `searchVideos(q:string): Promise<SearchResult[]>`; `resummarize(id:string): Promise<void>`; `subtitlesUrl(id:string): string`.

- [ ] **Step 1: Write the failing test** (`search.test.ts`) — mock `fetch`, assert `searchVideos('iphone')` calls `/api/search?q=iphone` and returns parsed results; `searchVideos('')` returns `[]` without fetching.

```ts
import { describe, it, expect, vi } from 'vitest'
import { searchVideos } from './search'

describe('searchVideos', () => {
  it('returns [] for blank query without fetching', async () => {
    const spy = vi.spyOn(globalThis, 'fetch')
    expect(await searchVideos('   ')).toEqual([])
    expect(spy).not.toHaveBeenCalled()
    spy.mockRestore()
  })
  it('fetches and parses results', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ results: [{ video: { id: 'v1' }, matches: [] }] }), { status: 200 }),
    )
    const r = await searchVideos('iphone')
    expect(r[0].video.id).toBe('v1')
  })
})
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd ui && npx vitest run src/api/search.test.ts`
Expected: FAIL.

- [ ] **Step 3: Implement.** Add the fields/types to `types.ts`. Create `search.ts`:

```ts
import type { Video } from './types'

export type SearchMatch = { start_seconds: number; snippet: string; distance: number }
export type SearchResult = { video: Video; matches: SearchMatch[] }

export async function searchVideos(q: string): Promise<SearchResult[]> {
  const query = q.trim()
  if (!query) return []
  const res = await fetch(`/api/search?q=${encodeURIComponent(query)}`, { credentials: 'same-origin' })
  if (!res.ok) throw new Error(`search failed: ${res.status}`)
  const body = await res.json()
  return body.results ?? []
}

export async function resummarize(id: string): Promise<void> {
  const res = await fetch(`/api/videos/${encodeURIComponent(id)}/resummarize`, { method: 'POST', credentials: 'same-origin' })
  if (!res.ok && res.status !== 202) throw new Error(`resummarize failed: ${res.status}`)
}

export function subtitlesUrl(id: string): string {
  return `/api/videos/${encodeURIComponent(id)}/subtitles`
}
```

Re-export from `index.ts`. (Match the existing files' fetch idiom — if the codebase uses a shared `http.ts` wrapper, route these through it instead of raw `fetch`, keeping the blank-query short-circuit.)

- [ ] **Step 4: Run to verify it passes**

Run: `cd ui && npx vitest run src/api/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add ui/src/api/
git commit -m "feat(ui/api): video summary fields, semantic search + resummarize + subtitles helpers"
```

---

## Task 17: Frontend — Player intelligence panels + captions

**Files:**
- Modify: `ui/src/views/Player.tsx`, `ui/src/views/Player.test.tsx`, `ui/src/index.css`

**Interfaces:**
- Consumes: `Video.summary/chapters/key_points/summary_status`, `subtitlesUrl`, `resummarize`.
- Produces: the mixed layout from the approved mockup — sidebar (Summary + Highlights) beside the video, Contents + collapsible Transcript below it, a `<track>` + CC toggle, click-to-seek on chapters/highlights/cues, an in-player transcript find. Replaces the three "Coming in a later update" placeholders.

- [ ] **Step 1: Write the failing test.** Render `Player` with a fixture video (summary_status `done`, one chapter, one key point). Assert: summary text present; a chapter button click calls the `<video>` seek (spy on `currentTime`); the CC toggle flips the track mode; a `no_transcript` fixture shows "No transcript available".

```tsx
it('renders summary, seeks on chapter click, toggles CC', async () => {
  const video = makeVideo({ summary_status: 'done', summary: 'Prose.', chapters: [{ ts: 108, title: 'Frame', source: 'yt-dlp' }], key_points: [{ ts: 12, text: 'wow' }], has_subtitles: true })
  render(<Player video={video} /* + required props */ />)
  expect(screen.getByText('Prose.')).toBeInTheDocument()
  // chapter seek
  const vid = document.querySelector('video') as HTMLVideoElement
  const seekSpy = vi.spyOn(vid, 'currentTime', 'set')
  fireEvent.click(screen.getByRole('button', { name: /Frame/ }))
  expect(seekSpy).toHaveBeenCalledWith(108)
})

it('shows no-transcript state', () => {
  render(<Player video={makeVideo({ summary_status: 'no_transcript' })} /* props */ />)
  expect(screen.getByText(/No transcript available/i)).toBeInTheDocument()
})
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd ui && npx vitest run src/views/Player.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement.** Replace the three placeholder blocks with the panels. Key pieces (adapt to the existing Player structure/props):

- Add a `<track kind="subtitles" srcLang={video.audio_language || 'en'} src={subtitlesUrl(video.id)} default={false} />` inside the existing `<video>`, plus a CC toggle button that flips `videoRef.current.textTracks[0].mode` between `'showing'`/`'hidden'`.
- A `seek(ts:number)` helper: `videoRef.current.currentTime = ts`.
- Summary panel: `summary_status==='done'` → paragraphs from `video.summary` (split on `\n\n`); `'no_transcript'` → "No transcript available"; `'pending'|'running'` → "Summarizing…"; `'error'` → an error line + a "Re-summarize" button calling `resummarize(video.id)`.
- Contents: `video.chapters.map(c => <button onClick={()=>seek(c.ts)}>{fmt(c.ts)} {c.title}{c.source==='yt-dlp' && <span>yt-dlp</span>}</button>)`.
- Highlights: `video.key_points.map(k => <button onClick={()=>seek(k.ts)}>★ {fmt(k.ts)} {k.text}</button>)`.
- Transcript: collapsible; a text input filters cue lines (fetch the VTT via `subtitlesUrl` and parse client-side, OR reuse `video`-embedded cues if you expose them — for P3, fetch the VTT text and split into `NN:NN\ttext` rows using a small client parser). Clicking a cue seeks.

Follow the mockup's class names/structure (`leftcol`, `belowvideo`, `card`, `toc-grid`, `hl`, `transcript`) and add the corresponding CSS to `index.css` (copy the relevant rules from the mockup file `peeq-p3-mockup.html`). Keep the design tokens already in `index.css`.

- [ ] **Step 4: Run to verify it passes**

Run: `cd ui && npx vitest run src/views/Player.test.tsx`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add ui/src/views/Player.tsx ui/src/views/Player.test.tsx ui/src/index.css
git commit -m "feat(ui/player): summary/contents/highlights panels, captions + CC toggle, transcript find"
```

---

## Task 18: Frontend — Global search view + rail item

**Files:**
- Create: `ui/src/views/Search.tsx`, `ui/src/views/Search.test.tsx`
- Modify: `ui/src/shell/Rail.tsx`, `ui/src/App.tsx`

**Interfaces:**
- Consumes: `searchVideos`.
- Produces: a `Search` view — query box → results grouped by video with matched moments (snippet + timestamp) that navigate to the player at that timestamp. A "Search" rail item routes to it.

- [ ] **Step 1: Write the failing test.** Render `Search`, type a query, mock `searchVideos` to return one grouped result, assert the video title + a match snippet render and clicking a match invokes the navigation prop with `(videoId, startSeconds)`.

```tsx
it('shows results and navigates to a moment', async () => {
  vi.mock('../api/search', () => ({ searchVideos: vi.fn().mockResolvedValue([
    { video: { id: 'v1', title: 'iPhone 27 review' }, matches: [{ start_seconds: 560, snippet: 'the new iPhone', distance: 0.1 }] },
  ]) }))
  const onOpen = vi.fn()
  render(<Search onOpen={onOpen} />)
  fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: 'iphone' } })
  fireEvent.submit(screen.getByRole('search') ?? screen.getByRole('form'))
  expect(await screen.findByText('iPhone 27 review')).toBeInTheDocument()
  fireEvent.click(screen.getByText(/the new iPhone/))
  expect(onOpen).toHaveBeenCalledWith('v1', 560)
})
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd ui && npx vitest run src/views/Search.test.tsx`
Expected: FAIL.

- [ ] **Step 3: Implement** `Search.tsx` (query box + results list, styled per the mockup's `.result`/`.match` classes; add those rules to `index.css`). Wire a "Search" item into `Rail.tsx` (lucide search icon) and route it in `App.tsx`'s view switch. The `onOpen(videoId, startSeconds)` prop opens the Player and seeks (App holds the current-video + a pending-seek state the Player consumes on mount).

- [ ] **Step 4: Run to verify it passes**

Run: `cd ui && npx vitest run src/views/Search.test.tsx && npx vitest run`
Expected: PASS (whole FE suite green).

- [ ] **Step 5: Commit**

```bash
git add ui/src/views/Search.tsx ui/src/views/Search.test.tsx ui/src/shell/Rail.tsx ui/src/App.tsx ui/src/index.css
git commit -m "feat(ui): global semantic search view + rail item, jump-to-moment"
```

---

## Task 19: Docs — env, README, AGENTS.md

**Files:**
- Modify: `.env.example`, `README.md`, `AGENTS.md`

**Interfaces:** none (documentation).

- [ ] **Step 1: Update `.env.example`** — add, with comments marking the first three as required:

```bash
# --- Phase 3: AI (required — peeq will not boot without these) ---
BACKEND_MIMO_BASE_URL=http://localhost:8000/v1
BACKEND_MIMO_API_KEY=
BACKEND_EMBED_BASE_URL=http://localhost:8001/v1
BACKEND_EMBED_API_KEY=
BACKEND_EMBED_MODEL=text-embedding-3-small
BACKEND_EMBED_DIM=1536
BACKEND_DEFAULT_SUB_LANG=en
```

- [ ] **Step 2: Update `README.md`** — a "Subtitles, summaries & search" section: the new required env vars; that a changed `BACKEND_EMBED_DIM` requires recreating the DB (`rm ./data/peeq.db*`); and extend the manual operator checklist with the P3 end-to-end (real cookie + running MiMo + embeddings: download a video, confirm VTT/CC, three artifacts populate, a global search hits the right timestamp).

- [ ] **Step 3: Update `AGENTS.md`** — one line under conventions: "Phase 3 requires MiMo + embeddings endpoints (`BACKEND_MIMO_*`, `BACKEND_EMBED_*`); tests fake them with httptest — never call a real LLM/embeddings endpoint or the real yt-dlp binary." Keep the file lean (≤5k chars).

- [ ] **Step 4: Commit**

```bash
git add .env.example README.md AGENTS.md
git commit -m "docs: phase-3 AI env vars, dim-change caveat, operator checklist"
```

---

## Task 20: Whole-branch verification

**Files:** none (verification gate).

- [ ] **Step 1: Backend full suite with race**

Run: `cd backend && gofmt -l . && go vet ./... && go test ./... -race`
Expected: gofmt prints nothing; vet clean; all tests PASS.

- [ ] **Step 2: Frontend build + tests**

Run: `cd ui && npx vitest run && npm run build`
Expected: all tests PASS; build succeeds (SPA embeds into `backend/web/dist`).

- [ ] **Step 3: Boot smoke (fatal-config check).** With the AI vars unset, `go run ./cmd/peeq` must exit non-zero with a clear "BACKEND_MIMO_BASE_URL is required"-class error. With them set (dummy URLs) + `BACKEND_AUTH_MODE=dev` on loopback, it boots and logs "summarize worker started".

- [ ] **Step 4: Final commit if any fixups**

```bash
git add -A && git commit -m "chore(phase-3): whole-branch verification fixups" || true
```

---

## Self-Review

**Spec coverage:**
- §1 config required-at-boot → Task 2, Task 20 step 3. §3 schema (all columns/tables, dim guard) → Task 1, Task 5 (`BuiltDim`), Task 15 (boot warn). §5 subtitle acquisition + prefer-yt-dlp-chapters → Task 9, Task 11. §6 map-reduce + no_transcript + embed → Tasks 11, 12; tombstone keeps chunks (delete-only-media leaves rows; full delete → `DeleteVideoChunks`, wired where the existing delete handler runs — **note:** Task 14 does not re-wire `handleDeleteVideo`; add `s.deps.Rag.DeleteVideoChunks` there if the existing delete should purge chunks — see gap below). §7 endpoints (subtitles, search, resummarize, video fields) → Tasks 13, 14. §8 player + search UI → Tasks 17, 18. §9 testing (fakes) → every task. §10 reuse ledger → Tasks 3–5. §2 Phase-3.1 deferrals → not built (correct).
- **Gap found & noted:** full video deletion should purge `transcript_chunks`/`vec_chunks`. `transcript_chunks` has `ON DELETE CASCADE` on `videos(id)`, so the chunk rows go automatically — **but `vec_chunks` (vec0) is NOT reachable by cascade**, so its rows would leak. **Fix:** in Task 14, extend `handleDeleteVideo` (in `videos_handlers.go`) to call `s.deps.Rag.DeleteVideoChunks(ctx, id)` before/after the row delete. Add this to Task 14 Step 3 and a test asserting `vec_chunks` is empty after delete.

**Placeholder scan:** the FE test bodies in Tasks 17/18 and the download-worker test in Task 10 describe assertions in comments; when implementing, write the concrete assertion shown in the interface/step text (the exact fields are specified). No `TODO`/`TBD` remain in implementation code.

**Type consistency:** `Chapter{ts,title,source}` / `KeyPoint{ts,text}` identical across summarize (Task 11), videos JSON (Task 14), FE types (Task 16). `SubLang` on `DownloadReq` (Task 9) set by the worker (Task 10). `Embed(ctx,[]string)([][]float32,error)` identical across rag (Task 4), summarize `Embedder` (Task 12), httpapi `SearchEmbedder` (Task 14). `Enqueue(videoID)(int64,error)` identical across summaryjobs (Task 7), download `SummaryEnqueuer` (Task 10), httpapi `SummaryEnqueuer` (Task 14).

**Apply the two inline fixes above during Task 14** (delete-purges-vec_chunks) — folded into that task's scope.
