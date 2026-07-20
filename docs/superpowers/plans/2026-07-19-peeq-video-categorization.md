# peeq Video Categorization + Library Filter — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** During the summarize job, classify each video into exactly one fixed-enum category via the existing MiMo client, store it, and let the user filter the Library by category.

**Architecture:** A fixed category enum lives in the `videos` Go package (the authority) and is mirrored in one TS module. The summarize worker, after storing the summary, makes one extra constrained MiMo completion (`title` + `summary`) whose reply is normalized against the enum (invalid/empty/error → `uncategorized`) and stored on the video via a new `SetCategory`. `videos.Store.List` gains an orthogonal category filter; the list endpoint accepts `?category=`. The Library adds a category chip row; VideoCard shows a bottom-left thumbnail badge.

**Tech Stack:** Go (`net/http`, `ncruces/go-sqlite3`, `CGO_ENABLED=0`), React + TypeScript (Vite), existing MiMo `llm.Client`, httptest/fake test doubles.

## Global Constraints

- `CGO_ENABLED=0` for all Go builds/tests; pure-Go sqlite.
- Env prefix is `BACKEND_`, never `PEEQ_`.
- No new YouTube/subtitle path; the classify call reuses the existing MiMo `llm.Client` through the same throttle/pause choke point as summarize. Tests use fakes only — never a real LLM/embeddings/yt-dlp.
- Single **in-place** migration: edit `backend/internal/store/migrations/0001_init.sql` (DB recreated on upgrade; no prod DB, no backfill). Do NOT add `0002_*.sql`.
- English code comments; do not remove existing comments/features. Swiss orthography only applies to German text (none here).
- Backend tests run with `-race`. Work lands on branch `feat/video-categorization` → PR to `master`. Conventional commits.
- Category ids are the 15 stable strings in Task 1; AI is its own first-class category. `uncategorized` is the fallback.

---

### Task 1: Category enum + normalization (Go)

**Files:**
- Create: `backend/internal/videos/category.go`
- Test: `backend/internal/videos/category_test.go`

**Interfaces:**
- Produces:
  - `type Category struct { ID string; Label string }`
  - `var Categories []Category` (15 entries, order below)
  - `const UncategorizedCategory = "uncategorized"`
  - `func CategoryIDs() []string` — all 15 ids in `Categories` order
  - `func ValidCategory(id string) bool`
  - `func NormalizeCategory(reply string) string` — trims surrounding whitespace/quotes/backticks/trailing punctuation, lowercases, returns the id if valid else `UncategorizedCategory`

- [ ] **Step 1: Write the failing test**

```go
package videos

import "testing"

func TestCategoryIDsCoverAllAndIncludeAI(t *testing.T) {
	ids := CategoryIDs()
	if len(ids) != 15 {
		t.Fatalf("want 15 categories, got %d", len(ids))
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	for _, want := range []string{"ai", "uncategorized"} {
		if !seen[want] {
			t.Fatalf("missing id %q", want)
		}
	}
}

func TestValidCategory(t *testing.T) {
	if !ValidCategory("ai") {
		t.Fatal("ai should be valid")
	}
	if ValidCategory("nope") {
		t.Fatal("nope should be invalid")
	}
}

func TestNormalizeCategory(t *testing.T) {
	cases := map[string]string{
		"ai":            "ai",
		"AI":            "ai",
		"  software  ":  "software",
		"\"news\".":     "news",
		"`gaming`":      "gaming",
		"":              "uncategorized",
		"not-a-real-id": "uncategorized",
		"Science & Research": "uncategorized", // labels are not ids
	}
	for in, want := range cases {
		if got := NormalizeCategory(in); got != want {
			t.Fatalf("NormalizeCategory(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./internal/videos/ -run 'TestCategory|TestValidCategory|TestNormalizeCategory' -v`
Expected: FAIL (undefined: CategoryIDs, ValidCategory, NormalizeCategory)

- [ ] **Step 3: Write minimal implementation**

```go
// Package videos: category.go defines the fixed video-category enum (the
// authority; the TS side mirrors it) plus reply normalization. AI is a
// first-class category, deliberately split from general technology.
package videos

import "strings"

// Category is one entry of the fixed enum. ID is the stable machine string
// stored on videos.category; Label is display-only.
type Category struct {
	ID    string
	Label string
}

// UncategorizedCategory is the fallback id: used for no-transcript videos and
// for any classifier reply that isn't an exact enum id.
const UncategorizedCategory = "uncategorized"

// Categories is the fixed, ordered enum. Order drives the Library chip order.
var Categories = []Category{
	{"ai", "AI"},
	{"tech", "Technology & Gadgets"},
	{"software", "Software & Programming"},
	{"science", "Science & Research"},
	{"space", "Space & Astronomy"},
	{"engineering", "Engineering & Making"},
	{"business", "Business & Finance"},
	{"news", "News & Current Events"},
	{"history", "History & Culture"},
	{"health", "Health & Medicine"},
	{"nature", "Nature & Environment"},
	{"education", "Education & Tutorials"},
	{"gaming", "Gaming"},
	{"entertainment", "Entertainment & Music"},
	{"uncategorized", "Uncategorized"},
}

// CategoryIDs returns every id in Categories order.
func CategoryIDs() []string {
	ids := make([]string, len(Categories))
	for i, c := range Categories {
		ids[i] = c.ID
	}
	return ids
}

// ValidCategory reports whether id is an exact enum id.
func ValidCategory(id string) bool {
	for _, c := range Categories {
		if c.ID == id {
			return true
		}
	}
	return false
}

// NormalizeCategory maps a raw model reply to a valid id: it trims
// surrounding whitespace, quotes, backticks, and trailing punctuation, then
// lowercases. Returns the id when valid, else UncategorizedCategory.
func NormalizeCategory(reply string) string {
	s := strings.ToLower(strings.TrimSpace(reply))
	s = strings.Trim(s, " \t\n\r\"'`.,:;!")
	if ValidCategory(s) {
		return s
	}
	return UncategorizedCategory
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./internal/videos/ -run 'TestCategory|TestValidCategory|TestNormalizeCategory' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/videos/category.go backend/internal/videos/category_test.go
git commit -m "feat(videos): fixed category enum + reply normalization"
```

---

### Task 2: Migration column + Video field + store (SetCategory, List category filter)

**Files:**
- Modify: `backend/internal/store/migrations/0001_init.sql` (videos CREATE TABLE, after `embed_dim`)
- Modify: `backend/internal/videos/store.go` (Video struct, `videoColumns`, `scanVideo`, `List`, add `SetCategory`)
- Modify: `backend/internal/httpapi/videos_handlers.go:128` (the one production `List` caller)
- Test: `backend/internal/videos/store_test.go`

**Interfaces:**
- Consumes: `UncategorizedCategory`, `CategoryIDs` (Task 1).
- Produces:
  - `videos.Video` gains `Category string`
  - `func (s *Store) SetCategory(id, category string) error`
  - `func (s *Store) List(filter, category string) ([]Video, error)` — **signature changes**: category "" or "all" or unknown ⇒ no category constraint; otherwise `AND category = ?` (orthogonal to the status filter)

- [ ] **Step 1: Write the failing test** (append to `store_test.go`)

```go
func TestSetCategoryAndListByCategory(t *testing.T) {
	s := newTestStore(t) // existing helper in store_test.go
	mustInsertVideo(t, s, "v-ai")   // existing helper; inserts a downloaded video
	mustInsertVideo(t, s, "v-news")

	if err := s.SetCategory("v-ai", "ai"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCategory("v-news", "news"); err != nil {
		t.Fatal(err)
	}

	// Default before SetCategory is uncategorized; verify round-trip.
	got, err := s.Get("v-ai")
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != "ai" {
		t.Fatalf("category = %q, want ai", got.Category)
	}

	// Category filter, orthogonal to status.
	ai, err := s.List("all", "ai")
	if err != nil {
		t.Fatal(err)
	}
	if len(ai) != 1 || ai[0].ID != "v-ai" {
		t.Fatalf("List all/ai = %v, want [v-ai]", ai)
	}

	// Empty / "all" category ⇒ no constraint.
	all, err := s.List("all", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("List all/'' returned %d, want 2", len(all))
	}
}
```

> If `newTestStore`/`mustInsertVideo` helper names differ in `store_test.go`, use whatever the existing tests use to open a migrated store and insert a video row; the assertions are what matter.

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./internal/videos/ -run TestSetCategoryAndListByCategory -v`
Expected: FAIL (SetCategory undefined; List takes 1 arg)

- [ ] **Step 3a: Add the column to the migration**

In `backend/internal/store/migrations/0001_init.sql`, change the last two lines of `CREATE TABLE videos` from:

```sql
    embed_model              TEXT NOT NULL DEFAULT '',
    embed_dim                INTEGER NOT NULL DEFAULT 0
);
```

to:

```sql
    embed_model              TEXT NOT NULL DEFAULT '',
    embed_dim                INTEGER NOT NULL DEFAULT 0,
    -- category: fixed-enum classification (see internal/videos/category.go).
    -- Plain TEXT (no CHECK): the enum lives in Go and app-side
    -- NormalizeCategory guarantees a valid id or 'uncategorized' before write.
    category                 TEXT NOT NULL DEFAULT 'uncategorized'
);
```

- [ ] **Step 3b: Add the struct field** — in `store.go`, add to `type Video struct` after `EmbedDim int`:

```go
	Category string
```

- [ ] **Step 3c: Add the column to `videoColumns`** — append `category` as the final column:

```go
	audio_language, subtitle_path, summary, chapters, key_points, summary_status, summary_error, embed_model, embed_dim, category`
```

- [ ] **Step 3d: Scan it** — in `scanVideo`, add `&v.Category` as the final Scan arg (after `&v.EmbedDim`):

```go
		&v.SummaryStatus, &v.SummaryError, &v.EmbedModel, &v.EmbedDim, &v.Category,
	)
```

- [ ] **Step 3e: Extend `List`** — change the signature and add the category clause:

```go
// List returns videos matching filter (status dimension) and category
// (empty/"all"/unknown ⇒ no category constraint), newest first. The two
// dimensions are orthogonal and both apply when set.
func (s *Store) List(filter, category string) ([]Video, error) {
	conds := []string{}
	args := []any{}
	switch filter {
	case "unwatched":
		conds = append(conds, "status = 'downloaded' AND watched = 0")
	case "watched":
		conds = append(conds, "watched = 1")
	case "favorites":
		conds = append(conds, "favorite = 1")
	case "downloading":
		conds = append(conds, "status IN ('queued', 'downloading')")
	}
	if category != "" && category != "all" && ValidCategory(category) {
		conds = append(conds, "category = ?")
		args = append(args, category)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	rows, err := s.db.QueryContext(context.Background(),
		"SELECT "+videoColumns+" FROM videos "+where+" ORDER BY created_at DESC, id DESC",
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list videos (filter=%s, category=%s): %w", filter, category, err)
	}
	defer rows.Close()

	out := []Video{}
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, fmt.Errorf("list videos (filter=%s, category=%s): %w", filter, category, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list videos (filter=%s, category=%s): %w", filter, category, err)
	}
	return out, nil
}
```

Ensure `strings` is imported in `store.go` (add to the import block if missing).

- [ ] **Step 3f: Add `SetCategory`** — place near `SetSummary`:

```go
// SetCategory persists a video's classification. The value must already be a
// valid enum id or 'uncategorized' (callers use videos.NormalizeCategory).
func (s *Store) SetCategory(id, category string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET category = ? WHERE id = ?`, category, id)
	if err != nil {
		return fmt.Errorf("set video %s category: %w", id, err)
	}
	return nil
}
```

- [ ] **Step 3g: Update the existing List callers** — in `videos_handlers.go:128`:

```go
	all, err := s.videos.List(r.URL.Query().Get("filter"), r.URL.Query().Get("category"))
```

And in `store_test.go`, update the 5 existing `s.List("...")` calls (lines ~412–444) to pass `""` as the second arg, e.g. `s.List("all", "")`, `s.List("unwatched", "")`, etc.

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/videos/ ./internal/httpapi/ -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/store/migrations/0001_init.sql backend/internal/videos/store.go backend/internal/httpapi/videos_handlers.go backend/internal/videos/store_test.go
git commit -m "feat(videos): category column, SetCategory, and List category filter"
```

---

### Task 3: Summarizer.Classify (constrained MiMo call)

**Files:**
- Modify: `backend/internal/summarize/summarizer.go` (add `Classify`)
- Test: `backend/internal/summarize/summarizer_test.go`

**Interfaces:**
- Consumes: `Summarizer.c` (`Completer`, existing).
- Produces: `func (s *Summarizer) Classify(ctx context.Context, title, summary string, allowed []string) (string, error)` — returns the model's raw single-line reply (NOT normalized; the worker normalizes via `videos.NormalizeCategory`). Errors propagate.

- [ ] **Step 1: Write the failing test** (append to `summarizer_test.go`)

```go
func TestClassifyReturnsRawReplyAndSendsAllowedIDs(t *testing.T) {
	var gotSystem, gotUser string
	fc := completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		gotSystem = m[0].Content
		gotUser = m[1].Content
		return " ai \n", nil
	})
	s := New(fc)
	got, err := s.Classify(context.Background(), "GPT-5 is here", "A video about a new model.", []string{"ai", "news"})
	if err != nil {
		t.Fatal(err)
	}
	if got != " ai \n" {
		t.Fatalf("Classify returned %q, want raw reply unchanged", got)
	}
	if !strings.Contains(gotSystem, "ai") || !strings.Contains(gotSystem, "news") {
		t.Fatalf("system prompt missing allowed ids: %q", gotSystem)
	}
	if !strings.Contains(gotUser, "GPT-5 is here") || !strings.Contains(gotUser, "new model") {
		t.Fatalf("user content missing title/summary: %q", gotUser)
	}
}

// completerFunc adapts a func to the Completer interface for tests.
type completerFunc func(context.Context, []llm.Message) (string, error)

func (f completerFunc) Complete(ctx context.Context, m []llm.Message) (string, error) {
	return f(ctx, m)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./internal/summarize/ -run TestClassifyReturnsRawReply -v`
Expected: FAIL (Classify undefined)

- [ ] **Step 3: Write minimal implementation** — add to `summarizer.go`:

```go
// Classify asks the model to pick exactly one category id from allowed,
// given the video title and its generated summary. It returns the model's
// raw reply unchanged; the caller normalizes it against the enum (an invalid
// or empty reply must degrade to "uncategorized", not error). This is a
// cheap call: the input is the short summary, not the full transcript.
func (s *Summarizer) Classify(ctx context.Context, title, summary string, allowed []string) (string, error) {
	sys := "You classify a video into exactly one category id from this list: " +
		strings.Join(allowed, ", ") +
		". Reply with a single category id from the list and nothing else. If none fits, reply uncategorized."
	return s.c.Complete(ctx, []llm.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: "TITLE: " + title + "\n\nSUMMARY:\n" + summary},
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./internal/summarize/ -run TestClassifyReturnsRawReply -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/summarize/summarizer.go backend/internal/summarize/summarizer_test.go
git commit -m "feat(summarize): Classify picks one category id from the summary"
```

---

### Task 4: Worker classifies after storing the summary

**Files:**
- Modify: `backend/internal/summarize/worker.go` (in `processOne`, after `SetSummary`)
- Test: `backend/internal/summarize/worker_test.go`

**Interfaces:**
- Consumes: `Summarizer.Classify` (Task 3), `videos.CategoryIDs`, `videos.NormalizeCategory`, `videos.Store.SetCategory` (Tasks 1–2).
- Produces: no new exported symbols; observable effect is `videos.category` set after a successful summarize.

- [ ] **Step 1: Write the failing tests** — extend `worker_test.go`. Make the fake completer answer the classify prompt, and add two cases.

First, update `fakeWorkerCompleter.Complete` to dispatch the classify prompt (add this branch BEFORE the final `return "chunk summary"`):

```go
		if strings.Contains(sys, "category id") {
			return "ai", nil
		}
```

Then add tests:

```go
func TestProcessOneSetsCategory(t *testing.T) {
	h := newWorkerHarness(t) // existing helper
	// ... existing harness setup enqueues a video with a subtitle path ...
	// Drive one job:
	did, err := h.worker.processOne(context.Background())
	if err != nil || !did {
		t.Fatalf("processOne did=%v err=%v", did, err)
	}
	v, err := h.videos.Get(h.videoID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Category != "ai" {
		t.Fatalf("category = %q, want ai", v.Category)
	}
}

// classifyErrCompleter answers summary/keypoints normally but errors on the
// classify call, proving a classify failure does NOT fail the job and leaves
// the category at its 'uncategorized' default.
type classifyErrCompleter struct{}

func (classifyErrCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	sys := m[0].Content
	switch {
	case strings.Contains(sys, "Combine these section summaries"):
		return "Overall prose summary.", nil
	case strings.Contains(sys, "category id"):
		return "", errors.New("classify boom")
	case strings.Contains(sys, "JSON"):
		return `{"key_points":[]}`, nil
	}
	return "chunk summary", nil
}

func TestClassifyErrorLeavesUncategorizedAndJobSucceeds(t *testing.T) {
	h := newWorkerHarnessWithCompleter(t, classifyErrCompleter{}) // see note
	did, err := h.worker.processOne(context.Background())
	if err != nil || !did {
		t.Fatalf("processOne did=%v err=%v", did, err)
	}
	v, err := h.videos.Get(h.videoID)
	if err != nil {
		t.Fatal(err)
	}
	if v.SummaryStatus != "done" {
		t.Fatalf("summary_status = %q, want done (classify error must not fail the job)", v.SummaryStatus)
	}
	if v.Category != "uncategorized" {
		t.Fatalf("category = %q, want uncategorized", v.Category)
	}
}
```

> Use whatever harness constructor `worker_test.go` already provides. If the existing harness hard-codes `fakeWorkerCompleter{}`, add a small variant constructor `newWorkerHarnessWithCompleter(t, Completer)` that injects a custom completer, or set the completer field on the existing harness before `processOne`. The existing no-transcript test (`failCompleter`) already proves Classify is not called without a transcript — no new test needed for that path.

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=0 go test ./internal/summarize/ -run 'TestProcessOneSetsCategory|TestClassifyErrorLeaves' -v`
Expected: FAIL (category is uncategorized / stays default because the worker doesn't classify yet)

- [ ] **Step 3: Implement** — in `worker.go` `processOne`, after the `SetSummary` block succeeds and BEFORE `w.emit(video.ID, "done", "")`:

```go
	// Classify into one fixed-enum category from the stored summary. This is
	// best-effort: a classify error or invalid reply leaves the category at
	// its 'uncategorized' default and must NOT fail the job (the summary is
	// already stored). Same MiMo client + throttle/pause path as summarize.
	if raw, cerr := w.d.Summarizer.Classify(ctx, video.Title, art.Summary, videos.CategoryIDs()); cerr != nil {
		w.d.Logger.Warn("summarize worker: classify failed", "video_id", video.ID, "err", cerr)
	} else if serr := w.d.Videos.SetCategory(video.ID, videos.NormalizeCategory(raw)); serr != nil {
		w.d.Logger.Error("summarize worker: set category failed", "video_id", video.ID, "err", serr)
	}
```

`videos` is already imported in `worker.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/summarize/ -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/summarize/worker.go backend/internal/summarize/worker_test.go
git commit -m "feat(summarize): worker classifies video after storing summary"
```

---

### Task 5: API — expose category + honor ?category=

**Files:**
- Modify: `backend/internal/httpapi/videos_handlers.go` (`videoDTO`, `toVideoDTO`; `handleListVideos` doc comment)
- Test: `backend/internal/httpapi/videos_handlers_test.go` (or the existing videos handler test file)

**Interfaces:**
- Consumes: `videos.Video.Category` (Task 2); `List(filter, category)` caller already updated in Task 2.
- Produces: `videoDTO` gains `Category string \`json:"category"\``.

- [ ] **Step 1: Write the failing test** — find the existing list-videos handler test and add a category assertion, or add:

```go
func TestListVideosFiltersByCategory(t *testing.T) {
	srv := newTestServer(t) // existing helper
	// seed two videos with categories "ai" and "news" via srv.videos.SetCategory
	// (mirror how the existing handler tests seed videos)
	req := httptest.NewRequest("GET", "/api/videos?category=ai", nil)
	rec := httptest.NewRecorder()
	srv.handleListVideos(rec, withAuth(req)) // mirror existing auth helper if any
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["category"] != "ai" {
		t.Fatalf("category=ai returned %v", got)
	}
}
```

> Match the existing videos-handler test's server/auth/seed helpers exactly; the assertion (one row, `category":"ai"`) is the point.

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./internal/httpapi/ -run TestListVideosFiltersByCategory -v`
Expected: FAIL (category not in JSON / not filtered)

- [ ] **Step 3: Implement**

Add to `videoDTO` (after `SummaryStatus`):

```go
	Category              string                   `json:"category"`
```

In `toVideoDTO`, set it:

```go
	dto.Category = v.Category
```

(use the actual local variable name `toVideoDTO` builds — set `Category: v.Category` in the struct literal if it uses one.)

Update the `handleListVideos` doc comment to mention `?category=<id>` (orthogonal to `?filter=`; unknown/empty ⇒ all). The filtering itself already works via the Task 2 `List(filter, category)` caller.

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/httpapi/ -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/httpapi/videos_handlers.go backend/internal/httpapi/videos_handlers_test.go
git commit -m "feat(api): videos DTO carries category; list honors ?category="
```

---

### Task 6: Scan-classify ErrNoCookie cleanup

**Files:**
- Modify: `backend/internal/scan/scheduler.go` (the classify `switch` around lines 181–208)
- Test: `backend/internal/scan/scheduler_test.go`

**Interfaces:**
- Consumes: `ytdlp.ErrNoCookie`.
- Produces: no new symbols; behavior change = an `ErrNoCookie` scan failure no longer feeds `FailMonitor` (race-only, self-limiting — the cookie gate already stops scanning), mirroring the download worker's special-casing.

- [ ] **Step 1: Write the failing test** — mirror the existing scan-classify test that asserts a terminal/paused error does NOT call FailMonitor. Add:

```go
func TestScanErrNoCookieDoesNotCountTowardAutoPause(t *testing.T) {
	// Arrange a scheduler harness whose upload-lister returns ytdlp.ErrNoCookie,
	// with a spy FailMonitor (mirror the existing terminal-error test's setup).
	// Act: run one scan pass.
	// Assert: the spy FailMonitor.Fail was NOT called.
}
```

> Copy the exact harness/spy from the existing "terminal error is not counted" test in `scheduler_test.go` and swap the injected error for `ytdlp.ErrNoCookie`; assert `failCount == 0`.

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./internal/scan/ -run TestScanErrNoCookie -v`
Expected: FAIL (ErrNoCookie currently falls into `default` → FailMonitor.Fail called once)

- [ ] **Step 3: Implement** — add a case to the classify `switch` in `scheduler.go`, alongside `ErrPaused` (which also does nothing):

```go
		case errors.Is(err, ytdlp.ErrNoCookie):
			// No cookie at all: race-only and self-limiting — the scheduler's
			// own cookie gate stops scanning next pass — so it must not count
			// toward the shared auto-pause heuristic, mirroring the download
			// worker's classify. Leave cookie_status ('absent') as-is.
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/scan/ -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add backend/internal/scan/scheduler.go backend/internal/scan/scheduler_test.go
git commit -m "fix(scan): ErrNoCookie no longer counts toward auto-pause (mirror worker)"
```

---

### Task 7: UI — categories module, types, api param

**Files:**
- Create: `ui/src/categories.ts`
- Modify: `ui/src/api/types.ts` (`Video` gains `category`; add a `CategoryFilter` alias if useful)
- Modify: `ui/src/api/videos.ts` (`listVideos` gains optional category arg)
- Test: `ui/src/categories.test.ts`

**Interfaces:**
- Produces:
  - `export type CategoryMeta = { id: string; label: string; color: string }`
  - `export const CATEGORIES: CategoryMeta[]` — 15 entries mirroring Go `Categories` order
  - `export const CATEGORY_BY_ID: Record<string, CategoryMeta>`
  - `export const UNCATEGORIZED = "uncategorized"`
  - `Video.category: string`
  - `listVideos(filter?, category?)`

- [ ] **Step 1: Write the failing test**

```ts
import { describe, expect, it } from "vitest";
import { CATEGORIES, CATEGORY_BY_ID, UNCATEGORIZED } from "./categories";

describe("categories", () => {
  it("has 15 entries including ai and uncategorized", () => {
    expect(CATEGORIES).toHaveLength(15);
    expect(CATEGORY_BY_ID["ai"].label).toBe("AI");
    expect(CATEGORY_BY_ID[UNCATEGORIZED].label).toBe("Uncategorized");
  });
  it("every entry has a color", () => {
    for (const c of CATEGORIES) expect(c.color).toMatch(/^#/);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ui && npx vitest run src/categories.test.ts`
Expected: FAIL (cannot find module ./categories)

- [ ] **Step 3: Implement**

`ui/src/categories.ts`:

```ts
// Mirrors backend/internal/videos/category.go (the authority). Colors are a
// muted scanning aid used only as small dots; ai uses the warm accent, the
// fallback uses --color-faint. Keep this list in sync with the Go enum.
export type CategoryMeta = { id: string; label: string; color: string };

export const UNCATEGORIZED = "uncategorized";

export const CATEGORIES: CategoryMeta[] = [
  { id: "ai", label: "AI", color: "#d97757" },
  { id: "tech", label: "Technology & Gadgets", color: "#5aa0c8" },
  { id: "software", label: "Software & Programming", color: "#7c9cff" },
  { id: "science", label: "Science & Research", color: "#5ac89a" },
  { id: "space", label: "Space & Astronomy", color: "#9c7cdc" },
  { id: "engineering", label: "Engineering & Making", color: "#d6a15a" },
  { id: "business", label: "Business & Finance", color: "#7cc86a" },
  { id: "news", label: "News & Current Events", color: "#c8607a" },
  { id: "history", label: "History & Culture", color: "#c89a5a" },
  { id: "health", label: "Health & Medicine", color: "#6ac8b4" },
  { id: "nature", label: "Nature & Environment", color: "#6aa86a" },
  { id: "education", label: "Education & Tutorials", color: "#8a9ac8" },
  { id: "gaming", label: "Gaming", color: "#b06adc" },
  { id: "entertainment", label: "Entertainment & Music", color: "#dc6a9c" },
  { id: "uncategorized", label: "Uncategorized", color: "#6f6d66" },
];

export const CATEGORY_BY_ID: Record<string, CategoryMeta> = Object.fromEntries(
  CATEGORIES.map((c) => [c.id, c]),
);
```

`types.ts`: add `category: string;` to the `Video` type (after `has_subtitles`, or near `summary_status`).

`videos.ts`: change `listVideos`:

```ts
export async function listVideos(filter: VideoFilter = "all", category = "all"): Promise<Video[]> {
  const q = new URLSearchParams({ filter });
  if (category && category !== "all") q.set("category", category);
  return api.get<Video[]>(`/api/videos?${q.toString()}`, "failed to load videos");
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ui && npx vitest run src/categories.test.ts`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add ui/src/categories.ts ui/src/categories.test.ts ui/src/api/types.ts ui/src/api/videos.ts
git commit -m "feat(ui): categories module, Video.category, listVideos category param"
```

---

### Task 8: UI — Library category chip row + filtering

**Files:**
- Modify: `ui/src/views/Library.tsx` (category state + second chip row + pass category to `listVideos`)
- Modify: `ui/src/index.css` (add `.catchips`/`.catchip` styles)
- Test: `ui/src/views/Library.test.tsx`

**Interfaces:**
- Consumes: `CATEGORIES`, `CATEGORY_BY_ID` (Task 7), `listVideos(filter, category)`.
- Produces: none (view behavior).

**Design notes:**
- Add `const [category, setCategory] = useState<string>("all")`.
- Category chip row renders below the existing `.chips` div: an "All categories" chip plus one chip per category **with ≥1 video in the unfiltered `allVideos` list** (hide empty categories); each shows a color dot + label + count computed from `allVideos.filter(v => v.category === c.id).length`.
- Selecting a category calls `setCategory(id)`; the active-chip fetch effect must depend on **both** `filter` and `category` and call `listVideos(filter, category)`. Update the effect dependency array and the two `listVideos(filter)` calls in the polling/redownload paths to `listVideos(filter, category)`.
- Category filtering is client-independent of status: both dimensions apply server-side.

- [ ] **Step 1: Write the failing test** — add to `Library.test.tsx`, mirroring the existing status-chip test:

```tsx
it("renders a category chip row and filters by category", async () => {
  // Arrange: mock listVideos so the "all" call returns videos across
  // categories (ai, news), and a category call returns only that category.
  // (Mirror how Library.test.tsx already mocks listVideos per filter.)
  render(<Library onOpenVideo={() => {}} />);
  // The AI category chip appears (from the unfiltered list).
  const aiChip = await screen.findByRole("button", { name: /AI/ });
  fireEvent.click(aiChip);
  // Only the AI video remains.
  await waitFor(() => {
    expect(screen.queryByText(/news video title/i)).not.toBeInTheDocument();
  });
});
```

> Use the file's existing mocking style for `../api` / `listVideos`. Assert on real sample titles you seed.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ui && npx vitest run src/views/Library.test.tsx`
Expected: FAIL (no category chips)

- [ ] **Step 3: Implement**

Add category state and the chip row. After the existing `.chips` block in the returned JSX:

```tsx
      <div className="catchips">
        <button
          type="button"
          className={`catchip${category === "all" ? " on" : ""}`}
          onClick={() => setCategory("all")}
        >
          All categories <span className="n">{allVideos.length}</span>
        </button>
        {CATEGORIES.filter((c) => allVideos.some((v) => v.category === c.id)).map((c) => (
          <button
            key={c.id}
            type="button"
            className={`catchip${category === c.id ? " on" : ""}`}
            onClick={() => setCategory(c.id)}
          >
            <span className="dotc" style={{ background: c.color }} />
            {c.label} <span className="n">{allVideos.filter((v) => v.category === c.id).length}</span>
          </button>
        ))}
      </div>
```

Add the import: `import { CATEGORIES } from "../categories";`

Change the active-chip fetch effect to depend on `category` and pass it:

```tsx
  useEffect(() => {
    let active = true;
    setError(null);
    listVideos(filter, category)
      .then((v) => {
        if (active) setVideos(v);
      })
      .catch((e: Error) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
    };
  }, [filter, category]);
```

Also update the two other `listVideos(filter)` calls (the 3s poller and `handleRedownload`) to `listVideos(filter, category)`, and add `category` to the poller effect's dependency array (alongside `filter`, `jobsRefreshTick`).

`index.css` — add near the `.chips` block:

```css
.catchips {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  margin: -10px 0 22px;
}
.catchip {
  padding: 6px 13px;
  border-radius: 20px;
  border: 1px solid var(--color-border-soft);
  background: transparent;
  color: var(--color-muted);
  font-size: 13px;
  font-weight: 500;
  display: inline-flex;
  align-items: center;
  gap: 7px;
  cursor: pointer;
}
.catchip:hover {
  color: var(--color-ink-dim);
  border-color: var(--color-faint);
}
.catchip.on {
  background: color-mix(in srgb, var(--color-accent) 16%, transparent);
  color: var(--color-accent-strong);
  border-color: var(--color-accent-dim);
}
.catchip .dotc {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  flex-shrink: 0;
}
.catchip .n {
  font-family: var(--font-mono);
  font-size: 10.5px;
  opacity: 0.65;
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd ui && npx vitest run src/views/Library.test.tsx`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add ui/src/views/Library.tsx ui/src/index.css ui/src/views/Library.test.tsx
git commit -m "feat(ui): Library category chip row filters the grid"
```

---

### Task 9: UI — VideoCard category pill (meta line)

**Files:**
- Modify: `ui/src/components/VideoCard.tsx` (pill in the `.by` meta line)
- Modify: `ui/src/index.css` (`.metapill`)
- Test: `ui/src/components/VideoCard.test.tsx` (or existing card test)

**Interfaces:**
- Consumes: `CATEGORY_BY_ID`, `UNCATEGORIZED` (Task 7), `Video.category`.

- [ ] **Step 1: Write the failing test** — add to the VideoCard test:

```tsx
it("shows a category badge when categorized, hides it when uncategorized", () => {
  const base = makeVideo({ category: "ai" }); // use the file's video factory
  const { rerender } = render(<VideoCard video={base} retentionDays={14} onOpen={() => {}} onToggleFavorite={() => {}} onToggleWatched={() => {}} />);
  expect(screen.getByText("AI")).toBeInTheDocument();
  rerender(<VideoCard video={makeVideo({ category: "uncategorized" })} retentionDays={14} onOpen={() => {}} onToggleFavorite={() => {}} onToggleWatched={() => {}} />);
  expect(screen.queryByText("Uncategorized")).not.toBeInTheDocument();
});
```

> Use the existing test's video-object helper and prop set; if there's no factory, build a minimal `Video` inline with `category` set.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ui && npx vitest run src/components/VideoCard.test.tsx`
Expected: FAIL (no badge rendered)

- [ ] **Step 3: Implement**

Add the import: `import { CATEGORY_BY_ID, UNCATEGORIZED } from "../categories";`

In the `.by` meta line (which renders channel name + published date), append the pill after the existing published-date fragment, rendered only when categorized. The pill sits inside the `.by` flex row alongside the `·` dot separators:

```tsx
      <div className="by">
        {video.channel_name || video.channel_id}
        {video.published_at ? (
          <>
            <span className="dot">·</span>
            {new Date(video.published_at).toLocaleDateString()}
          </>
        ) : null}
        {video.category && video.category !== UNCATEGORIZED && CATEGORY_BY_ID[video.category] ? (
          <>
            <span className="dot">·</span>
            <span className="metapill">
              <span className="dotc" style={{ background: CATEGORY_BY_ID[video.category].color }} />
              {CATEGORY_BY_ID[video.category].label}
            </span>
          </>
        ) : null}
      </div>
```

> This replaces the existing `.by` block — keep the channel-name and published-date logic exactly as-is; only the category fragment is new.

`index.css` — add near the `.card .by` block:

```css
.metapill {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 2px 8px;
  border-radius: 12px;
  font-size: 11px;
  font-weight: 600;
  border: 1px solid var(--color-border);
  background: var(--color-panel);
  color: var(--color-ink-dim);
}
.metapill .dotc {
  width: 6px;
  height: 6px;
  border-radius: 50%;
}
```

> The pill lives in the meta line (not on the thumbnail), so it never competes with the `NEW` tag, duration, or hover actions.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd ui && npx vitest run src/components/VideoCard.test.tsx`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add ui/src/components/VideoCard.tsx ui/src/index.css ui/src/components/VideoCard.test.tsx
git commit -m "feat(ui): VideoCard shows a bottom-left category badge"
```

---

## Final verification (before PR)

- [ ] Backend: `cd backend && CGO_ENABLED=0 go test ./... -race` → all pass
- [ ] Backend vet: `cd backend && CGO_ENABLED=0 go vet ./...`
- [ ] UI: `cd ui && npx vitest run` → all pass
- [ ] UI build + lint/typecheck: `cd ui && npm run build` (or the repo's `npm run lint` / `tsc --noEmit` if defined)
- [ ] Whole-branch Fable review, then finishing-a-development-branch → PR to `master`, CI green (backend `-race` + UI).

## Self-review notes (coverage vs spec)

- Spec §3 taxonomy → Task 1 (Go) + Task 7 (TS), 15 ids incl. `ai` + `uncategorized`.
- Spec §4 data model (in-place `0001`, `category` column, struct field) → Task 2.
- Spec §5 classify step (separate constrained call after summary; error → uncategorized, non-fatal; no-transcript never classifies) → Tasks 3–4.
- Spec §6 filter + API (orthogonal `List(filter, category)`, `?category=`, client-side counts) → Tasks 2, 5, 8.
- Spec §7 frontend (categories module, chip row variant A, meta-line pill variant 1, muted dot colors) → Tasks 7–9.
- Spec §8 scan-classify `ErrNoCookie` cleanup → Task 6.
- Spec §9 testing → each task is TDD; §10 invariants restated in Global Constraints.
