# Channel Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Clicking a channel name anywhere in peeq opens a page for that channel — its archived videos, its newly discovered videos, and its subscription settings.

**Architecture:** The `channels` table stops meaning "channels the user tracks" and becomes a metadata cache any visited channel gets a row in; tracking moves to an explicit `tracked_at` column. Four new endpoints back a new `Channel` view with three tabs. Title search and sort are added to the video store and surfaced on both the channel page and the Library.

**Tech Stack:** Go 1.22+ (`net/http` `ServeMux` wildcards, `database/sql`, SQLite), React 19 + Vite + TypeScript, vitest + @testing-library/react.

**Design spec:** `docs/superpowers/specs/2026-07-20-peeq-channel-page-design.html` — read it before Task 1.

## Global Constraints

- **Work in the existing worktree** `/Users/jan/localgit/vark/.claude/worktrees/feat+channel-page`, branch `worktree-feat+channel-page`. Never `cd` to the main checkout.
- **Commit as `trick77@users.noreply.github.com`.** A pre-commit hook rejects the real address. Use `git -c user.email=trick77@users.noreply.github.com commit`.
- **The schema is squashed, not migrated.** Edit `backend/internal/store/migrations/0001_init.sql` in place. Do not add `0002_*.sql`. The production database will be deleted and re-created by the user.
- **Backend tests use real SQLite** in `t.TempDir()` via `openTestDB(t)` — never mock a store. Fakes are used only at the yt-dlp boundary.
- **Frontend tests** run under vitest + jsdom. `vi.mock("../api/channels", …)` mocks the module **wholesale**: every time you add an export to `ui/src/api/channels.ts`, add it to the mock factory in `ui/src/views/Channels.test.tsx` **in the same commit**, or existing tests break with `undefined is not a function`.
- **Buttons style through CSS classes, not inline styles** (`ui/src/ui.tsx` header comment). Put visual definitions in `ui/src/index.css`.
- **`.card` is defined twice in `index.css`** — a grid tile at ~line 576, a panel at ~line 1202; the panel wins by source order. Do not reuse `.card` for new channel-page surfaces. Use the `chan-*` prefix throughout.
- **Backend test command:** `cd backend && go test ./...`
- **Frontend test command:** `cd ui && npx vitest run`
- **Never claim a step passes without running the command and reading its output.**

---

### Task 1: Schema — `channels` becomes a metadata cache

**Files:**
- Modify: `backend/internal/store/migrations/0001_init.sql` (the `channels` CREATE TABLE, ~line 119)
- Modify: `backend/internal/channels/store.go` (`Channel` struct, `Upsert`, `Get`, `List`)
- Test: `backend/internal/channels/store_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `channels.Channel` gains `BannerPath string`, `Description string`, `TrackedAt string` (empty when NULL). `Store.Upsert(c Channel) error` no longer sets tracked state. New `Store.Track(channelID string, trackedAt string) error`. `Store.List(filter string)` returns only rows with `tracked_at IS NOT NULL`.

- [ ] **Step 1: Write the failing test**

Add to `backend/internal/channels/store_test.go`:

```go
// TestList_excludesCacheOnlyRows asserts a channel row that exists only as a
// metadata cache entry (never tracked by the user) is invisible to every
// ?filter= value the channels list supports. If this regresses, the user's
// Channels page fills up with channels they merely clicked on once.
func TestList_excludesCacheOnlyRows(t *testing.T) {
	s := newTestStore(t)

	if err := s.Upsert(Channel{ID: "UCcache", Name: "Cache Only"}); err != nil {
		t.Fatalf("upsert cache row: %v", err)
	}
	if err := s.Upsert(Channel{ID: "UCtracked", Name: "Tracked"}); err != nil {
		t.Fatalf("upsert tracked row: %v", err)
	}
	if err := s.Track("UCtracked", "2026-07-20 10:00:00"); err != nil {
		t.Fatalf("track: %v", err)
	}

	for _, filter := range []string{"all", "tracked", "subscribed", "autodownload"} {
		items, err := s.List(filter)
		if err != nil {
			t.Fatalf("list %s: %v", filter, err)
		}
		for _, it := range items {
			if it.ID == "UCcache" {
				t.Fatalf("filter %q returned cache-only channel", filter)
			}
		}
	}

	all, err := s.List("all")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 1 || all[0].ID != "UCtracked" {
		t.Fatalf("list all = %+v, want only UCtracked", all)
	}
}

// TestGet_returnsCacheOnlyRow asserts Get still finds a cache-only row — the
// channel page reads its metadata through Get even when untracked, so Get
// must NOT inherit List's tracked_at filter.
func TestGet_returnsCacheOnlyRow(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Channel{ID: "UCcache", Name: "Cache Only", Description: "hello"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	c, err := s.Get("UCcache")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if c == nil {
		t.Fatal("get returned nil for a cache-only row")
	}
	if c.TrackedAt != "" {
		t.Fatalf("TrackedAt = %q, want empty for an untracked row", c.TrackedAt)
	}
	if c.Description != "hello" {
		t.Fatalf("Description = %q, want %q", c.Description, "hello")
	}
}

// TestUpsert_preservesTrackedAt asserts re-caching a channel's metadata (which
// happens on every visit-triggered resolve) never silently untracks it.
func TestUpsert_preservesTrackedAt(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Channel{ID: "UCx", Name: "Before"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Track("UCx", "2026-07-20 10:00:00"); err != nil {
		t.Fatalf("track: %v", err)
	}
	if err := s.Upsert(Channel{ID: "UCx", Name: "After"}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	c, err := s.Get("UCx")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if c.TrackedAt == "" {
		t.Fatal("re-upsert cleared tracked_at")
	}
	if c.Name != "After" {
		t.Fatalf("Name = %q, want refreshed to %q", c.Name, "After")
	}
}
```

If `newTestStore` does not already exist in that file, add it:

```go
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/channels/ -run 'TestList_excludesCacheOnlyRows|TestGet_returnsCacheOnlyRow|TestUpsert_preservesTrackedAt' -v`

Expected: compile failure — `s.Track undefined`, `Channel.Description undefined`, `Channel.TrackedAt undefined`.

- [ ] **Step 3: Update the schema**

In `backend/internal/store/migrations/0001_init.sql`, replace the `channels` CREATE TABLE with:

```sql
-- channels: metadata cache for a YouTube channel. A row exists for any
-- channel peeq has looked at — including ones the user has never tracked
-- (their page is reachable from any video in the library). Tracking is
-- therefore NOT "a row exists"; it is tracked_at IS NOT NULL.
CREATE TABLE channels (
    id           TEXT PRIMARY KEY,          -- the channel UCID (UC...)
    handle       TEXT NOT NULL DEFAULT '',  -- @handle if known
    name         TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    avatar_path  TEXT NOT NULL DEFAULT '',  -- local path, relative to mediaDir
    banner_path  TEXT NOT NULL DEFAULT '',  -- local path, relative to mediaDir
    -- resolved_at: when the metadata above was last fetched from YouTube.
    -- Non-empty means "do not re-fetch", INCLUDING after a failed attempt,
    -- so an unresolvable channel is not re-fetched on every page visit.
    resolved_at  TEXT,
    -- tracked_at: when the user explicitly tracked this channel. NULL means
    -- this is a cache-only row and must not appear in the channels list.
    tracked_at   TEXT,
    added_at     TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_channels_tracked_at ON channels(tracked_at);
```

- [ ] **Step 4: Update the Go struct and queries**

In `backend/internal/channels/store.go`, replace the `Channel` struct:

```go
// Channel mirrors one row of the channels table. A Channel may exist purely
// as a metadata cache entry: TrackedAt is empty for a channel the user has
// visited but never tracked. AvatarPath and BannerPath are relative to the
// media dir (resolve them with media.SafeMediaPath before serving).
type Channel struct {
	ID          string
	Handle      string
	Name        string
	Description string
	AvatarPath  string
	BannerPath  string
	ResolvedAt  string
	TrackedAt   string
	AddedAt     string
}
```

Replace `Upsert`:

```go
// Upsert caches a channel's identity, inserting it if new or refreshing the
// resolved metadata if it already exists. It deliberately does NOT touch
// tracked_at: caching a channel's details must never track or untrack it.
// Empty fields do not overwrite stored values, so a partial refresh cannot
// blank out a name that was already known.
func (s *Store) Upsert(c Channel) error {
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO channels (id, handle, name, description, avatar_path, banner_path, resolved_at)
VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''))
ON CONFLICT(id) DO UPDATE SET
    handle      = COALESCE(NULLIF(excluded.handle, ''), channels.handle),
    name        = COALESCE(NULLIF(excluded.name, ''), channels.name),
    description = COALESCE(NULLIF(excluded.description, ''), channels.description),
    avatar_path = COALESCE(NULLIF(excluded.avatar_path, ''), channels.avatar_path),
    banner_path = COALESCE(NULLIF(excluded.banner_path, ''), channels.banner_path),
    resolved_at = COALESCE(excluded.resolved_at, channels.resolved_at)`,
		c.ID, c.Handle, c.Name, c.Description, c.AvatarPath, c.BannerPath, c.ResolvedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert channel %s: %w", c.ID, err)
	}
	return nil
}

// Track marks a cached channel as explicitly tracked by the user. It is
// idempotent: re-tracking an already-tracked channel keeps the original
// timestamp rather than resetting "tracked since".
func (s *Store) Track(channelID, trackedAt string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE channels SET tracked_at = COALESCE(tracked_at, ?) WHERE id = ?`,
		trackedAt, channelID,
	)
	if err != nil {
		return fmt.Errorf("track channel %s: %w", channelID, err)
	}
	return nil
}

// MarkResolveAttempted records that a metadata fetch was tried, whether or
// not it succeeded. Without this a permanently unresolvable channel would be
// re-fetched from YouTube on every single page visit.
func (s *Store) MarkResolveAttempted(channelID, at string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE channels SET resolved_at = ? WHERE id = ?`, at, channelID)
	if err != nil {
		return fmt.Errorf("mark resolve attempted %s: %w", channelID, err)
	}
	return nil
}
```

Replace `Get`:

```go
func (s *Store) Get(id string) (*Channel, error) {
	row := s.db.QueryRowContext(context.Background(), `
SELECT id, handle, name, description, avatar_path, banner_path,
       COALESCE(resolved_at, ''), COALESCE(tracked_at, ''), added_at
FROM channels WHERE id = ?`, id)
	var c Channel
	if err := row.Scan(&c.ID, &c.Handle, &c.Name, &c.Description,
		&c.AvatarPath, &c.BannerPath, &c.ResolvedAt, &c.TrackedAt, &c.AddedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get channel %s: %w", id, err)
	}
	return &c, nil
}
```

In `List`, change the SELECT and add the tracked guard. The base query becomes:

```go
	query := `
SELECT c.id, c.handle, c.name, c.description, c.avatar_path, c.banner_path,
       COALESCE(c.resolved_at, ''), COALESCE(c.tracked_at, ''), c.added_at,
       s.channel_id IS NOT NULL AS subscribed,
       COALESCE(s.autodownload, 0), COALESCE(s.format_override, ''),
       (SELECT count(*) FROM channel_videos cv WHERE cv.channel_id = c.id AND cv.state = 'pending'),
       (SELECT count(*) FROM videos v WHERE v.channel_id = c.id AND v.status = 'downloaded')
FROM channels c LEFT JOIN subscriptions s ON s.channel_id = c.id
WHERE c.tracked_at IS NOT NULL`
```

and each filter case appends `AND …` rather than `WHERE …`:

```go
	switch filter {
	case "subscribed":
		query += ` AND s.channel_id IS NOT NULL`
	case "tracked":
		query += ` AND s.channel_id IS NULL`
	case "autodownload":
		query += ` AND s.autodownload = 1`
	case "all", "":
		// no extra clause
	default:
		return nil, fmt.Errorf("list channels: unknown filter %q", filter)
	}
```

Update the `rows.Scan` in `List` to match the new column order:

```go
		if err := rows.Scan(
			&it.ID, &it.Handle, &it.Name, &it.Description, &it.AvatarPath, &it.BannerPath,
			&it.ResolvedAt, &it.TrackedAt, &it.AddedAt,
			&it.Subscribed, &it.Autodownload, &it.FormatOverride,
			&it.PendingCount, &it.DownloadedCount,
		); err != nil {
```

- [ ] **Step 5: Run the tests**

Run: `cd backend && go test ./internal/channels/ -v`

Expected: PASS. If other tests in the package fail on the changed `Upsert` semantics, fix those tests — the new semantics are correct.

- [ ] **Step 6: Fix the tracking call site**

`handleChannelsPost` in `backend/internal/httpapi/channels_handlers.go` currently relies on `Upsert` alone to track. It must now call `Track` too. After the existing `s.channels.Upsert(...)` block, insert:

```go
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	if err := s.channels.Track(ucid, now); err != nil {
		serverError(w, r, err, "track channel failed")
		return
	}
```

and change the `if req.Subscribe {` block below it to reuse that `now` instead of re-computing it:

```go
	if req.Subscribe {
		if err := s.channels.Subscribe(ucid, now); err != nil {
			serverError(w, r, err, "subscribe failed")
			return
		}
	}
```

- [ ] **Step 7: Run the whole backend suite**

Run: `cd backend && go test ./...`

Expected: PASS. `TestChannelsPost_tracksAndSubscribes` and `TestChannelsPost_trackOnly_notSubscribed` in `internal/httpapi/channels_handlers_test.go` both exercise this path and must stay green.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/store/migrations/0001_init.sql backend/internal/channels/store.go backend/internal/channels/store_test.go backend/internal/httpapi/channels_handlers.go
git -c user.email=trick77@users.noreply.github.com commit -m "feat(channels): make channels a metadata cache, track via tracked_at"
```

---

### Task 2: Guard the destructive paths against cache-only rows

**Files:**
- Modify: `backend/internal/httpapi/channels_handlers.go` (`handleChannelsDelete`, `handleChannelsSubscribe`)
- Test: `backend/internal/httpapi/channels_handlers_test.go`

**Interfaces:**
- Consumes: `channels.Store.Get` returning `TrackedAt` (Task 1).
- Produces: nothing new; tightens existing endpoints.

`DeleteCascade` destroys every video belonging to a channel. Before Task 1 it was unreachable for an untracked channel because no row existed. Now a row exists for any channel the user merely visited, so the guard must be explicit.

- [ ] **Step 1: Write the failing test**

Add to `backend/internal/httpapi/channels_handlers_test.go`:

```go
// TestChannelsDelete_cacheOnlyRow_404 asserts a channel that exists only as a
// metadata cache entry cannot be deleted. DeleteCascade destroys every video
// belonging to a channel, including favorited ones — reaching it for a
// channel the user never tracked would be data loss from a page they merely
// visited.
func TestChannelsDelete_cacheOnlyRow_404(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{ucid: "UCx", name: "X"})
	h := New(deps)
	if err := deps.Channels.Upsert(channels.Channel{ID: "UCcache", Name: "Cache Only"}); err != nil {
		t.Fatalf("seed cache row: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/channels/UCcache", nil)
	req.AddCookie(loginAndGetCookie(t, h))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
	c, err := deps.Channels.Get("UCcache")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if c == nil {
		t.Fatal("delete removed a cache-only channel row")
	}
}

// TestChannelsSubscribe_cacheOnlyRow_404 asserts subscribing requires an
// explicitly tracked channel — a cache row is not enough, or visiting a
// channel page would make it subscribable without ever tracking it.
func TestChannelsSubscribe_cacheOnlyRow_404(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{ucid: "UCx", name: "X"})
	h := New(deps)
	if err := deps.Channels.Upsert(channels.Channel{ID: "UCcache", Name: "Cache Only"}); err != nil {
		t.Fatalf("seed cache row: %v", err)
	}

	rec := postJSON(t, h, "/api/channels/UCcache/subscribe", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/httpapi/ -run 'TestChannelsDelete_cacheOnlyRow_404|TestChannelsSubscribe_cacheOnlyRow_404' -v`

Expected: FAIL — delete returns 200 and removes the row; subscribe returns 200.

- [ ] **Step 3: Add the guards**

In `handleChannelsDelete`, immediately after `id := r.PathValue("id")`:

```go
	// A cache-only row (visited, never tracked) must not be deletable:
	// DeleteCascade destroys every video belonging to the channel.
	c, err := s.channels.Get(id)
	if err != nil {
		serverError(w, r, err, "delete failed")
		return
	}
	if c == nil || c.TrackedAt == "" {
		writeJSONError(w, http.StatusNotFound, "channel not tracked")
		return
	}
```

Then rename the existing `refs, err := s.channels.VideoRefs(id)` to reuse the declared `err`:

```go
	refs, rerr := s.channels.VideoRefs(id)
	if rerr != nil {
		serverError(w, r, rerr, "delete failed")
		return
	}
```

In `handleChannelsSubscribe`, tighten the existing nil check:

```go
	if c == nil || c.TrackedAt == "" {
		writeJSONError(w, http.StatusNotFound, "channel not tracked")
		return
	}
```

- [ ] **Step 4: Run the tests**

Run: `cd backend && go test ./internal/httpapi/ -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/httpapi/channels_handlers.go backend/internal/httpapi/channels_handlers_test.go
git -c user.email=trick77@users.noreply.github.com commit -m "fix(channels): block delete and subscribe on cache-only rows"
```

---

### Task 3: Video store gains title search, sort, and channel scoping

**Files:**
- Modify: `backend/internal/videos/store.go` (`List`)
- Test: `backend/internal/videos/store_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `videos.Store.List(opts videos.ListOptions) ([]Video, error)` replacing `List(filter, category string)`. `ListOptions` is `{Filter, Category, Query, Sort, ChannelID string}`. Sort values: `""`/`"newest"`, `"oldest"`, `"longest"`, `"title"`.

This is a **breaking signature change**. Every caller must be updated in this task or the package will not compile.

- [ ] **Step 1: Write the failing test**

Add to `backend/internal/videos/store_test.go`:

```go
// TestList_query_matchesTitleCaseInsensitively asserts the search box matches
// on title regardless of case, and that a non-matching row is excluded.
func TestList_query_matchesTitleCaseInsensitively(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v1", Title: "Descending the Hranice Abyss", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "v2", Title: "Night trek across the Salar", Status: "downloaded"})

	got, err := s.List(ListOptions{Query: "HRANICE"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != "v1" {
		t.Fatalf("got %d rows %+v, want only v1", len(got), got)
	}
}

// TestList_query_escapesLikeWildcards asserts a literal % in the search box
// does not turn into a match-everything wildcard.
func TestList_query_escapesLikeWildcards(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v1", Title: "100% wool", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "v2", Title: "nothing special", Status: "downloaded"})

	got, err := s.List(ListOptions{Query: "%"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != "v1" {
		t.Fatalf("got %d rows %+v, want only the row literally containing %%", len(got), got)
	}
}

// TestList_sort_ordersRows asserts each sort key produces the documented
// order. Sorting was previously hardcoded to created_at DESC.
func TestList_sort_ordersRows(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "a", Title: "Bravo", DurationSeconds: 100, CreatedAt: "2026-01-01 00:00:00", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "b", Title: "Alpha", DurationSeconds: 300, CreatedAt: "2026-02-01 00:00:00", Status: "downloaded"})

	cases := []struct {
		sort string
		want []string
	}{
		{"newest", []string{"b", "a"}},
		{"oldest", []string{"a", "b"}},
		{"longest", []string{"b", "a"}},
		{"title", []string{"b", "a"}},
	}
	for _, tc := range cases {
		got, err := s.List(ListOptions{Sort: tc.sort})
		if err != nil {
			t.Fatalf("list sort=%s: %v", tc.sort, err)
		}
		var ids []string
		for _, v := range got {
			ids = append(ids, v.ID)
		}
		if len(ids) != len(tc.want) || ids[0] != tc.want[0] || ids[1] != tc.want[1] {
			t.Fatalf("sort=%s ids = %v, want %v", tc.sort, ids, tc.want)
		}
	}
}

// TestList_unknownSort_fallsBackToNewest asserts an unrecognized sort value
// from a hand-edited URL yields the default order rather than a SQL error or
// an injected ORDER BY clause.
func TestList_unknownSort_fallsBackToNewest(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "a", CreatedAt: "2026-01-01 00:00:00", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "b", CreatedAt: "2026-02-01 00:00:00", Status: "downloaded"})

	got, err := s.List(ListOptions{Sort: "id; DROP TABLE videos"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].ID != "b" {
		t.Fatalf("got %+v, want newest-first fallback", got)
	}
}

// TestList_channelID_scopesToOneChannel asserts channel scoping matches on
// channel_id and, for older rows written before channel ids were recorded,
// falls back to an exact channel_name match.
func TestList_channelID_scopesToOneChannel(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v1", ChannelID: "UCa", ChannelName: "Alpha", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "v2", ChannelID: "", ChannelName: "Alpha", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "v3", ChannelID: "UCb", ChannelName: "Beta", Status: "downloaded"})

	got, err := s.List(ListOptions{ChannelID: "UCa", ChannelName: "Alpha"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows %+v, want v1 and v2", len(got), got)
	}
	for _, v := range got {
		if v.ID == "v3" {
			t.Fatal("channel scoping leaked another channel's video")
		}
	}
}
```

If `newTestStore`/`seedVideo` do not exist in that file, add helpers following the package's existing pattern (real SQLite via `store.Open` + `store.Migrate` in `t.TempDir()`, then `Upsert` each seed video).

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/videos/ -run TestList_ -v`

Expected: compile failure — `ListOptions` undefined, `List` takes two strings.

- [ ] **Step 3: Implement `ListOptions`**

In `backend/internal/videos/store.go`, replace `List` with:

```go
// ListOptions narrows videos.Store.List. Every field is optional; the zero
// value means "every video, newest first" — the pre-existing behavior.
type ListOptions struct {
	// Filter is the status dimension: unwatched|watched|favorites|downloading.
	// Anything else (including "" and "all") means no status constraint.
	Filter string
	// Category is the classification dimension, ANDed with Filter.
	Category string
	// Query matches case-insensitively against the title as a substring.
	Query string
	// Sort is newest|oldest|longest|title. Anything else means newest.
	Sort string
	// ChannelID scopes to one channel. ChannelName is the fallback for rows
	// written before channel ids were recorded, and is only consulted when
	// ChannelID is also set.
	ChannelID   string
	ChannelName string
}

// sortClauses maps the accepted Sort values to ORDER BY fragments. Sort is
// interpolated into SQL, so it must only ever come from this map — never
// from the caller's string.
var sortClauses = map[string]string{
	"newest":  "created_at DESC, id DESC",
	"oldest":  "created_at ASC, id ASC",
	"longest": "COALESCE(duration_seconds, 0) DESC, id DESC",
	"title":   "title COLLATE NOCASE ASC, id ASC",
}

// escapeLike escapes the three characters LIKE treats specially so a user
// typing "100%" searches for a literal percent sign rather than matching
// every row. Pairs with the ESCAPE '\' clause in the query below.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// List returns videos matching opts, ordered by opts.Sort. The status,
// category, search, and channel dimensions are orthogonal: all that are set
// apply together.
func (s *Store) List(opts ListOptions) ([]Video, error) {
	conds := []string{}
	args := []any{}
	switch opts.Filter {
	case "unwatched":
		conds = append(conds, "status = 'downloaded' AND watched = 0")
	case "watched":
		conds = append(conds, "watched = 1")
	case "favorites":
		conds = append(conds, "favorite = 1")
	case "downloading":
		conds = append(conds, "status IN ('queued', 'downloading')")
	}
	if opts.Category != "" && opts.Category != "all" && ValidCategory(opts.Category) {
		conds = append(conds, "category = ?")
		args = append(args, opts.Category)
	}
	if q := strings.TrimSpace(opts.Query); q != "" {
		conds = append(conds, `title LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(q)+"%")
	}
	if opts.ChannelID != "" {
		if opts.ChannelName != "" {
			conds = append(conds, "(channel_id = ? OR (channel_id = '' AND channel_name = ?))")
			args = append(args, opts.ChannelID, opts.ChannelName)
		} else {
			conds = append(conds, "channel_id = ?")
			args = append(args, opts.ChannelID)
		}
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	order, ok := sortClauses[opts.Sort]
	if !ok {
		order = sortClauses["newest"]
	}

	rows, err := s.db.QueryContext(context.Background(),
		"SELECT "+videoColumns+" FROM videos "+where+" ORDER BY "+order,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list videos (%+v): %w", opts, err)
	}
	defer rows.Close()

	out := []Video{}
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, fmt.Errorf("list videos (%+v): %w", opts, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list videos (%+v): %w", opts, err)
	}
	return out, nil
}
```

- [ ] **Step 4: Update every caller**

Run: `cd backend && grep -rn '\.List(' --include='*.go' internal/ | grep -i video`

For each hit, convert `List(filter, category)` to `List(videos.ListOptions{Filter: filter, Category: category})`. The known caller is `handleListVideos` in `backend/internal/httpapi/videos_handlers.go`:

```go
	all, err := s.videos.List(videos.ListOptions{
		Filter:   r.URL.Query().Get("filter"),
		Category: r.URL.Query().Get("category"),
		Query:    r.URL.Query().Get("q"),
		Sort:     r.URL.Query().Get("sort"),
	})
```

Update the doc comment above it to mention `?q=` and `?sort=`.

- [ ] **Step 5: Run the tests**

Run: `cd backend && go test ./...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/videos/store.go backend/internal/videos/store_test.go backend/internal/httpapi/videos_handlers.go
git -c user.email=trick77@users.noreply.github.com commit -m "feat(videos): add title search, sort, and channel scoping to List"
```

---

### Task 4: Library gains the search box and sort control

**Files:**
- Modify: `ui/src/api/videos.ts` (`listVideos`)
- Modify: `ui/src/views/Library.tsx`
- Modify: `ui/src/index.css` (toolbar rules)
- Test: `ui/src/views/Library.test.tsx`

**Interfaces:**
- Consumes: `?q=` and `?sort=` on `GET /api/videos` (Task 3).
- Produces: `listVideos(opts: { filter?: VideoFilter; category?: string; q?: string; sort?: VideoSort })`. `VideoSort = "newest" | "oldest" | "longest" | "title"`, exported from `ui/src/api/types.ts`. `SORT_OPTIONS` exported from `ui/src/views/Library.tsx` for reuse by the channel page.

- [ ] **Step 1: Write the failing test**

Add to `ui/src/views/Library.test.tsx` (create the file following the `Channels.test.tsx` conventions if it does not exist — `vi.mock` factory listing every export of each mocked module, then post-mock imports, then `mockReset`/`mockResolvedValue` in `beforeEach`):

```tsx
it("typing in the search box refetches with the query", async () => {
  const user = userEvent.setup();
  render(<Library onOpenVideo={() => {}} />);
  await screen.findByPlaceholderText(/search/i);

  await user.type(screen.getByPlaceholderText(/search/i), "abyss");

  await waitFor(() => {
    expect(listVideos).toHaveBeenCalledWith(expect.objectContaining({ q: "abyss" }));
  });
});

it("choosing a sort option refetches with that sort", async () => {
  const user = userEvent.setup();
  render(<Library onOpenVideo={() => {}} />);
  await screen.findByLabelText(/sort/i);

  await user.selectOptions(screen.getByLabelText(/sort/i), "longest");

  await waitFor(() => {
    expect(listVideos).toHaveBeenCalledWith(expect.objectContaining({ sort: "longest" }));
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd ui && npx vitest run src/views/Library.test.tsx`

Expected: FAIL — no element with placeholder "Search", no element labelled "Sort".

- [ ] **Step 3: Update the API client**

Replace `listVideos` in `ui/src/api/videos.ts`:

```ts
// ListVideosOptions mirrors the query params handleListVideos understands.
// Every field is optional; omitting all of them is "everything, newest first".
export type ListVideosOptions = {
  filter?: VideoFilter;
  category?: string;
  q?: string;
  sort?: VideoSort;
  /** Scopes the list to one channel (the channel page's Archive tab). */
  channel?: string;
};

export async function listVideos(opts: ListVideosOptions = {}): Promise<Video[]> {
  const p = new URLSearchParams();
  if (opts.filter) p.set("filter", opts.filter);
  if (opts.category && opts.category !== "all") p.set("category", opts.category);
  if (opts.q) p.set("q", opts.q);
  if (opts.sort) p.set("sort", opts.sort);
  if (opts.channel) p.set("channel", opts.channel);
  const qs = p.toString();
  return api.get<Video[]>(`/api/videos${qs ? `?${qs}` : ""}`, "failed to load videos");
}
```

Add to `ui/src/api/types.ts`, next to `VideoFilter`:

```ts
// VideoSort mirrors the sort keys videos.Store.List accepts.
export type VideoSort = "newest" | "oldest" | "longest" | "title";
```

- [ ] **Step 4: Add the controls to Library**

In `ui/src/views/Library.tsx`, export the shared sort options so the channel page reuses them verbatim:

```tsx
// SORT_OPTIONS is shared with the channel page's Archive tab so the two
// lists can never drift apart in wording or in accepted values.
export const SORT_OPTIONS: { id: VideoSort; label: string }[] = [
  { id: "newest", label: "Newest first" },
  { id: "oldest", label: "Oldest first" },
  { id: "longest", label: "Longest first" },
  { id: "title", label: "Title A–Z" },
];
```

Add state and a debounce, then wire the fetch. The debounce matters: without it every keystroke fires a request.

```tsx
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<VideoSort>("newest");
  const [debouncedQuery, setDebouncedQuery] = useState("");

  // Debounce the search box so typing "abyss" fires one request, not five.
  useEffect(() => {
    const id = setTimeout(() => setDebouncedQuery(query), 250);
    return () => clearTimeout(id);
  }, [query]);
```

Include `debouncedQuery` and `sort` in the existing load effect's dependency array, and pass them through:

```tsx
    listVideos({ filter, category, q: debouncedQuery, sort })
```

Render the controls above the grid, alongside the existing chips:

```tsx
      <div className="listbar">
        <input
          className={controlClass}
          style={{ maxWidth: 280 }}
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search titles"
          aria-label="Search titles"
        />
        <select
          className={controlClass}
          style={{ maxWidth: 180 }}
          value={sort}
          onChange={(e) => setSort(e.target.value as VideoSort)}
          aria-label="Sort"
        >
          {SORT_OPTIONS.map((o) => (
            <option key={o.id} value={o.id}>
              {o.label}
            </option>
          ))}
        </select>
      </div>
```

Add to `ui/src/index.css`:

```css
/* listbar — the search + sort row shared by the Library and the channel
   page's Archive tab. Sits below the filter chips, above the grid. */
.listbar {
  display: flex;
  gap: 10px;
  align-items: center;
  flex-wrap: wrap;
  margin-bottom: 18px;
}
```

- [ ] **Step 5: Run the tests**

Run: `cd ui && npx vitest run`

Expected: PASS. Other tests calling `listVideos("all")` positionally will fail to compile — update them to the options object.

- [ ] **Step 6: Typecheck**

Run: `cd ui && npx tsc -p tsconfig.app.json --noEmit`

Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add ui/src/api/videos.ts ui/src/api/types.ts ui/src/views/Library.tsx ui/src/views/Library.test.tsx ui/src/index.css
git -c user.email=trick77@users.noreply.github.com commit -m "feat(library): add title search and sort controls"
```

---

### Task 5: `ResolveChannel` returns full metadata

**Files:**
- Modify: `backend/internal/ytdlp/channel.go`
- Modify: `backend/internal/httpapi/channels_handlers.go` (`ChannelResolver` interface, `handleChannelsPost`)
- Modify: `backend/internal/httpapi/channels_handlers_test.go` (`testResolver`)
- Test: `backend/internal/ytdlp/channel_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `ytdlp.ChannelInfo{UCID, Name, Handle, Description, AvatarURL, BannerURL string}` and `Runner.ResolveChannel(ctx, url) (ChannelInfo, error)` — **a breaking signature change** from `(ucid, name string, err error)`. The `httpapi.ChannelResolver` interface changes to match.

- [ ] **Step 1: Write the failing test**

Add to `backend/internal/ytdlp/channel_test.go`:

```go
// TestParseChannelInfo_picksAvatarAndBanner asserts the avatar and banner are
// selected by yt-dlp's thumbnail id, not by array position — the array also
// contains cropped variants, and its order is not guaranteed.
func TestParseChannelInfo_picksAvatarAndBanner(t *testing.T) {
	raw := []byte(`{
      "channel_id": "UCxyz",
      "channel": "Uncanny Expeditions",
      "description": "Long-form field documentaries.",
      "thumbnails": [
        {"id": "avatar_uncropped", "url": "https://x/avatar.jpg"},
        {"id": "banner_uncropped", "url": "https://x/banner.jpg"},
        {"id": "0", "url": "https://x/other.jpg"}
      ]
    }`)

	info, err := parseChannelInfo(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.UCID != "UCxyz" {
		t.Fatalf("UCID = %q", info.UCID)
	}
	if info.Name != "Uncanny Expeditions" {
		t.Fatalf("Name = %q", info.Name)
	}
	if info.Description != "Long-form field documentaries." {
		t.Fatalf("Description = %q", info.Description)
	}
	if info.AvatarURL != "https://x/avatar.jpg" {
		t.Fatalf("AvatarURL = %q", info.AvatarURL)
	}
	if info.BannerURL != "https://x/banner.jpg" {
		t.Fatalf("BannerURL = %q", info.BannerURL)
	}
}

// TestParseChannelInfo_missingImages_isNotAnError asserts a channel with no
// banner still resolves. Many channels have no banner at all, and that must
// not fail the whole resolve.
func TestParseChannelInfo_missingImages_isNotAnError(t *testing.T) {
	info, err := parseChannelInfo([]byte(`{"channel_id":"UCx","channel":"X"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if info.AvatarURL != "" || info.BannerURL != "" {
		t.Fatalf("expected empty image urls, got %+v", info)
	}
}

// TestParseChannelInfo_noUCID_isAnError asserts a response we cannot pin to a
// channel id is rejected rather than cached under an empty key.
func TestParseChannelInfo_noUCID_isAnError(t *testing.T) {
	if _, err := parseChannelInfo([]byte(`{"channel":"X"}`)); err == nil {
		t.Fatal("expected an error when no channel id is present")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/ytdlp/ -run TestParseChannelInfo -v`

Expected: compile failure — `parseChannelInfo` undefined.

- [ ] **Step 3: Implement**

In `backend/internal/ytdlp/channel.go`, extend `flatListing` and add the parser:

```go
// ChannelInfo is a channel's identity as resolved from a metadata-only
// yt-dlp call. AvatarURL and BannerURL are REMOTE urls; the caller decides
// whether to download them.
type ChannelInfo struct {
	UCID        string
	Name        string
	Description string
	AvatarURL   string
	BannerURL   string
}

// channelThumb is one entry of the channel-level thumbnails array. Unlike a
// video's thumbnails, these carry an id naming the role ("avatar_uncropped",
// "banner_uncropped"), which is how the two are told apart — array order is
// not guaranteed and the array also holds cropped variants.
type channelThumb struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}
```

Add to `flatListing`:

```go
	Description string         `json:"description"`
	Thumbnails  []channelThumb `json:"thumbnails"`
```

Then:

```go
// parseChannelInfo extracts a ChannelInfo from a yt-dlp metadata-only
// channel response. Split out from ResolveChannel so it is testable without
// shelling out.
func parseChannelInfo(out []byte) (ChannelInfo, error) {
	var raw flatListing
	if err := json.Unmarshal(out, &raw); err != nil {
		return ChannelInfo{}, fmt.Errorf("ytdlp: parse channel json: %w", err)
	}
	info := ChannelInfo{
		UCID:        raw.Ch,
		Name:        raw.Channel,
		Description: raw.Description,
	}
	if info.UCID == "" {
		info.UCID = raw.ID
	}
	if info.Name == "" {
		info.Name = raw.Uploader
	}
	if info.Name == "" {
		info.Name = raw.Title
	}
	for _, th := range raw.Thumbnails {
		switch th.ID {
		case "avatar_uncropped":
			info.AvatarURL = th.URL
		case "banner_uncropped":
			info.BannerURL = th.URL
		}
	}
	if info.UCID == "" {
		return ChannelInfo{}, fmt.Errorf("ytdlp: could not resolve channel id")
	}
	return info, nil
}
```

Replace `ResolveChannel`'s body after the `exec` call:

```go
func (r *Runner) ResolveChannel(ctx context.Context, channelURL string) (ChannelInfo, error) {
	if perr := r.pauseGate(); perr != nil {
		return ChannelInfo{}, perr
	}
	cookieText, gerr := r.cookieGate()
	if gerr != nil {
		return ChannelInfo{}, gerr
	}
	out, xerr := r.exec(ctx, cookieText, "-J", "--flat-playlist", "--skip-download", "--playlist-items", "0", channelURL)
	if xerr != nil {
		return ChannelInfo{}, xerr
	}
	info, err := parseChannelInfo(out)
	if err != nil {
		return ChannelInfo{}, fmt.Errorf("%w (url %q)", err, channelURL)
	}
	return info, nil
}
```

- [ ] **Step 4: Update the interface and its callers**

In `backend/internal/httpapi/channels_handlers.go`:

```go
type ChannelResolver interface {
	ResolveChannel(ctx context.Context, url string) (ytdlp.ChannelInfo, error)
}
```

In `handleChannelsPost`, replace the resolve block:

```go
	info, err := s.channelResolver.ResolveChannel(r.Context(), channelURL)
	if err != nil {
		if errors.Is(err, ytdlp.ErrNoCookie) {
			writeJSONError(w, http.StatusConflict, "cookie required")
			return
		}
		writeJSONError(w, http.StatusBadGateway, "resolve channel failed: "+err.Error())
		return
	}
	ucid, name := info.UCID, info.Name
```

and extend the `Upsert` call to persist the new metadata (image download comes in Task 6; store the empty paths for now):

```go
	handle := channelHandleFromURL(req.URL)
	if err := s.channels.Upsert(channels.Channel{
		ID:          ucid,
		Name:        name,
		Handle:      handle,
		Description: info.Description,
		ResolvedAt:  time.Now().UTC().Format("2006-01-02 15:04:05"),
	}); err != nil {
		serverError(w, r, err, "track channel failed")
		return
	}
```

In `backend/internal/httpapi/channels_handlers_test.go`, update the fake:

```go
type testResolver struct {
	info  ytdlp.ChannelInfo
	err   error
	calls int
}

func (r *testResolver) ResolveChannel(ctx context.Context, url string) (ytdlp.ChannelInfo, error) {
	r.calls++
	if r.err != nil {
		return ytdlp.ChannelInfo{}, r.err
	}
	return r.info, nil
}
```

Update every `&testResolver{ucid: "X", name: "Y"}` literal in the package to `&testResolver{info: ytdlp.ChannelInfo{UCID: "X", Name: "Y"}}`.

- [ ] **Step 5: Run the tests**

Run: `cd backend && go test ./...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/ytdlp/channel.go backend/internal/ytdlp/channel_test.go backend/internal/httpapi/channels_handlers.go backend/internal/httpapi/channels_handlers_test.go
git -c user.email=trick77@users.noreply.github.com commit -m "feat(ytdlp): resolve channel description, avatar, and banner"
```

---

### Task 6: Download and serve channel images

**Files:**
- Create: `backend/internal/media/fetch.go`
- Create: `backend/internal/media/fetch_test.go`
- Modify: `backend/internal/httpapi/channels_handlers.go` (image routes + wiring into `handleChannelsPost`)
- Modify: `backend/internal/httpapi/server.go` (route registration)

**Interfaces:**
- Consumes: `ytdlp.ChannelInfo` (Task 5), `media.SafeMediaPath`.
- Produces: `media.FetchImage(ctx context.Context, url, mediaDir, relPath string) (string, error)` returning the stored relative path. Routes `GET /api/channels/{id}/avatar` and `GET /api/channels/{id}/banner`. Channel images live at `<mediaDir>/.channels/<ucid>/avatar.<ext>` and `banner.<ext>`, stored **relative** to `mediaDir`.

- [ ] **Step 1: Write the failing test**

Create `backend/internal/media/fetch_test.go`:

```go
package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFetchImage_savesAndReturnsRelativePath asserts the image lands under
// mediaDir and the returned path is relative, matching how subtitle_path is
// stored (SafeMediaPath resolves both, but relative is what new code writes).
func TestFetchImage_savesAndReturnsRelativePath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff fake jpeg bytes"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	rel, err := FetchImage(context.Background(), srv.URL, dir, ".channels/UCx/avatar")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if filepath.IsAbs(rel) {
		t.Fatalf("returned path %q is absolute, want relative", rel)
	}
	if !strings.HasSuffix(rel, ".jpg") {
		t.Fatalf("returned path %q has no jpeg extension", rel)
	}
	if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

// TestFetchImage_rejectsNonImage asserts an HTML error page served with a 200
// is not written to disk as if it were an avatar.
func TestFetchImage_rejectsNonImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>nope</html>"))
	}))
	defer srv.Close()

	if _, err := FetchImage(context.Background(), srv.URL, t.TempDir(), ".channels/UCx/avatar"); err == nil {
		t.Fatal("expected an error for a non-image content type")
	}
}

// TestFetchImage_rejectsOversizeBody asserts a hostile or broken server
// cannot fill the disk through this path.
func TestFetchImage_rejectsOversizeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		big := make([]byte, maxImageBytes+1024)
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	if _, err := FetchImage(context.Background(), srv.URL, t.TempDir(), ".channels/UCx/avatar"); err == nil {
		t.Fatal("expected an error for an oversize body")
	}
}

// TestFetchImage_emptyURL asserts a channel with no banner is a no-op rather
// than an error the caller has to special-case.
func TestFetchImage_emptyURL(t *testing.T) {
	rel, err := FetchImage(context.Background(), "", t.TempDir(), ".channels/UCx/banner")
	if err != nil {
		t.Fatalf("empty url should not error: %v", err)
	}
	if rel != "" {
		t.Fatalf("rel = %q, want empty", rel)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/media/ -run TestFetchImage -v`

Expected: compile failure — `FetchImage` and `maxImageBytes` undefined.

- [ ] **Step 3: Implement**

Create `backend/internal/media/fetch.go`:

```go
package media

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxImageBytes caps a fetched channel image. Avatars and banners are well
// under this; the cap exists so a hostile or broken server cannot fill the
// disk through this path.
const maxImageBytes = 8 << 20 // 8 MiB

// imageExts maps the content types YouTube serves channel art as to the
// extension we store it under. A response whose type is not in this map is
// rejected — an HTML error page served with a 200 must never be written to
// disk as if it were an avatar.
var imageExts = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

// FetchImage downloads url into mediaDir under relBase (a path relative to
// mediaDir, WITHOUT an extension — the extension comes from the response's
// content type) and returns the stored path relative to mediaDir.
//
// An empty url is a no-op returning ("", nil): a channel with no banner is
// normal, not an error.
func FetchImage(ctx context.Context, url, mediaDir, relBase string) (string, error) {
	if url == "" {
		return "", nil
	}
	if mediaDir == "" {
		return "", fmt.Errorf("fetch image: media dir not configured")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch image: status %d", resp.StatusCode)
	}

	ctype := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	ext, ok := imageExts[strings.ToLower(ctype)]
	if !ok {
		return "", fmt.Errorf("fetch image: unsupported content type %q", ctype)
	}

	// Read one byte past the cap so an exactly-at-limit body still succeeds
	// while an oversize one is detected rather than silently truncated.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	if len(body) > maxImageBytes {
		return "", fmt.Errorf("fetch image: body exceeds %d bytes", maxImageBytes)
	}

	rel := relBase + ext
	dest := filepath.Join(mediaDir, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	// Write to a temp file and rename, so a reader never observes a partial
	// image and a failed write cannot leave a corrupt one behind.
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("fetch image: %w", err)
	}
	return rel, nil
}
```

- [ ] **Step 4: Run the media tests**

Run: `cd backend && go test ./internal/media/ -v`

Expected: PASS.

- [ ] **Step 5: Add the serving handlers**

Add to `backend/internal/httpapi/channels_handlers.go`:

```go
// handleChannelAvatar and handleChannelBanner serve a cached channel image
// off local disk. Like video thumbnails, the stored path never reaches the
// browser — only these endpoints do — and it is resolved through
// media.SafeMediaPath so a crafted stored value cannot escape the media dir.
func (s *server) handleChannelAvatar(w http.ResponseWriter, r *http.Request) {
	s.serveChannelImage(w, r, func(c *channels.Channel) string { return c.AvatarPath })
}

func (s *server) handleChannelBanner(w http.ResponseWriter, r *http.Request) {
	s.serveChannelImage(w, r, func(c *channels.Channel) string { return c.BannerPath })
}

func (s *server) serveChannelImage(w http.ResponseWriter, r *http.Request, pick func(*channels.Channel) string) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	c, err := s.channels.Get(r.PathValue("id"))
	if err != nil {
		serverError(w, r, err, "load channel failed")
		return
	}
	if c == nil {
		http.NotFound(w, r)
		return
	}
	stored := pick(c)
	if stored == "" {
		http.NotFound(w, r)
		return
	}
	path, err := media.SafeMediaPath(s.mediaDir, stored)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, filepath.Base(path), fi.ModTime(), f)
}
```

Add `"os"` and `"path/filepath"` to the file's imports.

Register in `backend/internal/httpapi/server.go`, next to the other channel routes:

```go
	mux.Handle("GET /api/channels/{id}/avatar", s.requireAuth(http.HandlerFunc(s.handleChannelAvatar)))
	mux.Handle("GET /api/channels/{id}/banner", s.requireAuth(http.HandlerFunc(s.handleChannelBanner)))
```

- [ ] **Step 6: Fetch the images when a channel is tracked**

In `handleChannelsPost`, after the resolve and before the `Upsert`, add:

```go
	// Images are best-effort: a channel with no banner, or a transient fetch
	// failure, must not prevent the channel from being tracked.
	avatarPath, err := media.FetchImage(r.Context(), info.AvatarURL, s.mediaDir, ".channels/"+ucid+"/avatar")
	if err != nil {
		slog.Warn("channel avatar fetch failed", "channel_id", ucid, "err", err)
	}
	bannerPath, err := media.FetchImage(r.Context(), info.BannerURL, s.mediaDir, ".channels/"+ucid+"/banner")
	if err != nil {
		slog.Warn("channel banner fetch failed", "channel_id", ucid, "err", err)
	}
```

and pass `AvatarPath: avatarPath, BannerPath: bannerPath` into the `channels.Channel` literal. Add `"log/slog"` to the imports.

- [ ] **Step 7: Run the whole backend suite**

Run: `cd backend && go test ./...`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/media/fetch.go backend/internal/media/fetch_test.go backend/internal/httpapi/channels_handlers.go backend/internal/httpapi/server.go
git -c user.email=trick77@users.noreply.github.com commit -m "feat(channels): download and serve channel avatar and banner"
```

---

### Task 7: Channel detail endpoint

**Files:**
- Modify: `backend/internal/channels/store.go` (`GetSubscription`, `Stats`)
- Modify: `backend/internal/httpapi/channels_handlers.go` (`handleChannelDetail`)
- Modify: `backend/internal/httpapi/server.go`
- Test: `backend/internal/httpapi/channels_handlers_test.go`

**Interfaces:**
- Consumes: `channels.Store.Get` (Task 1).
- Produces: `Store.GetSubscription(channelID string) (*Subscription, error)` (nil when not subscribed). `Store.Stats(channelID, channelName string) (Stats, error)` where `Stats` is `{ArchivedCount int; RuntimeSeconds int64; DiskBytes int64; NewestPublishedAt string}`. Route `GET /api/channels/{id}` returning `channelDetail` JSON.

- [ ] **Step 1: Write the failing test**

Add to `backend/internal/httpapi/channels_handlers_test.go`:

```go
// TestChannelDetail_untrackedChannel_200 asserts a channel that exists only
// because the user downloaded one of its videos by URL still has a page.
// This is the whole point of splitting the cache from the tracking list.
func TestChannelDetail_untrackedChannel_200(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	h := New(deps)
	seedVideoRow(t, deps, "v1", "UCloose", "Deep Field Radio")

	body := getJSON(t, h, "/api/channels/UCloose")
	if !strings.Contains(body, `"tracked":false`) {
		t.Fatalf("want tracked:false, got %s", body)
	}
	if !strings.Contains(body, "Deep Field Radio") {
		t.Fatalf("want the channel name from its videos, got %s", body)
	}
}

// TestChannelDetail_stats asserts the four header numbers count only
// downloaded videos — a queued or errored row is not on disk and must not be
// claimed as archived.
func TestChannelDetail_stats(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	h := New(deps)
	seedVideoRowFull(t, deps, seedVideo{ID: "v1", ChannelID: "UCa", ChannelName: "A", Status: "downloaded", Duration: 600, Bytes: 1000})
	seedVideoRowFull(t, deps, seedVideo{ID: "v2", ChannelID: "UCa", ChannelName: "A", Status: "downloaded", Duration: 300, Bytes: 2000})
	seedVideoRowFull(t, deps, seedVideo{ID: "v3", ChannelID: "UCa", ChannelName: "A", Status: "queued", Duration: 999, Bytes: 9999})

	body := getJSON(t, h, "/api/channels/UCa")
	if !strings.Contains(body, `"archived_count":2`) {
		t.Fatalf("want archived_count 2, got %s", body)
	}
	if !strings.Contains(body, `"runtime_seconds":900`) {
		t.Fatalf("want runtime_seconds 900, got %s", body)
	}
	if !strings.Contains(body, `"disk_bytes":3000`) {
		t.Fatalf("want disk_bytes 3000, got %s", body)
	}
}

// TestChannelDetail_unknownChannel_404 asserts an id matching neither a
// cached channel nor any video is a 404, not an empty page.
func TestChannelDetail_unknownChannel_404(t *testing.T) {
	h := New(channelsTestDeps(t, &testResolver{}))
	req := httptest.NewRequest(http.MethodGet, "/api/channels/UCnope", nil)
	req.AddCookie(loginAndGetCookie(t, h))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestChannelDetail_subscribed_includesSchedule asserts the page can tell the
// user when peeq last checked and when it will check next.
func TestChannelDetail_subscribed_includesSchedule(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCs", Name: "Subbed"}})
	h := New(deps)
	rec := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@s", "subscribe": true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: status %d body %s", rec.Code, rec.Body.String())
	}

	body := getJSON(t, h, "/api/channels/UCs")
	for _, want := range []string{`"tracked":true`, `"subscribed":true`, `"next_scan_at"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("want %s in %s", want, body)
		}
	}
}
```

Add the seed helpers to that file (the channels store exposes `DB()` for exactly this):

```go
type seedVideo struct {
	ID          string
	ChannelID   string
	ChannelName string
	Status      string
	Duration    int
	Bytes       int64
	PublishedAt string
}

func seedVideoRowFull(t *testing.T, deps Deps, v seedVideo) {
	t.Helper()
	_, err := deps.Channels.DB().Exec(`
INSERT INTO videos (id, url, title, channel_id, channel_name, duration_seconds, filesize_bytes, status, published_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ID, "https://y/"+v.ID, "title "+v.ID, v.ChannelID, v.ChannelName,
		v.Duration, v.Bytes, v.Status, v.PublishedAt)
	if err != nil {
		t.Fatalf("seed video %s: %v", v.ID, err)
	}
}

func seedVideoRow(t *testing.T, deps Deps, id, channelID, channelName string) {
	t.Helper()
	seedVideoRowFull(t, deps, seedVideo{ID: id, ChannelID: channelID, ChannelName: channelName, Status: "downloaded"})
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/httpapi/ -run TestChannelDetail -v`

Expected: FAIL — `GET /api/channels/UCa` returns 404 for every case (no route).

- [ ] **Step 3: Add the store methods**

Add to `backend/internal/channels/store.go`:

```go
// GetSubscription returns the subscription row for channelID, or (nil, nil)
// when the channel is not subscribed. ClaimDue is due-based and cannot answer
// "what is this one channel's schedule", which the channel page needs.
func (s *Store) GetSubscription(channelID string) (*Subscription, error) {
	row := s.db.QueryRowContext(context.Background(), `
SELECT channel_id, autodownload, format_override, baselined_at, last_scanned_at, next_scan_at, created_at
FROM subscriptions WHERE channel_id = ?`, channelID)

	var sub Subscription
	var baselinedAt, lastScannedAt sql.NullString
	err := row.Scan(&sub.ChannelID, &sub.Autodownload, &sub.FormatOverride,
		&baselinedAt, &lastScannedAt, &sub.NextScanAt, &sub.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get subscription %s: %w", channelID, err)
	}
	sub.BaselinedAt = baselinedAt.String
	sub.LastScannedAt = lastScannedAt.String
	return &sub, nil
}

// Stats are the channel page's four header numbers. They count only
// downloaded videos: a queued, errored, or tombstoned row is not on disk, so
// counting it would overstate what the user actually has.
type Stats struct {
	ArchivedCount     int
	RuntimeSeconds    int64
	DiskBytes         int64
	NewestPublishedAt string
}

// Stats computes the header numbers for one channel. channelName is the
// fallback for videos written before channel ids were recorded; pass "" to
// match on channel_id alone.
func (s *Store) Stats(channelID, channelName string) (Stats, error) {
	where := "channel_id = ?"
	args := []any{channelID}
	if channelName != "" {
		where = "(channel_id = ? OR (channel_id = '' AND channel_name = ?))"
		args = []any{channelID, channelName}
	}
	row := s.db.QueryRowContext(context.Background(), `
SELECT count(*),
       COALESCE(sum(duration_seconds), 0),
       COALESCE(sum(filesize_bytes), 0),
       COALESCE(max(published_at), '')
FROM videos WHERE status = 'downloaded' AND `+where, args...)

	var st Stats
	if err := row.Scan(&st.ArchivedCount, &st.RuntimeSeconds, &st.DiskBytes, &st.NewestPublishedAt); err != nil {
		return Stats{}, fmt.Errorf("channel stats %s: %w", channelID, err)
	}
	return st, nil
}

// NameFromVideos returns the channel name recorded on this channel's videos,
// used when there is no cached channels row yet — the untracked case, where
// the only thing peeq knows about the channel is what its videos say.
func (s *Store) NameFromVideos(channelID string) (string, error) {
	row := s.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(channel_name, '') FROM videos WHERE channel_id = ? AND channel_name != '' LIMIT 1`,
		channelID)
	var name string
	if err := row.Scan(&name); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", fmt.Errorf("channel name from videos %s: %w", channelID, err)
	}
	return name, nil
}
```

- [ ] **Step 4: Add the handler**

Add to `backend/internal/httpapi/channels_handlers.go`:

```go
// channelDetail is the JSON shape returned by GET /api/channels/{id}. It
// covers both a tracked channel and one the user has merely visited: Tracked
// and Subscribed are the flags the page branches on, and the subscription
// fields are zero when Subscribed is false.
type channelDetail struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Handle      string `json:"handle,omitempty"`
	Description string `json:"description,omitempty"`
	HasAvatar   bool   `json:"has_avatar"`
	HasBanner   bool   `json:"has_banner"`

	Tracked   bool   `json:"tracked"`
	TrackedAt string `json:"tracked_at,omitempty"`

	ArchivedCount     int    `json:"archived_count"`
	RuntimeSeconds    int64  `json:"runtime_seconds"`
	DiskBytes         int64  `json:"disk_bytes"`
	NewestPublishedAt string `json:"newest_published_at,omitempty"`

	Subscribed     bool   `json:"subscribed"`
	Autodownload   bool   `json:"autodownload"`
	FormatOverride string `json:"format_override,omitempty"`
	LastScannedAt  string `json:"last_scanned_at,omitempty"`
	NextScanAt     string `json:"next_scan_at,omitempty"`
	PendingCount   int    `json:"pending_count"`
}

func (s *server) handleChannelDetail(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")

	c, err := s.channels.Get(id)
	if err != nil {
		serverError(w, r, err, "load channel failed")
		return
	}

	// No cached row: fall back to what this channel's own videos say. If
	// there are none either, the id names nothing peeq knows about.
	name := ""
	if c != nil {
		name = c.Name
	}
	if name == "" {
		name, err = s.channels.NameFromVideos(id)
		if err != nil {
			serverError(w, r, err, "load channel failed")
			return
		}
	}
	stats, err := s.channels.Stats(id, name)
	if err != nil {
		serverError(w, r, err, "load channel failed")
		return
	}
	if c == nil && stats.ArchivedCount == 0 {
		writeJSONError(w, http.StatusNotFound, "channel not found")
		return
	}

	out := channelDetail{
		ID:                id,
		Name:              name,
		ArchivedCount:     stats.ArchivedCount,
		RuntimeSeconds:    stats.RuntimeSeconds,
		DiskBytes:         stats.DiskBytes,
		NewestPublishedAt: stats.NewestPublishedAt,
	}
	if c != nil {
		out.Handle = c.Handle
		out.Description = c.Description
		out.HasAvatar = c.AvatarPath != ""
		out.HasBanner = c.BannerPath != ""
		out.Tracked = c.TrackedAt != ""
		out.TrackedAt = c.TrackedAt
	}

	if out.Tracked {
		sub, serr := s.channels.GetSubscription(id)
		if serr != nil {
			serverError(w, r, serr, "load subscription failed")
			return
		}
		if sub != nil {
			out.Subscribed = true
			out.Autodownload = sub.Autodownload
			out.FormatOverride = sub.FormatOverride
			out.LastScannedAt = sub.LastScannedAt
			out.NextScanAt = sub.NextScanAt
		}
		if s.ledger != nil {
			pending, perr := s.ledger.ListPendingForChannel(id)
			if perr != nil {
				serverError(w, r, perr, "load pending failed")
				return
			}
			out.PendingCount = len(pending)
		}
	}

	writeJSON(w, out)
}
```

`ListPendingForChannel` is added in Task 9; to keep this task independently green, add it now to `backend/internal/channelvideos/store.go`:

```go
// ListPendingForChannel is ListPending scoped to one channel. The
// idx_channel_videos_channel index already supports this predicate.
func (s *Store) ListPendingForChannel(channelID string) ([]Entry, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT `+pendingColumns+`, COALESCE(c.name, '') AS channel_name
FROM channel_videos cv
LEFT JOIN channels c ON c.id = cv.channel_id
WHERE cv.state = 'pending' AND cv.channel_id = ?
ORDER BY cv.discovered_at DESC, cv.video_id DESC`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list pending for channel %s: %w", channelID, err)
	}
	defer rows.Close()
	return scanPendingEntries(rows)
}
```

If `ListPending` inlines its row loop rather than calling a helper, extract that loop into `scanPendingEntries(rows *sql.Rows) ([]Entry, error)` and have both call it, so the two lists can never diverge.

Register the route in `server.go`:

```go
	mux.Handle("GET /api/channels/{id}", s.requireAuth(http.HandlerFunc(s.handleChannelDetail)))
```

- [ ] **Step 5: Run the tests**

Run: `cd backend && go test ./...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/channels/store.go backend/internal/channelvideos/store.go backend/internal/httpapi/channels_handlers.go backend/internal/httpapi/channels_handlers_test.go backend/internal/httpapi/server.go
git -c user.email=trick77@users.noreply.github.com commit -m "feat(channels): add GET /api/channels/{id} detail endpoint"
```

---

### Task 8: Channel-scoped video list and background metadata resolve

**Files:**
- Modify: `backend/internal/httpapi/videos_handlers.go` (`handleListVideos` gains `?channel=`)
- Modify: `backend/internal/httpapi/channels_handlers.go` (background resolve in `handleChannelDetail`)
- Test: `backend/internal/httpapi/channels_handlers_test.go`

**Interfaces:**
- Consumes: `videos.ListOptions.ChannelID`/`ChannelName` (Task 3), `channels.Store.MarkResolveAttempted` (Task 1), `ChannelResolver` (Task 5).
- Produces: `GET /api/videos?channel={id}` scoping to one channel. `handleChannelDetail` triggers a one-shot background resolve for an unresolved channel.

- [ ] **Step 1: Write the failing test**

Add to `backend/internal/httpapi/channels_handlers_test.go`:

```go
// TestChannelDetail_unresolvedChannel_triggersOneResolve asserts visiting an
// uncached channel schedules exactly one metadata fetch, and that a second
// visit does not fetch again. Without the second assertion a permanently
// unresolvable channel would hit YouTube on every page load.
func TestChannelDetail_unresolvedChannel_triggersOneResolve(t *testing.T) {
	resolver := &testResolver{info: ytdlp.ChannelInfo{UCID: "UCloose", Name: "Deep Field Radio"}}
	deps := channelsTestDeps(t, resolver)
	done := make(chan struct{}, 4)
	deps.OnChannelResolved = func(string) { done <- struct{}{} }
	h := New(deps)
	seedVideoRow(t, deps, "v1", "UCloose", "Deep Field Radio")

	getJSON(t, h, "/api/channels/UCloose")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("background resolve never completed")
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver.calls = %d, want 1", resolver.calls)
	}

	getJSON(t, h, "/api/channels/UCloose")
	select {
	case <-done:
		t.Fatal("second visit resolved again; resolved_at should suppress it")
	case <-time.After(300 * time.Millisecond):
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver.calls = %d after second visit, want still 1", resolver.calls)
	}
}

// TestChannelDetail_resolveFailure_stillMarksAttempted asserts a channel that
// cannot be resolved (stale cookie, deleted channel) is not retried on every
// visit.
func TestChannelDetail_resolveFailure_stillMarksAttempted(t *testing.T) {
	resolver := &testResolver{err: errors.New("boom")}
	deps := channelsTestDeps(t, resolver)
	done := make(chan struct{}, 4)
	deps.OnChannelResolved = func(string) { done <- struct{}{} }
	h := New(deps)
	seedVideoRow(t, deps, "v1", "UCbad", "Bad Channel")

	getJSON(t, h, "/api/channels/UCbad")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("background resolve never completed")
	}

	getJSON(t, h, "/api/channels/UCbad")
	time.Sleep(300 * time.Millisecond)
	if resolver.calls != 1 {
		t.Fatalf("resolver.calls = %d, want 1 — a failed resolve must not retry every visit", resolver.calls)
	}
}

// TestListVideos_channelParam_scopes asserts ?channel= narrows the library to
// one channel, which is what the Archive tab loads.
func TestListVideos_channelParam_scopes(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	h := New(deps)
	seedVideoRow(t, deps, "v1", "UCa", "Alpha")
	seedVideoRow(t, deps, "v2", "UCb", "Beta")

	body := getJSON(t, h, "/api/videos?channel=UCa")
	if !strings.Contains(body, `"v1"`) || strings.Contains(body, `"v2"`) {
		t.Fatalf("channel scoping wrong: %s", body)
	}
}
```

Note: `channelsTestDeps` must wire `Videos: videos.New(db)` for the last test — update the helper to build and share one `*sql.DB` across `Channels`, `Videos`, and `Ledger`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/httpapi/ -run 'TestChannelDetail_unresolved|TestChannelDetail_resolveFailure|TestListVideos_channelParam' -v`

Expected: compile failure — `Deps.OnChannelResolved` undefined.

- [ ] **Step 3: Add the channel param to the video list**

In `handleListVideos`, resolve the channel's name for the loose-text fallback and pass both:

```go
	channelID := r.URL.Query().Get("channel")
	channelName := ""
	if channelID != "" && s.channels != nil {
		if c, cerr := s.channels.Get(channelID); cerr == nil && c != nil {
			channelName = c.Name
		} else if n, nerr := s.channels.NameFromVideos(channelID); nerr == nil {
			channelName = n
		}
	}
	all, err := s.videos.List(videos.ListOptions{
		Filter:      r.URL.Query().Get("filter"),
		Category:    r.URL.Query().Get("category"),
		Query:       r.URL.Query().Get("q"),
		Sort:        r.URL.Query().Get("sort"),
		ChannelID:   channelID,
		ChannelName: channelName,
	})
```

- [ ] **Step 4: Add the background resolve**

Add a test hook to `Deps` in `backend/internal/httpapi/server.go`:

```go
	// OnChannelResolved fires after a background channel-metadata resolve
	// settles, successfully or not. Test-only: it exists so a test can wait
	// for the goroutine instead of sleeping. nil in production.
	OnChannelResolved func(channelID string)
```

Add the matching `server` field and assign it in `New`.

Add to `backend/internal/httpapi/channels_handlers.go`, and call `s.maybeResolveChannel(id, c)` from `handleChannelDetail` just before `writeJSON(w, out)`:

```go
// maybeResolveChannel kicks off a one-shot background metadata fetch for a
// channel peeq has never resolved. It deliberately does NOT block the
// response: the page renders from what is already in the database and the
// header fills in on the next load.
//
// resolved_at is written whether the fetch succeeds or fails, so a channel
// that cannot be resolved — a stale cookie, a deleted channel — is not
// re-fetched on every single visit.
func (s *server) maybeResolveChannel(channelID string, cached *channels.Channel) {
	if s.channelResolver == nil || s.channels == nil {
		return
	}
	if cached != nil && cached.ResolvedAt != "" {
		return
	}
	go func() {
		defer func() {
			if s.onChannelResolved != nil {
				s.onChannelResolved(channelID)
			}
		}()
		// Detached from the request: the browser has its response already.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		url := "https://www.youtube.com/channel/" + channelID
		info, err := s.channelResolver.ResolveChannel(ctx, url)
		if err != nil {
			slog.Warn("channel resolve failed", "channel_id", channelID, "err", err)
			// Ensure a row exists to carry resolved_at, so the failure is
			// remembered and not retried on the next visit.
			if cached == nil {
				if uerr := s.channels.Upsert(channels.Channel{ID: channelID, ResolvedAt: now}); uerr != nil {
					slog.Error("cache channel after failed resolve", "channel_id", channelID, "err", uerr)
				}
				return
			}
			if merr := s.channels.MarkResolveAttempted(channelID, now); merr != nil {
				slog.Error("mark resolve attempted", "channel_id", channelID, "err", merr)
			}
			return
		}

		avatarPath, aerr := media.FetchImage(ctx, info.AvatarURL, s.mediaDir, ".channels/"+channelID+"/avatar")
		if aerr != nil {
			slog.Warn("channel avatar fetch failed", "channel_id", channelID, "err", aerr)
		}
		bannerPath, berr := media.FetchImage(ctx, info.BannerURL, s.mediaDir, ".channels/"+channelID+"/banner")
		if berr != nil {
			slog.Warn("channel banner fetch failed", "channel_id", channelID, "err", berr)
		}
		if uerr := s.channels.Upsert(channels.Channel{
			ID:          channelID,
			Name:        info.Name,
			Description: info.Description,
			AvatarPath:  avatarPath,
			BannerPath:  bannerPath,
			ResolvedAt:  now,
		}); uerr != nil {
			slog.Error("cache resolved channel", "channel_id", channelID, "err", uerr)
		}
	}()
}
```

- [ ] **Step 5: Run the tests**

Run: `cd backend && go test ./... -race`

Expected: PASS. The `-race` flag matters here — this is the first goroutine writing to the database off a request.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/httpapi/
git -c user.email=trick77@users.noreply.github.com commit -m "feat(channels): scope videos by channel, resolve metadata on first visit"
```

---

### Task 9: Scan-now and channel-scoped pending

**Files:**
- Modify: `backend/internal/httpapi/channels_handlers.go` (`handleChannelScan`, `handlePendingList`)
- Modify: `backend/internal/httpapi/server.go`
- Test: `backend/internal/httpapi/channels_handlers_test.go`

**Interfaces:**
- Consumes: `channels.Store.Backoff` (reused to set `next_scan_at`), `channelvideos.Store.ListPendingForChannel` (Task 7).
- Produces: `POST /api/channels/{id}/scan` returning `{"status":"scheduled"}` or `{"status":"blocked","reason":"..."}`. `GET /api/pending?channel={id}`.

The scheduler holds no in-memory schedule — `Run` polls `ClaimDue(now)` — so scheduling a scan is one UPDATE.

- [ ] **Step 1: Write the failing test**

```go
// TestChannelScan_setsNextScanAt asserts "check now" is exactly one update:
// the scheduler polls ClaimDue(now), so moving next_scan_at into the past is
// the whole mechanism.
func TestChannelScan_setsNextScanAt(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCs", Name: "S"}})
	h := New(deps)
	if rec := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@s", "subscribe": true}); rec.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
	future := time.Now().UTC().Add(6 * time.Hour).Format("2006-01-02 15:04:05")
	if err := deps.Channels.Backoff("UCs", future); err != nil {
		t.Fatalf("push scan into the future: %v", err)
	}

	rec := postJSON(t, h, "/api/channels/UCs/scan", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	sub, err := deps.Channels.GetSubscription("UCs")
	if err != nil {
		t.Fatalf("get subscription: %v", err)
	}
	if sub.NextScanAt >= future {
		t.Fatalf("next_scan_at = %q, want moved earlier than %q", sub.NextScanAt, future)
	}
}

// TestChannelScan_notSubscribed_400 asserts there is nothing to schedule for
// a channel with no subscription, rather than a silent success.
func TestChannelScan_notSubscribed_400(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCt", Name: "T"}})
	h := New(deps)
	if rec := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@t"}); rec.Code != http.StatusCreated {
		t.Fatalf("setup: %d", rec.Code)
	}
	rec := postJSON(t, h, "/api/channels/UCt/scan", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
}

// TestPendingList_channelParam_scopes asserts the New tab sees only its own
// channel's discoveries.
func TestPendingList_channelParam_scopes(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	h := New(deps)
	seedChannelAndPending(t, deps, "UCa", "va")
	seedChannelAndPending(t, deps, "UCb", "vb")

	body := getJSON(t, h, "/api/pending?channel=UCa")
	if !strings.Contains(body, "va") || strings.Contains(body, "vb") {
		t.Fatalf("pending scoping wrong: %s", body)
	}
}
```

Add the seed helper:

```go
func seedChannelAndPending(t *testing.T, deps Deps, channelID, videoID string) {
	t.Helper()
	if err := deps.Channels.Upsert(channels.Channel{ID: channelID, Name: channelID}); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	_, err := deps.Channels.DB().Exec(
		`INSERT INTO channel_videos (video_id, channel_id, title, url, state) VALUES (?, ?, ?, ?, 'pending')`,
		videoID, channelID, "title "+videoID, "https://y/"+videoID)
	if err != nil {
		t.Fatalf("seed pending: %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd backend && go test ./internal/httpapi/ -run 'TestChannelScan|TestPendingList_channelParam' -v`

Expected: FAIL — scan route 404s; `?channel=` is ignored.

- [ ] **Step 3: Implement the scan handler**

```go
// handleChannelScan schedules a scan of one channel by moving its
// next_scan_at into the past. The scheduler holds no in-memory schedule — it
// polls ClaimDue(now) — so this single update IS the mechanism, and the scan
// runs on the scheduler's next poll rather than immediately. The UI must say
// "checking soon", never imply the scan is happening this instant.
//
// Two gates in the scheduler's own loop can still delay it indefinitely: an
// invalid YouTube cookie and the global pause flag. When either is set, say
// so rather than reporting a success the user will never see the result of.
func (s *server) handleChannelScan(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")
	sub, err := s.channels.GetSubscription(id)
	if err != nil {
		serverError(w, r, err, "schedule scan failed")
		return
	}
	if sub == nil {
		writeJSONError(w, http.StatusBadRequest, "channel is not subscribed")
		return
	}

	if s.settings != nil {
		if paused, reason := s.youtubePausedReason(r.Context()); paused {
			writeJSON(w, map[string]string{
				"status": "blocked",
				"reason": "YouTube access is paused" + reason,
			})
			return
		}
		if status, serr := s.settings.CookieStatus(r.Context()); serr == nil && status != "valid" {
			writeJSON(w, map[string]string{
				"status": "blocked",
				"reason": "Your YouTube cookie needs refreshing before peeq can check this channel.",
			})
			return
		}
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	if err := s.channels.Backoff(id, now); err != nil {
		serverError(w, r, err, "schedule scan failed")
		return
	}
	writeJSON(w, map[string]string{"status": "scheduled"})
}
```

Implement `youtubePausedReason` following however `DownloadStatusBanner`'s data is produced today (`downloadsStatus` in `downloads_handlers.go` already reads `youtube_paused` and `youtube_pause_reason` — reuse that reader rather than adding a second one). If the settings accessor names differ, use the existing ones; do not add a parallel path.

Register:

```go
	mux.Handle("POST /api/channels/{id}/scan", s.requireAuth(http.HandlerFunc(s.handleChannelScan)))
```

- [ ] **Step 4: Scope the pending list**

In `handlePendingList`, replace the fetch:

```go
	var items []channelvideos.Entry
	var err error
	if channelID := r.URL.Query().Get("channel"); channelID != "" {
		items, err = s.ledger.ListPendingForChannel(channelID)
	} else {
		items, err = s.ledger.ListPending()
	}
```

- [ ] **Step 5: Run the tests**

Run: `cd backend && go test ./...`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/httpapi/
git -c user.email=trick77@users.noreply.github.com commit -m "feat(channels): add scan-now and channel-scoped pending list"
```

---

### Task 10: Frontend API client and types

**Files:**
- Modify: `ui/src/api/channels.ts`
- Modify: `ui/src/api/pending.ts`
- Modify: `ui/src/api/types.ts`
- Modify: `ui/src/views/Channels.test.tsx` (mock factory — **same commit, mandatory**)

**Interfaces:**
- Consumes: the endpoints from Tasks 7–9.
- Produces: `ChannelDetail` type; `getChannel(id)`, `scanChannel(id)`, `channelAvatarUrl(id)`, `channelBannerUrl(id)` in `ui/src/api/channels.ts`; `listPending(channelId?)` in `ui/src/api/pending.ts`.

- [ ] **Step 1: Add the type**

In `ui/src/api/types.ts`:

```ts
// ChannelDetail mirrors httpapi.channelDetail — one channel's page data.
// Tracked is false for a channel the user has only visited; when it is false
// every subscription field below is at its zero value.
export type ChannelDetail = {
  id: string;
  name: string;
  handle?: string;
  description?: string;
  has_avatar: boolean;
  has_banner: boolean;

  tracked: boolean;
  tracked_at?: string;

  archived_count: number;
  runtime_seconds: number;
  disk_bytes: number;
  newest_published_at?: string;

  subscribed: boolean;
  autodownload: boolean;
  format_override?: string;
  last_scanned_at?: string;
  next_scan_at?: string;
  pending_count: number;
};

// ScanResult mirrors POST /api/channels/{id}/scan. "blocked" carries a
// human-readable reason the scan cannot run — a stale cookie or the global
// YouTube pause — which the UI shows verbatim.
export type ScanResult = { status: "scheduled" | "blocked"; reason?: string };
```

- [ ] **Step 2: Add the client functions**

In `ui/src/api/channels.ts`:

```ts
export async function getChannel(id: string): Promise<ChannelDetail> {
  return api.get<ChannelDetail>(`/api/channels/${encodeURIComponent(id)}`, "failed to load channel");
}

export async function scanChannel(id: string): Promise<ScanResult> {
  return api.post<ScanResult>(`/api/channels/${encodeURIComponent(id)}/scan`, undefined, "failed to schedule a check");
}

export function channelAvatarUrl(id: string): string {
  return `/api/channels/${encodeURIComponent(id)}/avatar`;
}

export function channelBannerUrl(id: string): string {
  return `/api/channels/${encodeURIComponent(id)}/banner`;
}
```

In `ui/src/api/pending.ts`, give `listPending` an optional scope:

```ts
export async function listPending(channelId?: string): Promise<PendingItem[]> {
  const qs = channelId ? `?channel=${encodeURIComponent(channelId)}` : "";
  return api.get<PendingItem[]>(`/api/pending${qs}`, "failed to load pending");
}
```

- [ ] **Step 3: Update the existing mock factory**

In `ui/src/views/Channels.test.tsx`, extend the factory — omitting this makes the new functions `undefined` inside that file:

```tsx
vi.mock("../api/channels", () => ({
  listChannels: vi.fn(),
  addChannel: vi.fn(),
  updateChannel: vi.fn(),
  subscribeChannel: vi.fn(),
  unsubscribeChannel: vi.fn(),
  deleteChannel: vi.fn(),
  getChannel: vi.fn(),
  scanChannel: vi.fn(),
  channelAvatarUrl: (id: string) => `/api/channels/${id}/avatar`,
  channelBannerUrl: (id: string) => `/api/channels/${id}/banner`,
}));
```

- [ ] **Step 4: Typecheck and test**

Run: `cd ui && npx tsc -p tsconfig.app.json --noEmit && npx vitest run`

Expected: no type errors, all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add ui/src/api/ ui/src/views/Channels.test.tsx
git -c user.email=trick77@users.noreply.github.com commit -m "feat(ui): add channel detail API client"
```

---

### Task 11: Routing — a view that takes a parameter

**Files:**
- Modify: `ui/src/shell/Rail.tsx` (`ViewId`)
- Modify: `ui/src/App.tsx` (`VIEW_META`, state, `ViewSwitch`)
- Create: `ui/src/views/Channel.tsx` (placeholder shell)
- Test: `ui/src/App.test.tsx`

**Interfaces:**
- Consumes: nothing.
- Produces: `App` holds `selectedChannelId: string | null` and passes `onOpenChannel: (id: string) => void` into `Library`, `Player`, `Pending`, and `Channels`. `Channel` component signature: `({ channelId, onOpenVideo, onBack }: { channelId: string | null; onOpenVideo: (id: string) => void; onBack: () => void })`.

`ViewSwitch`'s switch has no `default` and `VIEW_META` is a `Record<ViewId, …>`, so adding `"channel"` breaks the build in exactly the two places that must change. That is the intended safety net.

- [ ] **Step 1: Write the failing test**

Add to `ui/src/App.test.tsx`:

Create `ui/src/views/Channel.test.tsx` with the navigation test — it fails now because there is no `Channel` component at all:

```tsx
import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Channel } from "./Channel";

describe("Channel routing", () => {
  it("renders the selected channel's id", () => {
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    expect(screen.getByTestId("channel-page")).toHaveTextContent("UCa");
  });

  it("says so when no channel is selected", () => {
    render(<Channel channelId={null} onOpenVideo={() => {}} onBack={() => {}} />);
    expect(screen.getByText(/no channel selected/i)).toBeInTheDocument();
  });
});
```

Also add to `ui/src/App.test.tsx`:

```tsx
it("channel is not in the nav rail", () => {
  render(<App />);
  // The channel page is reached by clicking a channel name, the way the
  // player is reached by clicking a video — it must not appear as a
  // destination in the rail.
  expect(screen.queryByRole("button", { name: /^channel$/i })).toBeNull();
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd ui && npx vitest run src/views/Channel.test.tsx`

Expected: FAIL — `Cannot find module './Channel'`.

- [ ] **Step 3: Add the view id and the placeholder**

In `ui/src/shell/Rail.tsx`:

```ts
// "channel" is a detail destination reached by clicking a channel name, not
// a rail entry — deliberately absent from SECTIONS below, like "player" is
// reached from a video card. Rail's `active` simply matches nothing then.
export type ViewId =
  | "library"
  | "player"
  | "search"
  | "add"
  | "pending"
  | "channels"
  | "channel"
  | "settings";
```

Create `ui/src/views/Channel.tsx`:

```tsx
export function Channel({
  channelId,
  onOpenVideo,
  onBack,
}: {
  channelId: string | null;
  onOpenVideo: (id: string) => void;
  onBack: () => void;
}) {
  if (!channelId) {
    return <p style={{ color: "var(--color-faint)" }}>No channel selected.</p>;
  }
  return <div data-testid="channel-page">{channelId}</div>;
}
```

In `ui/src/App.tsx`, add to `VIEW_META`:

```tsx
  channel: { title: "Channel" },
```

Add the state and the navigation helper next to `openVideo`:

```tsx
  const [selectedChannelId, setSelectedChannelId] = useState<string | null>(null);

  // openChannel is the channel page's only entry point: there is no rail
  // item for it, so this is called from every place a channel name appears.
  function openChannel(id: string) {
    setSelectedChannelId(id);
    setView("channel");
  }
```

Pass `onOpenChannel={openChannel}` and `selectedChannelId` into `ViewSwitch`, add them to its props type, and add the case:

```tsx
    case "channel":
      return (
        <Channel
          channelId={selectedChannelId}
          onOpenVideo={onOpenVideo}
          onBack={() => setView("channels")}
        />
      );
```

Thread `onOpenChannel` into the four views that render a channel name:

```tsx
    case "library":
      return <Library onOpenVideo={onOpenVideo} onOpenChannel={onOpenChannel} />;
    case "player":
      return (
        <Player
          videoId={selectedVideoId}
          seekTo={pendingSeek}
          onSeekConsumed={onSeekConsumed}
          onDeleted={() => setView("library")}
          onOpenChannel={onOpenChannel}
        />
      );
    case "pending":
      return <Pending onCountChange={setPendingCount} onOpenChannel={onOpenChannel} />;
    case "channels":
      return <Channels onOpenChannel={onOpenChannel} />;
```

- [ ] **Step 4: Typecheck**

Run: `cd ui && npx tsc -p tsconfig.app.json --noEmit`

Expected: errors in `Library.tsx`, `Player.tsx`, `Pending.tsx`, `Channels.tsx` — each does not accept `onOpenChannel` yet. Add the optional prop to each component's props type now (`onOpenChannel?: (id: string) => void`); the links themselves come in Task 15.

Re-run until clean.

- [ ] **Step 5: Run the tests**

Run: `cd ui && npx vitest run`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add ui/src/App.tsx ui/src/shell/Rail.tsx ui/src/views/Channel.tsx ui/src/App.test.tsx ui/src/views/
git -c user.email=trick77@users.noreply.github.com commit -m "feat(ui): route to a channel detail view"
```

---

### Task 12: Channel header and tab shell

**Files:**
- Modify: `ui/src/views/Channel.tsx`
- Modify: `ui/src/index.css`
- Test: `ui/src/views/Channel.test.tsx`

**Interfaces:**
- Consumes: `getChannel`, `channelAvatarUrl`, `channelBannerUrl` (Task 10).
- Produces: `Channel` owns `detail: ChannelDetail | null` and `tab: "archive" | "new" | "settings"`, and renders the three tab bodies from Tasks 13–14.

- [ ] **Step 1: Write the failing test**

Create `ui/src/views/Channel.test.tsx`:

```tsx
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Channel } from "./Channel";
import type { ChannelDetail } from "../api/types";

vi.mock("../api/channels", () => ({
  listChannels: vi.fn(),
  addChannel: vi.fn(),
  updateChannel: vi.fn(),
  subscribeChannel: vi.fn(),
  unsubscribeChannel: vi.fn(),
  deleteChannel: vi.fn(),
  getChannel: vi.fn(),
  scanChannel: vi.fn(),
  channelAvatarUrl: (id: string) => `/api/channels/${id}/avatar`,
  channelBannerUrl: (id: string) => `/api/channels/${id}/banner`,
}));
vi.mock("../api/videos", () => ({ listVideos: vi.fn(), thumbnailUrl: (id: string) => `/t/${id}` }));
vi.mock("../api/pending", () => ({ listPending: vi.fn(), downloadPending: vi.fn(), ignorePending: vi.fn() }));

import { getChannel } from "../api/channels";
import { listVideos } from "../api/videos";
import { listPending } from "../api/pending";

function detail(overrides: Partial<ChannelDetail> = {}): ChannelDetail {
  return {
    id: "UCa",
    name: "Uncanny Expeditions",
    handle: "@UncannyExpeditions",
    description: "Field documentaries.",
    has_avatar: true,
    has_banner: true,
    tracked: true,
    tracked_at: "2026-03-14 09:00:00",
    archived_count: 142,
    runtime_seconds: 219600,
    disk_bytes: 40802189312,
    newest_published_at: "2026-07-18T00:00:00Z",
    subscribed: true,
    autodownload: true,
    format_override: "",
    last_scanned_at: "2026-07-20 08:00:00",
    next_scan_at: "2026-07-20 14:00:00",
    pending_count: 7,
    ...overrides,
  };
}

describe("Channel", () => {
  beforeEach(() => {
    vi.mocked(getChannel).mockReset();
    vi.mocked(listVideos).mockReset();
    vi.mocked(listPending).mockReset();
    vi.mocked(getChannel).mockResolvedValue(detail());
    vi.mocked(listVideos).mockResolvedValue([]);
    vi.mocked(listPending).mockResolvedValue([]);
  });

  it("shows the channel name and its four stats", async () => {
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");
    expect(screen.getByText("142")).toBeInTheDocument();
    expect(screen.getByText(/61 h/)).toBeInTheDocument();
    expect(screen.getByText(/38(\.\d+)? GB/)).toBeInTheDocument();
  });

  it("an untracked channel hides the New and Settings tabs", async () => {
    vi.mocked(getChannel).mockResolvedValue(
      detail({ tracked: false, subscribed: false, pending_count: 0 }),
    );
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");

    expect(screen.getByRole("tab", { name: /archive/i })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: /new/i })).toBeNull();
    expect(screen.queryByRole("tab", { name: /settings/i })).toBeNull();
    expect(screen.getByRole("button", { name: /track this channel/i })).toBeInTheDocument();
  });

  it("switching to the New tab loads that channel's pending videos", async () => {
    const user = userEvent.setup();
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("tab", { name: /new/i }));

    await waitFor(() => {
      expect(listPending).toHaveBeenCalledWith("UCa");
    });
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd ui && npx vitest run src/views/Channel.test.tsx`

Expected: FAIL — the placeholder renders only the id.

- [ ] **Step 3: Implement the header and tabs**

Replace `ui/src/views/Channel.tsx`:

```tsx
import { useEffect, useRef, useState } from "react";
import { Button } from "../ui";
import { Icon } from "../icons";
import { getChannel, channelAvatarUrl, channelBannerUrl, subscribeChannel, unsubscribeChannel } from "../api/channels";
import { gradientClassFor } from "../format";
import type { ChannelDetail } from "../api/types";
import { ArchiveTab } from "./channel/ArchiveTab";
import { NewTab } from "./channel/NewTab";
import { SettingsTab } from "./channel/SettingsTab";

type TabId = "archive" | "new" | "settings";

// formatRuntime renders a total duration as whole hours ("61 h"), falling
// back to minutes below an hour so a small channel does not read "0 h".
function formatRuntime(seconds: number): string {
  if (seconds < 3600) return `${Math.round(seconds / 60)} min`;
  return `${Math.round(seconds / 3600)} h`;
}

// formatBytes renders a size in the largest unit that keeps it readable.
function formatBytes(bytes: number): string {
  if (bytes >= 1e12) return `${(bytes / 1e12).toFixed(1)} TB`;
  if (bytes >= 1e9) return `${(bytes / 1e9).toFixed(1)} GB`;
  if (bytes >= 1e6) return `${Math.round(bytes / 1e6)} MB`;
  return `${Math.round(bytes / 1e3)} kB`;
}

// formatAge renders an ISO timestamp as a coarse "how long ago", matching
// how the rest of peeq talks about time on cards.
function formatAge(iso: string | undefined): string {
  if (!iso) return "—";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "—";
  const days = Math.floor((Date.now() - then) / 86400000);
  if (days <= 0) return "today";
  if (days === 1) return "1 d ago";
  if (days < 30) return `${days} d ago`;
  if (days < 365) return `${Math.round(days / 30)} mo ago`;
  return `${Math.round(days / 365)} y ago`;
}

export function Channel({
  channelId,
  onOpenVideo,
  onBack,
}: {
  channelId: string | null;
  onOpenVideo: (id: string) => void;
  onBack: () => void;
}) {
  const [detail, setDetail] = useState<ChannelDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<TabId>("archive");
  const [busy, setBusy] = useState(false);

  // loadSeq drops out-of-order responses, the same guard Channels.tsx uses:
  // navigating between two channels quickly must not leave the slower
  // response painted over the newer one.
  const loadSeq = useRef(0);

  function reload() {
    if (!channelId) return;
    const seq = ++loadSeq.current;
    setError(null);
    getChannel(channelId)
      .then((d) => {
        if (seq !== loadSeq.current) return;
        setDetail(d);
      })
      .catch((e: Error) => {
        if (seq !== loadSeq.current) return;
        setError(e.message);
      });
  }

  useEffect(() => {
    setDetail(null);
    setTab("archive");
    reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [channelId]);

  async function handleToggleSubscribe() {
    if (!detail) return;
    setBusy(true);
    try {
      if (detail.subscribed) await unsubscribeChannel(detail.id);
      else await subscribeChannel(detail.id);
      reload();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (!channelId) return <p style={{ color: "var(--color-faint)" }}>No channel selected.</p>;
  if (error && !detail) return <div className="errline">{error}</div>;
  if (!detail) return null;

  const tabs: { id: TabId; label: string; count?: number }[] = detail.tracked
    ? [
        { id: "archive", label: "Archive", count: detail.archived_count },
        { id: "new", label: "New", count: detail.pending_count },
        { id: "settings", label: "Settings" },
      ]
    : [{ id: "archive", label: "Archive", count: detail.archived_count }];

  return (
    <div className="chan">
      <header className="chan-head">
        {detail.has_banner ? (
          <div
            className="chan-banner"
            style={{ backgroundImage: `url(${channelBannerUrl(detail.id)})` }}
            aria-hidden="true"
          />
        ) : null}
        <div className="chan-head-in">
          {detail.has_avatar ? (
            <img className="chan-av" src={channelAvatarUrl(detail.id)} alt="" />
          ) : (
            <div className={`chan-av ${gradientClassFor(detail.id)}`} aria-hidden="true" />
          )}
          <div className="chan-id">
            <h2>{detail.name}</h2>
            <div className="chan-handle">
              {detail.handle ? `${detail.handle} · ` : ""}
              {detail.tracked ? (
                <>tracked since {new Date(detail.tracked_at ?? "").toLocaleDateString()}</>
              ) : (
                <span style={{ color: "var(--color-faint)" }}>not tracked</span>
              )}
            </div>
            {detail.description ? <p className="chan-desc">{detail.description}</p> : null}
            <div className="chan-stats">
              <div className="chan-stat">
                <div className="k">{detail.archived_count}</div>
                <div className="l">archived</div>
              </div>
              <div className="chan-stat">
                <div className="k">{formatRuntime(detail.runtime_seconds)}</div>
                <div className="l">runtime</div>
              </div>
              <div className="chan-stat">
                <div className="k">{formatBytes(detail.disk_bytes)}</div>
                <div className="l">on disk</div>
              </div>
              <div className="chan-stat">
                <div className="k">{formatAge(detail.newest_published_at)}</div>
                <div className="l">newest</div>
              </div>
            </div>
          </div>
          <div className="chan-acts">
            <Button type="button" variant="ghost" onClick={onBack}>
              <Icon name="tv" size="16px" /> All channels
            </Button>
            {detail.tracked ? (
              <Button
                type="button"
                variant={detail.subscribed ? "gold" : "secondary"}
                busy={busy}
                onClick={handleToggleSubscribe}
              >
                <Icon name={detail.subscribed ? "starFilled" : "star"} size="16px" />
                {detail.subscribed ? "Subscribed" : "Subscribe"}
              </Button>
            ) : (
              <Button type="button" variant="primary" onClick={() => window.alert("Track flow: see Task 15 note")}>
                Track this channel
              </Button>
            )}
          </div>
        </div>
      </header>

      {error ? <div className="errline">{error}</div> : null}

      <div className="chan-tabs" role="tablist">
        {tabs.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={tab === t.id}
            className={`chan-tab${tab === t.id ? " on" : ""}`}
            onClick={() => setTab(t.id)}
          >
            {t.label}
            {t.count !== undefined ? <span className="chan-cnt">{t.count}</span> : null}
          </button>
        ))}
      </div>

      <div className="chan-body">
        {tab === "archive" ? <ArchiveTab channelId={detail.id} onOpenVideo={onOpenVideo} /> : null}
        {tab === "new" ? <NewTab detail={detail} onChanged={reload} /> : null}
        {tab === "settings" ? <SettingsTab detail={detail} onChanged={reload} onDeleted={onBack} /> : null}
      </div>
    </div>
  );
}
```

**Note on "Track this channel":** the placeholder `window.alert` above must be replaced before this task is done. `POST /api/channels` takes a *URL*, and here we have a UCID — construct `https://www.youtube.com/channel/${detail.id}` and call `addChannel(url, false)`, then `reload()`. Replace the placeholder with:

```tsx
              <Button
                type="button"
                variant="primary"
                busy={busy}
                onClick={async () => {
                  setBusy(true);
                  try {
                    await addChannel(`https://www.youtube.com/channel/${detail.id}`, false);
                    reload();
                  } catch (e) {
                    setError((e as Error).message);
                  } finally {
                    setBusy(false);
                  }
                }}
              >
                Track this channel
              </Button>
```

and import `addChannel` from `../api/channels`.

- [ ] **Step 4: Add the CSS**

Append to `ui/src/index.css`. Note the `chan-` prefix throughout: `.card` is already defined twice in this file and must not be overloaded a third time.

```css
/* ---- channel page ---------------------------------------------------- */
/* Deliberately prefixed `chan-` rather than reusing `.card`, which is
   already defined twice in this file (grid tile ~576, panel ~1202) and
   whose panel definition wins by source order. */
.chan-head {
  position: relative;
  overflow: hidden;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-ui-lg);
  background: var(--color-panel);
  padding: 22px 24px;
  margin-bottom: 4px;
}
/* The banner is a backdrop, not a picture: YouTube channel art is bright and
   busy, and at full strength it fights the palette. Low opacity plus a
   gradient that resolves to the panel colour keeps the header legible. */
.chan-banner {
  position: absolute;
  inset: 0;
  background-size: cover;
  background-position: center;
  opacity: 0.22;
  filter: saturate(0.75);
}
.chan-banner::after {
  content: "";
  position: absolute;
  inset: 0;
  background: linear-gradient(
    180deg,
    color-mix(in srgb, var(--color-bg) 45%, transparent) 0%,
    color-mix(in srgb, var(--color-panel) 88%, transparent) 62%,
    var(--color-panel) 100%
  );
}
.chan-head-in {
  position: relative;
  display: flex;
  gap: 18px;
  align-items: flex-start;
  flex-wrap: wrap;
}
.chan-av {
  width: 68px;
  height: 68px;
  border-radius: 50%;
  flex: none;
  object-fit: cover;
  border: 2px solid color-mix(in srgb, var(--color-ink) 14%, transparent);
}
.chan-id {
  min-width: 240px;
  flex: 1;
}
.chan-id h2 {
  margin: 0 0 2px;
  font-family: var(--font-serif);
  font-size: var(--text-display);
  font-weight: 500;
  letter-spacing: -0.01em;
}
.chan-handle {
  font-size: var(--text-label);
  color: var(--color-muted);
}
.chan-desc {
  margin: 8px 0 0;
  font-size: var(--text-label);
  color: var(--color-muted);
  max-width: 60ch;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.chan-stats {
  display: flex;
  gap: 22px;
  margin-top: 14px;
  flex-wrap: wrap;
}
.chan-stat .k {
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  font-size: 16px;
}
.chan-stat .l {
  font-size: var(--text-micro);
  letter-spacing: 0.1em;
  text-transform: uppercase;
  color: var(--color-faint);
  margin-top: 1px;
}
.chan-acts {
  margin-left: auto;
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}

.chan-tabs {
  display: flex;
  gap: 2px;
  border-bottom: 1px solid var(--color-border-soft);
  margin-bottom: 20px;
}
.chan-tab {
  padding: 11px 14px;
  font-family: var(--font-sans);
  font-size: var(--text-ui);
  color: var(--color-muted);
  background: none;
  border: none;
  border-bottom: 2px solid transparent;
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 7px;
}
.chan-tab:hover {
  color: var(--color-ink-dim);
}
.chan-tab.on {
  color: var(--color-ink);
  border-bottom-color: var(--color-accent);
}
.chan-cnt {
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  font-size: var(--text-micro);
  padding: 1px 6px;
  border-radius: 20px;
  background: var(--color-active);
  color: var(--color-muted);
}
.chan-tab.on .chan-cnt {
  background: var(--color-accent-dim);
  color: var(--color-ink);
}
```

- [ ] **Step 5: Run the tests**

Run: `cd ui && npx vitest run src/views/Channel.test.tsx`

Expected: FAIL until Tasks 13–14 supply the three tab components. Create them as one-line stubs now so this task compiles:

```tsx
// ui/src/views/channel/ArchiveTab.tsx (stub — Task 13 fills it in)
export function ArchiveTab(_: { channelId: string; onOpenVideo: (id: string) => void }) {
  return null;
}
```

with matching stubs for `NewTab` and `SettingsTab`. Then re-run: the header and untracked tests PASS; the New-tab test is completed by Task 13.

- [ ] **Step 6: Commit**

```bash
git add ui/src/views/Channel.tsx ui/src/views/Channel.test.tsx ui/src/views/channel/ ui/src/index.css
git -c user.email=trick77@users.noreply.github.com commit -m "feat(ui): channel page header and tabs"
```

---

### Task 13: Archive and New tabs

**Files:**
- Modify: `ui/src/views/channel/ArchiveTab.tsx`
- Modify: `ui/src/views/channel/NewTab.tsx`
- Modify: `ui/src/index.css`
- Test: `ui/src/views/Channel.test.tsx`

**Interfaces:**
- Consumes: `listVideos({ channel, q, sort, category })` (Task 4), `listPending(channelId)` (Task 10), `SORT_OPTIONS` from `ui/src/views/Library.tsx` (Task 4), `VideoCard` from `ui/src/components/VideoCard.tsx`, `scanChannel` (Task 10).
- Produces: `ArchiveTab({ channelId, onOpenVideo })`, `NewTab({ detail, onChanged })`.

- [ ] **Step 1: Write the failing test**

Add to `ui/src/views/Channel.test.tsx`:

```tsx
  it("the archive tab loads only this channel's videos", async () => {
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(expect.objectContaining({ channel: "UCa" }));
    });
  });

  it("the New tab's empty state says when the next check is due", async () => {
    const user = userEvent.setup();
    vi.mocked(listPending).mockResolvedValue([]);
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("tab", { name: /new/i }));

    expect(await screen.findByText(/nothing new/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /check now/i })).toBeInTheDocument();
  });

  it("a blocked scan shows the reason rather than reporting success", async () => {
    const user = userEvent.setup();
    vi.mocked(scanChannel).mockResolvedValue({
      status: "blocked",
      reason: "Your YouTube cookie needs refreshing before peeq can check this channel.",
    });
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /new/i }));

    await user.click(await screen.findByRole("button", { name: /check now/i }));

    expect(await screen.findByText(/cookie needs refreshing/i)).toBeInTheDocument();
  });
```

Add `scanChannel` to the post-mock import list and give it a default in `beforeEach`:

```tsx
    vi.mocked(scanChannel).mockReset();
    vi.mocked(scanChannel).mockResolvedValue({ status: "scheduled" });
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd ui && npx vitest run src/views/Channel.test.tsx`

Expected: FAIL — the stubs render nothing.

- [ ] **Step 3: Implement `ArchiveTab`**

```tsx
import { useEffect, useRef, useState } from "react";
import { VideoCard } from "../../components/VideoCard";
import { listVideos } from "../../api/videos";
import { getSettings } from "../../api";
import { CATEGORIES } from "../../categories";
import { SORT_OPTIONS } from "../Library";
import { controlClass } from "../../ui";
import type { Video, VideoSort } from "../../api/types";

export function ArchiveTab({
  channelId,
  onOpenVideo,
}: {
  channelId: string;
  onOpenVideo: (id: string) => void;
}) {
  const [videos, setVideos] = useState<Video[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [debouncedQuery, setDebouncedQuery] = useState("");
  const [category, setCategory] = useState("all");
  const [sort, setSort] = useState<VideoSort>("newest");
  const [retentionDays, setRetentionDays] = useState(0);

  // The Archive tab keeps its own search/category/sort state rather than
  // sharing the Library's: visiting a channel must never change what the
  // Library shows when the user goes back to it.
  useEffect(() => {
    const id = setTimeout(() => setDebouncedQuery(query), 250);
    return () => clearTimeout(id);
  }, [query]);

  const loadSeq = useRef(0);

  useEffect(() => {
    const seq = ++loadSeq.current;
    setError(null);
    listVideos({ channel: channelId, q: debouncedQuery, category, sort })
      .then((vs) => {
        if (seq !== loadSeq.current) return;
        setVideos(vs);
      })
      .catch((e: Error) => {
        if (seq !== loadSeq.current) return;
        setError(e.message);
      });
  }, [channelId, debouncedQuery, category, sort]);

  useEffect(() => {
    getSettings()
      .then((s) => setRetentionDays(s.retention_days))
      .catch(() => setRetentionDays(0));
  }, []);

  return (
    <>
      <div className="listbar">
        <input
          className={controlClass}
          style={{ maxWidth: 280 }}
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search this channel"
          aria-label="Search this channel"
        />
        <select
          className={controlClass}
          style={{ maxWidth: 200 }}
          value={category}
          onChange={(e) => setCategory(e.target.value)}
          aria-label="Category"
        >
          <option value="all">All categories</option>
          {CATEGORIES.map((c) => (
            <option key={c.id} value={c.id}>
              {c.label}
            </option>
          ))}
        </select>
        <select
          className={controlClass}
          style={{ maxWidth: 180 }}
          value={sort}
          onChange={(e) => setSort(e.target.value as VideoSort)}
          aria-label="Sort"
        >
          {SORT_OPTIONS.map((o) => (
            <option key={o.id} value={o.id}>
              {o.label}
            </option>
          ))}
        </select>
      </div>

      {error ? <div className="errline">{error}</div> : null}

      <div className="grid">
        {videos.map((v) => (
          <VideoCard
            key={v.id}
            video={v}
            retentionDays={retentionDays}
            onOpen={onOpenVideo}
            onToggleFavorite={() => {}}
            onToggleWatched={() => {}}
          />
        ))}
      </div>
      {videos.length === 0 && !error ? (
        <p style={{ color: "var(--color-faint)" }}>
          {debouncedQuery || category !== "all" ? "No videos match." : "Nothing archived from this channel yet."}
        </p>
      ) : null}
    </>
  );
}
```

- [ ] **Step 4: Implement `NewTab`**

```tsx
import { useEffect, useState } from "react";
import { Button } from "../../ui";
import { Icon } from "../../icons";
import { listPending, downloadPending, ignorePending } from "../../api/pending";
import { scanChannel } from "../../api/channels";
import { formatDuration } from "../../format";
import type { ChannelDetail, PendingItem } from "../../api/types";

// scheduleLine renders the "last checked / next check" sentence shown in
// both the populated and the empty state. next_scan_at in the past means the
// scheduler simply has not reached this channel yet.
function scheduleLine(detail: ChannelDetail): string {
  const parts: string[] = [];
  if (detail.last_scanned_at) {
    parts.push(`Checked ${new Date(detail.last_scanned_at + "Z").toLocaleString()}`);
  } else {
    parts.push("Never checked");
  }
  if (detail.next_scan_at) {
    const next = new Date(detail.next_scan_at + "Z").getTime();
    parts.push(next <= Date.now() ? "next check due now" : `next check ${new Date(next).toLocaleString()}`);
  }
  return parts.join(" · ");
}

export function NewTab({ detail, onChanged }: { detail: ChannelDetail; onChanged: () => void }) {
  const [items, setItems] = useState<PendingItem[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [scanning, setScanning] = useState(false);

  function load() {
    setError(null);
    listPending(detail.id)
      .then(setItems)
      .catch((e: Error) => setError(e.message));
  }

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [detail.id]);

  async function handleScan() {
    setScanning(true);
    setNotice(null);
    setError(null);
    try {
      const res = await scanChannel(detail.id);
      // The scheduler polls for due channels rather than being told to run,
      // so a successful call means "queued", never "done".
      setNotice(
        res.status === "blocked"
          ? (res.reason ?? "peeq cannot check this channel right now.")
          : "Checking soon — peeq will look for new videos on its next pass.",
      );
      onChanged();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setScanning(false);
    }
  }

  async function decide(item: PendingItem, keep: boolean) {
    setBusyId(item.video_id);
    try {
      if (keep) await downloadPending(item.video_id);
      else await ignorePending(item.video_id);
      setItems((prev) => prev.filter((i) => i.video_id !== item.video_id));
      onChanged();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusyId(null);
    }
  }

  const checkNow = (
    <Button type="button" variant="secondary" busy={scanning} onClick={handleScan}>
      <Icon name="clock" size="16px" /> Check now
    </Button>
  );

  return (
    <>
      {error ? <div className="errline">{error}</div> : null}
      {notice ? <div className="hint">{notice}</div> : null}

      {items.length === 0 ? (
        <div className="chan-empty">
          <div className="big">Nothing new</div>
          <div className="sub">{scheduleLine(detail)}</div>
          <div style={{ marginTop: 14 }}>{checkNow}</div>
        </div>
      ) : (
        <>
          <div className="listbar">
            <span style={{ fontSize: "var(--text-label)", color: "var(--color-faint)" }}>
              {scheduleLine(detail)}
            </span>
            <span style={{ marginLeft: "auto", display: "flex", gap: 8 }}>{checkNow}</span>
          </div>
          <div className="chan-plist">
            {items.map((item) => (
              <div key={item.video_id} className="chan-prow">
                <img className="chan-pthumb" src={item.thumbnail_url} alt="" loading="lazy" />
                <div className="chan-pt">
                  <div className="ti">{item.title}</div>
                  <div className="sub">{formatDuration(item.duration_seconds)}</div>
                </div>
                <div style={{ display: "flex", gap: 6 }}>
                  <Button
                    type="button"
                    variant="secondary"
                    disabled={busyId === item.video_id}
                    onClick={() => decide(item, true)}
                  >
                    <Icon name="download" size="16px" /> Add
                  </Button>
                  <Button
                    type="button"
                    variant="dangerQuiet"
                    disabled={busyId === item.video_id}
                    onClick={() => decide(item, false)}
                  >
                    Ignore
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </>
      )}
    </>
  );
}
```

- [ ] **Step 5: Add the CSS**

```css
.chan-empty {
  text-align: center;
  padding: 46px 20px;
  color: var(--color-faint);
}
.chan-empty .big {
  font-family: var(--font-serif);
  font-size: var(--text-title);
  color: var(--color-muted);
  margin-bottom: 5px;
}
.chan-empty .sub {
  font-size: var(--text-label);
}
.chan-plist {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.chan-prow {
  display: flex;
  gap: 12px;
  align-items: center;
  padding: 9px 10px;
  border-radius: var(--radius-ui-sm);
}
.chan-prow:hover {
  background: var(--color-active);
}
.chan-pthumb {
  width: 104px;
  aspect-ratio: 16 / 9;
  object-fit: cover;
  border-radius: 6px;
  flex: none;
  border: 1px solid var(--color-border);
}
.chan-pt {
  flex: 1;
  min-width: 0;
}
.chan-pt .ti {
  font-size: var(--text-ui);
  line-height: 1.35;
}
.chan-pt .sub {
  color: var(--color-faint);
  font-size: var(--text-label);
  margin-top: 3px;
}
```

- [ ] **Step 6: Run the tests**

Run: `cd ui && npx vitest run`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add ui/src/views/channel/ ui/src/views/Channel.test.tsx ui/src/index.css
git -c user.email=trick77@users.noreply.github.com commit -m "feat(ui): channel archive and new tabs"
```

---

### Task 14: Settings tab

**Files:**
- Modify: `ui/src/views/channel/SettingsTab.tsx`
- Modify: `ui/src/index.css`
- Test: `ui/src/views/Channel.test.tsx`

**Interfaces:**
- Consumes: `updateChannel`, `scanChannel`, `deleteChannel`, `subscribeChannel`, `unsubscribeChannel` (existing + Task 10).
- Produces: `SettingsTab({ detail, onChanged, onDeleted })`.

- [ ] **Step 1: Write the failing test**

```tsx
  it("the delete button names how many videos it will destroy", async () => {
    const user = userEvent.setup();
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    expect(await screen.findByRole("button", { name: /delete channel and its 142 videos/i })).toBeInTheDocument();
  });

  it("toggling auto-add saves it", async () => {
    const user = userEvent.setup();
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    await user.click(await screen.findByLabelText(/add new videos automatically/i));

    await waitFor(() => {
      expect(updateChannel).toHaveBeenCalledWith("UCa", { autodownload: false });
    });
  });
```

Add `updateChannel` and `deleteChannel` to the post-mock imports and give them defaults in `beforeEach`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd ui && npx vitest run src/views/Channel.test.tsx`

Expected: FAIL — the stub renders nothing.

- [ ] **Step 3: Implement**

```tsx
import { useState } from "react";
import { Button, controlClass } from "../../ui";
import { Icon } from "../../icons";
import {
  updateChannel,
  scanChannel,
  deleteChannel,
  subscribeChannel,
  unsubscribeChannel,
} from "../../api/channels";
import type { ChannelDetail } from "../../api/types";

export function SettingsTab({
  detail,
  onChanged,
  onDeleted,
}: {
  detail: ChannelDetail;
  onChanged: () => void;
  onDeleted: () => void;
}) {
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [format, setFormat] = useState(detail.format_override ?? "");
  const [busy, setBusy] = useState(false);

  async function run(fn: () => Promise<unknown>) {
    setBusy(true);
    setError(null);
    try {
      await fn();
      onChanged();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function handleScan() {
    setNotice(null);
    setError(null);
    try {
      const res = await scanChannel(detail.id);
      setNotice(
        res.status === "blocked"
          ? (res.reason ?? "peeq cannot check this channel right now.")
          : "Checking soon — peeq will look for new videos on its next pass.",
      );
      onChanged();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function handleDelete() {
    const ok = window.confirm(
      `Delete ${detail.name} and its ${detail.archived_count} videos? This removes the files from disk, including any you kept forever. This cannot be undone.`,
    );
    if (!ok) return;
    setBusy(true);
    try {
      await deleteChannel(detail.id);
      onDeleted();
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  }

  return (
    <>
      {error ? <div className="errline">{error}</div> : null}
      {notice ? <div className="hint">{notice}</div> : null}

      <div className="chan-settings">
        <div className="chan-srow">
          <div>
            <div className="lab">Subscribed</div>
            <div className="hint">peeq checks this channel for new uploads on a schedule.</div>
          </div>
          <Button
            type="button"
            variant={detail.subscribed ? "gold" : "secondary"}
            busy={busy}
            onClick={() =>
              run(() => (detail.subscribed ? unsubscribeChannel(detail.id) : subscribeChannel(detail.id)))
            }
          >
            <Icon name={detail.subscribed ? "starFilled" : "star"} size="16px" />
            {detail.subscribed ? "Subscribed" : "Subscribe"}
          </Button>
        </div>

        {detail.subscribed ? (
          <>
            <div className="chan-srow">
              <div>
                <label className="lab" htmlFor="chan-autoadd">
                  Add new videos automatically
                </label>
                <div className="hint">
                  New uploads download without asking. Off means they wait in the New tab.
                </div>
              </div>
              <input
                id="chan-autoadd"
                type="checkbox"
                checked={detail.autodownload}
                onChange={() => run(() => updateChannel(detail.id, { autodownload: !detail.autodownload }))}
              />
            </div>

            <div className="chan-srow">
              <div>
                <label className="lab" htmlFor="chan-format">
                  Format override
                </label>
                <div className="hint">Leave empty to use your global format setting.</div>
              </div>
              <input
                id="chan-format"
                className={controlClass}
                style={{ maxWidth: 220 }}
                type="text"
                value={format}
                onChange={(e) => setFormat(e.target.value)}
                onBlur={() => {
                  if (format !== (detail.format_override ?? "")) {
                    run(() => updateChannel(detail.id, { format_override: format }));
                  }
                }}
                placeholder="Use the default"
              />
            </div>

            <div className="chan-srow">
              <div>
                <div className="lab">Checking for new videos</div>
                <div className="hint">
                  {detail.last_scanned_at
                    ? `Last checked ${new Date(detail.last_scanned_at + "Z").toLocaleString()}`
                    : "Never checked"}
                  {detail.next_scan_at
                    ? ` · next check ${new Date(detail.next_scan_at + "Z").toLocaleString()}`
                    : ""}
                </div>
              </div>
              <Button type="button" variant="secondary" onClick={handleScan}>
                Check now
              </Button>
            </div>
          </>
        ) : null}
      </div>

      <div className="chan-danger">
        <Button type="button" variant="dangerQuiet" busy={busy} onClick={handleDelete}>
          <Icon name="trash" size="16px" /> Delete channel and its {detail.archived_count} videos
        </Button>
      </div>
    </>
  );
}
```

- [ ] **Step 4: Add the CSS**

```css
.chan-settings {
  border: 1px solid var(--color-border);
  border-radius: var(--radius-ui);
  background: var(--color-panel);
  padding: 0 18px;
  max-width: 640px;
}
.chan-srow {
  display: flex;
  align-items: center;
  gap: 16px;
  justify-content: space-between;
  padding: 15px 0;
  border-bottom: 1px solid var(--color-border-soft);
}
.chan-srow:last-child {
  border-bottom: 0;
}
.chan-srow .lab {
  font-size: var(--text-ui);
  color: var(--color-ink-dim);
  display: block;
}
.chan-srow .hint {
  font-size: var(--text-label);
  color: var(--color-faint);
  margin: 3px 0 0;
  max-width: 46ch;
}
.chan-srow input[type="checkbox"] {
  accent-color: var(--color-accent);
  width: 16px;
  height: 16px;
  flex: none;
}
/* Delete sits outside the settings panel: everything inside it is
   reversible, and this is not. */
.chan-danger {
  margin-top: 18px;
  max-width: 640px;
}
```

- [ ] **Step 5: Run the tests**

Run: `cd ui && npx vitest run`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add ui/src/views/channel/SettingsTab.tsx ui/src/views/Channel.test.tsx ui/src/index.css
git -c user.email=trick77@users.noreply.github.com commit -m "feat(ui): channel settings tab"
```

---

### Task 15: Entry points and the toned-down channel row

**Files:**
- Modify: `ui/src/views/Channels.tsx` (link the name, tone down the counts line)
- Modify: `ui/src/components/VideoCard.tsx`
- Modify: `ui/src/views/Player.tsx`
- Modify: `ui/src/views/Pending.tsx`
- Modify: `ui/src/index.css`
- Test: `ui/src/views/Channels.test.tsx`

**Interfaces:**
- Consumes: `onOpenChannel?: (id: string) => void` on all four components (Task 11).
- Produces: a shared `.chan-link` class.

`VideoCard`'s thumbnail is already a `<button>`; the `.by` div is a **sibling** of it, so a `<button>` there is valid. Do not nest one inside the thumbnail button.

- [ ] **Step 1: Write the failing test**

Add to `ui/src/views/Channels.test.tsx`:

```tsx
  it("clicking a channel's name opens its page", async () => {
    const user = userEvent.setup();
    const onOpenChannel = vi.fn();
    render(<Channels onOpenChannel={onOpenChannel} />);
    await screen.findByText("Tracked Channel");

    await user.click(screen.getByRole("button", { name: "Tracked Channel" }));

    expect(onOpenChannel).toHaveBeenCalledWith("c1");
  });
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd ui && npx vitest run src/views/Channels.test.tsx`

Expected: FAIL — the name is an `<h3>`, not a button.

- [ ] **Step 3: Link the four entry points**

In `ui/src/views/Channels.tsx`, replace the row heading and tone down the counts line. The line currently renders at near-full text brightness and competes with the name beside it:

```tsx
            <div className="channel-info">
              <h3 style={{ margin: 0, fontFamily: "var(--font-serif)", fontSize: 17, fontWeight: 500 }}>
                {onOpenChannel ? (
                  <button type="button" className="chan-link" onClick={() => onOpenChannel(c.id)}>
                    {c.name}
                  </button>
                ) : (
                  c.name
                )}
              </h3>
              <div className="channel-by">
                {c.handle ? `${c.handle} · ` : ""}
                <b>{c.pending_count}</b> pending · <b>{c.downloaded_count}</b> downloaded
              </div>
            </div>
```

In `ui/src/components/VideoCard.tsx`, inside the `.by` div:

```tsx
      <div className="by">
        {onOpenChannel && video.channel_id ? (
          <button type="button" className="chan-link" onClick={() => onOpenChannel(video.channel_id)}>
            {video.channel_name || video.channel_id}
          </button>
        ) : (
          video.channel_name || video.channel_id
        )}
```

Apply the same pattern to the `.sub` block in `ui/src/views/Player.tsx` and the `.by` div in `ui/src/views/Pending.tsx` (Pending uses `item.channel_id`). Add `onOpenChannel?: (id: string) => void` to each props type and thread it from `Library` down to `VideoCard`.

- [ ] **Step 4: Add the CSS**

```css
/* chan-link — a channel name that navigates. A button, not an anchor: peeq
   has no router and no URLs to link to. */
.chan-link {
  background: none;
  border: none;
  padding: 0;
  font: inherit;
  color: inherit;
  cursor: pointer;
  border-bottom: 1px solid transparent;
}
.chan-link:hover {
  border-bottom-color: var(--color-accent);
}
.chan-link:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 2px;
  border-radius: 2px;
}

/* The counts line under a channel name. Faint and one step smaller than the
   old `.by` treatment so the name leads; only the numerals lift to muted so
   they stay scannable. */
.channel-by {
  margin-top: 2px;
  font-size: var(--text-label);
  color: var(--color-faint);
}
.channel-by b {
  font-weight: 500;
  color: var(--color-muted);
  font-variant-numeric: tabular-nums;
}
```

Remove the now-unused inline `style={{ marginTop: 2 }}` from that div.

- [ ] **Step 5: Run the tests and typecheck**

Run: `cd ui && npx tsc -p tsconfig.app.json --noEmit && npx vitest run`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add ui/src/views/ ui/src/components/VideoCard.tsx ui/src/index.css
git -c user.email=trick77@users.noreply.github.com commit -m "feat(ui): link channel names, tone down the channel row counts"
```

---

### Task 16: Full verification in a browser

**Files:** none — this task changes nothing. It exists because the last design change in this repository passed every test and was still wrong in the browser.

- [ ] **Step 1: Run the full suite**

```bash
cd backend && go test ./... && go vet ./...
cd ../ui && npx tsc -p tsconfig.app.json --noEmit && npx vitest run && npm run build
```

Expected: all green. `npm run build` dirties `backend/web/dist/index.html` — that file is tracked on purpose; leave it alone rather than "fixing" it.

- [ ] **Step 2: Wipe the database and restart**

The schema was squashed, so the existing database is incompatible. Ask the user before deleting anything — the path depends on their environment.

- [ ] **Step 3: Look at it**

Start the dev stack and check each of these by eye. A green test suite does not demonstrate any of them:

- Track a channel with loud, bright channel art. The banner must read as a **backdrop** — the name, handle, and stats stay fully legible on top of it. If they do not, lower `.chan-banner`'s opacity rather than changing the palette.
- A channel with no banner and no avatar falls back to the gradient and does not collapse the header layout.
- The toned-down counts line under a channel name is still readable — faint, not invisible.
- All four entry points navigate: Channels row, a Library card, the player, and a Pending item.
- Click a channel name on a video you added by URL. The page opens, the header is sparse at first, and after a reload the avatar, banner, and description have filled in.
- Tab counts match reality, and the New tab's empty state shows a sensible last-checked/next-check line.
- "Check now" says "Checking soon", not a spinner implying it is running. Then break it deliberately: pause YouTube in Settings, press it again, and confirm the page shows the reason instead of appearing to do nothing.
- Resize to a narrow window. The header wraps, and the page body does not scroll sideways.

- [ ] **Step 4: Update the docs**

Add the channel page's manual checks to `docs/manual-verification.md`, following the existing format.

- [ ] **Step 5: Commit and open the PR**

```bash
git add docs/manual-verification.md
git -c user.email=trick77@users.noreply.github.com commit -m "docs: manual verification steps for the channel page"
git push -u origin worktree-feat+channel-page
gh pr create --base master --title "Channel page" --body "Implements docs/superpowers/specs/2026-07-20-peeq-channel-page-design.html"
```

`master` is protected and requires a PR plus four green CI checks. Never push to it directly.

---

## Self-Review Notes

**Spec coverage.** Every section of the design spec maps to a task: schema/cache split → 1, guards → 2, search+sort → 3–4, images and description → 5–6, detail endpoint and stats → 7, channel-scoped videos and background resolve → 8, scan-now and scoped pending → 9, client → 10, routing → 11, header/tabs → 12, Archive/New → 13, Settings → 14, entry points and the toned-down row → 15, browser verification → 16.

**Two deviations from the spec, both deliberate:**

1. The spec says images are fetched only at track time. Task 8 also fetches them on first visit to an *untracked* channel — this is the later "pull all info for untracked too" decision, and the spec was updated to match (see its *Populating the cache* section).
2. The spec does not mention `resolved_at`. It is required to stop a permanently unresolvable channel from being re-fetched on every page load, and Task 8's second test pins that behavior.

**Known risk not covered by tests.** `parseChannelInfo` selects images by yt-dlp's thumbnail ids `avatar_uncropped` and `banner_uncropped`. Those ids are yt-dlp's, not a documented YouTube API contract, and could change. If they do, the images silently go missing rather than breaking anything — verify them against real output during Task 16 rather than trusting the unit test's fixture.
