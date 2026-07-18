# vark Phase 2 — Channels & Subscriptions — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Follow YouTube channels — **track** a channel (identity only, no action) vs **subscribe** (daily flat scan); genuinely-new uploads on autodownload channels enqueue automatically at low priority, others land in a **New & pending** list to Download-now or Ignore.

**Architecture:** Reuse Phase 1 wholesale. Two new yt-dlp wrapper methods (`ResolveChannel`, `ChannelVideos`) go through the existing `exec` choke point (cookie gate + 20s throttle). A single new **scan scheduler** goroutine (mirrors the download worker's `Run(ctx)` loop) discovers new videos and either enqueues them on the existing `jobs.Store`/`download.Worker` or records them in a `channel_videos` ledger for user decision. Three new tables (`channels`, `subscriptions`, `channel_videos`) plus one squashed migration file. New "Channels" rail item + "New & pending" view on the existing React shell.

**Tech Stack:** Go 1.25 · `net/http` (Go 1.22 method routing) · `ncruces/go-sqlite3` · React 19 · Vite · Tailwind v4 · `lucide-react` · Vitest · yt-dlp (binary, faked in tests).

## Global Constraints

- Module `github.com/trick77/vark`. Go 1.25. `CGO_ENABLED=0` everywhere.
- **Hard invariant:** NO call to YouTube without a valid cookie. Both new wrapper methods funnel through `Runner.exec`, which calls `cookieGate()` before anything.
- **Throttle invariant:** the 20s minimum floor + random jitter runs before every yt-dlp invocation via `Runner.exec`. The scan scheduler adds a **≥60s between-channel** spacing on top.
- Branch `feat/phase-2-channels` (off `master`, already created). Never commit to `master`. Conventional Commits. YAML `.yaml`. English comments only.
- TDD: failing test first, then minimal implementation. Every DB-writing table access goes through a store method (no ad-hoc SQL in handlers, except the delete-cascade orchestration which is explicitly a store method).
- Automated tests use a **fake yt-dlp stub** — never the real binary. The full authenticated e2e (real cookie) is a manual step.
- Design system = `../music` (Warm Editorial dark), lucide icons stroke-width 1.9, Anthropic fonts. UI copy English.
- **Squashed migrations:** until vark ships, ALL schema lives in a single `0001_init.sql`. Dev DBs are disposable and must be recreated (`rm ./data/vark.db`) after Task 1.
- **NOT in P2 (Phase 3):** subtitles, summaries, embeddings, transcript/caption display. Autodownloaded videos are ordinary downloads (no subtitles).
- **Deferred to the NEXT phase (do NOT build):** auto-unsubscribe of channels idle > N weeks (3-month default) + the stale-channel filter.

---

## File Structure

**Backend (new):**
- `backend/internal/channels/store.go` — `channels` + `subscriptions` tables: track/list/subscribe/config/claim-due/mark-scanned/delete-cascade.
- `backend/internal/channelvideos/store.go` — the `channel_videos` scan ledger: exists/insert/set-state/list-pending.
- `backend/internal/scan/scheduler.go` — the scan scheduler goroutine.
- `backend/internal/ytdlp/channel.go` — `ChannelVideos`, `ResolveChannel`, `ChannelEntry`.
- `backend/internal/httpapi/channels_handlers.go` — channels + pending REST.

**Backend (modified):**
- `backend/internal/store/migrations/0001_init.sql` — squashed (0002 folded in) + 3 new tables + 2 new columns; delete `0002_auth.sql`.
- `backend/internal/settings/store.go` — `min_video_duration_seconds`.
- `backend/internal/httpapi/settings_handlers.go` — validate the new setting.
- `backend/internal/ytdlp/url.go` — `channel` kind.
- `backend/internal/videos/store.go` — `requested_format` column (per-channel format override).
- `backend/internal/download/worker.go` — prefer `video.RequestedFormat`.
- `backend/internal/httpapi/downloads_handlers.go` — reject `channel` kind; auto-track hook.
- `backend/internal/httpapi/server.go` — register routes + `Deps` fields.
- `backend/cmd/vark/main.go` — start scheduler goroutine, wire new stores/runner.

**Frontend (new):** `ui/src/api/channels.ts`, `ui/src/api/pending.ts`, `ui/src/views/Channels.tsx`, `ui/src/views/Pending.tsx`.
**Frontend (modified):** `ui/src/api/types.ts`, `ui/src/api/index.ts`, `ui/src/shell/Rail.tsx`, `ui/src/App.tsx`, `ui/src/views/Add.tsx`, `ui/src/views/Settings.tsx`.

**Data model recap (all in the squashed `0001_init.sql`):**
- `channels(id PK UCID, handle, name, avatar_path, added_at)` — presence = tracked.
- `subscriptions(channel_id PK→channels ON DELETE CASCADE, autodownload, format_override, baselined_at, last_scanned_at, next_scan_at, created_at)` — presence = subscribed.
- `channel_videos(video_id PK, channel_id→channels ON DELETE CASCADE, title, duration_seconds, url, thumbnail_url, state CHECK IN (seen,pending,ignored,queued), discovered_at, decided_at)` — scan ledger / pending list / dedup set.
- `settings.min_video_duration_seconds INTEGER NOT NULL DEFAULT 180`.
- `videos.requested_format TEXT NOT NULL DEFAULT ''` — per-channel format override for this video's download.

**State/table cheat-sheet (avoid the P1-vs-P2 traps):**
- A **pending** item has ONLY a `channel_videos` row — NO `videos` row yet. Pending endpoints read/write `channel_videos`. "Download now" Upserts the `videos` row *then* enqueues.
- `channel_videos.thumbnail_url` is a **remote** URL (rendered directly by the UI). `videos.thumbnail_path` is a **local** path served by `handleVideoThumbnail`. Never copy one into the other.
- Dedup: act on a scanned id only if it exists in **neither** `channel_videos` **nor** `videos`.

---

## Task 1: Squash migrations + P2 schema + new settings/videos columns

**Files:**
- Modify: `backend/internal/store/migrations/0001_init.sql` (fold in 0002; add tables + columns)
- Delete: `backend/internal/store/migrations/0002_auth.sql`
- Modify: `backend/internal/settings/store.go`
- Modify: `backend/internal/httpapi/settings_handlers.go`
- Test: `backend/internal/store/store_test.go` (extend), `backend/internal/settings/store_test.go` (extend)

**Interfaces:**
- Produces: `settings.Settings.MinVideoDurationSeconds int` (json `min_video_duration_seconds`); `settings.Patch.MinVideoDurationSeconds *int`; new tables `channels`, `subscriptions`, `channel_videos`; new columns `settings.min_video_duration_seconds` (default 180), `videos.requested_format` (default '').

- [ ] **Step 1: Failing test — migrate creates the P2 tables.** Add to `backend/internal/store/store_test.go`:
```go
func TestMigrate_createsPhase2Tables(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"channels", "subscriptions", "channel_videos", "users", "sessions"} {
		var n int
		if err := db.QueryRow(
			"select count(*) from sqlite_master where type='table' and name=?", tbl,
		).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s missing (n=%d err=%v)", tbl, n, err)
		}
	}
}
```

- [ ] **Step 2: Run — expect FAIL.** `cd backend && go test ./internal/store/ -run TestMigrate_createsPhase2Tables -v` → FAIL (channels/subscriptions/channel_videos missing).

- [ ] **Step 3: Squash `0002_auth.sql` into `0001_init.sql` and delete it.** Append the ENTIRE contents of `0002_auth.sql` (the `users` and `sessions` tables + their indexes, verbatim) to the end of `0001_init.sql`, then `git rm backend/internal/store/migrations/0002_auth.sql`. (The runner records applied migrations by filename; on a fresh DB only `0001_init.sql` runs. Existing dev DBs must be recreated — see Task 15's README note.)

- [ ] **Step 4: Add the new columns + tables to `0001_init.sql`.** In the `settings` table add, after `min_free_gb`:
```sql
    min_video_duration_seconds INTEGER NOT NULL DEFAULT 180,
```
In the `videos` table add, after `format_used`:
```sql
    requested_format         TEXT NOT NULL DEFAULT '',
```
Append the three new tables (after the folded-in `sessions` block):
```sql
-- channels: a tracked YouTube channel (identity only). Presence == "tracked".
CREATE TABLE channels (
    id          TEXT PRIMARY KEY,          -- the channel UCID (UC...)
    handle      TEXT NOT NULL DEFAULT '',  -- @handle if known
    name        TEXT NOT NULL DEFAULT '',
    avatar_path TEXT NOT NULL DEFAULT '',  -- reserved, unused in P2
    added_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

-- subscriptions: presence == "subscribed". One row per subscribed channel.
CREATE TABLE subscriptions (
    channel_id      TEXT PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    autodownload    INTEGER NOT NULL DEFAULT 0,
    format_override TEXT NOT NULL DEFAULT '',
    baselined_at    TEXT,                       -- NULL until the first scan completes
    last_scanned_at TEXT,
    next_scan_at    TEXT NOT NULL,              -- set to now on subscribe
    created_at      TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_subscriptions_next_scan_at ON subscriptions(next_scan_at);

-- channel_videos: per-channel scan ledger + pending list + dedup set.
CREATE TABLE channel_videos (
    video_id         TEXT PRIMARY KEY,
    channel_id       TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    title            TEXT NOT NULL DEFAULT '',
    duration_seconds INTEGER,
    url              TEXT NOT NULL DEFAULT '',
    thumbnail_url    TEXT NOT NULL DEFAULT '',   -- REMOTE url (not a local path)
    state            TEXT NOT NULL DEFAULT 'seen' CHECK (state IN ('seen','pending','ignored','queued')),
    discovered_at    TEXT NOT NULL DEFAULT (datetime('now')),
    decided_at       TEXT
);
CREATE INDEX idx_channel_videos_channel ON channel_videos(channel_id);
CREATE INDEX idx_channel_videos_state ON channel_videos(state);
```
Also update the seed `INSERT OR IGNORE INTO settings (...)` to include the new column so the singleton row has it explicitly:
```sql
INSERT OR IGNORE INTO settings (id, format_preset, retention_days, throttle_base_seconds, min_free_gb, cookie_status, min_video_duration_seconds)
VALUES (1, 'apple-1080p', 14, 10, 5, 'absent', 180);
```

- [ ] **Step 5: Run — expect PASS.** `cd backend && go test ./internal/store/ -v`.

- [ ] **Step 6: Failing test — settings round-trips the new field.** Add to `backend/internal/settings/store_test.go` (follow the existing test's setup for opening a migrated DB):
```go
func TestUpdate_minVideoDurationSeconds(t *testing.T) {
	s := newTestStore(t) // existing helper in this test file
	want := 300
	if err := s.Update(context.Background(), Patch{MinVideoDurationSeconds: &want}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.MinVideoDurationSeconds != want {
		t.Fatalf("min_video_duration_seconds = %d, want %d", got.MinVideoDurationSeconds, want)
	}
	// Default is 180 on a fresh row — sanity that the column exists & seeds.
}
```
(If the test file has no `newTestStore` helper, open a migrated temp DB the same way the neighboring tests do.)

- [ ] **Step 7: Run — expect FAIL** (no such field). 

- [ ] **Step 8: Implement the settings store changes.** In `backend/internal/settings/store.go`:
  - Add to `Settings`: `MinVideoDurationSeconds int \`json:"min_video_duration_seconds"\`` (after `MinFreeGB`).
  - Add to `Patch`: `MinVideoDurationSeconds *int`.
  - In `Get`, add `min_video_duration_seconds` to the SELECT column list and `&st.MinVideoDurationSeconds` to the Scan (keep column/scan order aligned — place it after `min_free_gb`, before `ytdlp_version`).
  - In `Update`, add `min_video_duration_seconds = COALESCE(?, min_video_duration_seconds),` to the SET clause and `patch.MinVideoDurationSeconds` to the args (matching position).

- [ ] **Step 9: Run — expect PASS.** `cd backend && go test ./internal/settings/ -v`.

- [ ] **Step 10: Wire PUT validation.** In `backend/internal/httpapi/settings_handlers.go`:
  - Add `MinVideoDurationSeconds *int \`json:"min_video_duration_seconds"\`` to `settingsPatchRequest`.
  - Add `{"min_video_duration_seconds", req.MinVideoDurationSeconds}` to the negative-rejection slice.
  - Add `MinVideoDurationSeconds: req.MinVideoDurationSeconds` to the `settings.Patch{...}` literal.

- [ ] **Step 11: Full backend build + test.** `cd backend && go build ./... && go test ./internal/store/ ./internal/settings/ ./internal/httpapi/ -v`. Expected: PASS.

- [ ] **Step 12: Commit.**
```bash
git add backend/internal/store backend/internal/settings backend/internal/httpapi/settings_handlers.go
git commit -m "feat: squash migrations + channels/subscriptions/channel_videos schema + min-duration & requested-format columns"
```

---

## Task 2: URL canonicalize `channel` kind + reject channels at `/api/downloads`

**Files:**
- Modify: `backend/internal/ytdlp/url.go`
- Modify: `backend/internal/httpapi/downloads_handlers.go`
- Test: `backend/internal/ytdlp/url_test.go` (extend)

**Interfaces:**
- Produces: `Canonicalize` now recognizes channel URLs and returns `kind == "channel"` with `watchURL` = the canonical channel URL and `id` = the channel identifier (UCID when the URL carries one, else the `@handle`/custom slug). It returns EARLY (before the 11-char video-id check).

- [ ] **Step 1: Failing test — channel URLs canonicalize.** Add to `backend/internal/ytdlp/url_test.go`:
```go
func TestCanonicalize_channelKinds(t *testing.T) {
	cases := map[string]struct{ url, id string }{
		"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv": {"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv", "UCabcdefghijklmnopqrstuv"},
		"https://www.youtube.com/@SomeHandle":                      {"https://www.youtube.com/@SomeHandle", "@SomeHandle"},
		"https://www.youtube.com/c/SomeName":                       {"https://www.youtube.com/c/SomeName", "SomeName"},
		"https://www.youtube.com/user/LegacyName":                  {"https://www.youtube.com/user/LegacyName", "LegacyName"},
	}
	for in, want := range cases {
		gotURL, gotID, kind, err := Canonicalize(in)
		if err != nil {
			t.Fatalf("%s: unexpected err %v", in, err)
		}
		if kind != "channel" {
			t.Fatalf("%s: kind = %q, want channel", in, kind)
		}
		if gotURL != want.url || gotID != want.id {
			t.Fatalf("%s: got (%q,%q), want (%q,%q)", in, gotURL, gotID, want.url, want.id)
		}
	}
}
```

- [ ] **Step 2: Run — expect FAIL.** `cd backend && go test ./internal/ytdlp/ -run TestCanonicalize_channelKinds -v`.

- [ ] **Step 3: Implement the channel branch in `url.go`.** Inside the `case "youtube.com":` switch, add these cases BEFORE the `default`:
```go
		case strings.HasPrefix(path, "channel/"):
			id = strings.TrimPrefix(path, "channel/")
			return "https://www.youtube.com/channel/" + id, id, "channel", nil
		case strings.HasPrefix(path, "@"):
			return "https://www.youtube.com/" + path, path, "channel", nil
		case strings.HasPrefix(path, "c/"):
			id = strings.TrimPrefix(path, "c/")
			return "https://www.youtube.com/c/" + id, id, "channel", nil
		case strings.HasPrefix(path, "user/"):
			id = strings.TrimPrefix(path, "user/")
			return "https://www.youtube.com/user/" + id, id, "channel", nil
```
These `return` early — mirroring the `playlist` early-return at the bottom — so a channel URL never reaches the `videoIDRe` 11-char check that would reject it.

- [ ] **Step 4: Run — expect PASS.**

- [ ] **Step 5: Failing test — POST /api/downloads rejects a channel URL.** Add to `backend/internal/httpapi/downloads_handlers_test.go` (follow the existing POST test's `newTestServer`/request helper pattern):
```go
func TestDownloadsPost_rejectsChannelURL(t *testing.T) {
	h := newDownloadsTestServer(t) // existing helper; cookie present, fake runner
	rr := postJSON(t, h, "/api/downloads", map[string]string{"url": "https://www.youtube.com/@SomeHandle"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}
```

- [ ] **Step 6: Run — expect FAIL** (channel falls through to Metadata, likely 502).

- [ ] **Step 7: Implement the reject.** In `handleDownloadsPost`'s `switch kind {` block (`downloads_handlers.go`), add:
```go
	case "channel":
		writeJSONError(w, http.StatusBadRequest, "That's a channel link — add it under Channels, not here")
		return
```

- [ ] **Step 8: Run — expect PASS.** `cd backend && go test ./internal/ytdlp/ ./internal/httpapi/ -v`.

- [ ] **Step 9: Commit.**
```bash
git add backend/internal/ytdlp/url.go backend/internal/ytdlp/url_test.go backend/internal/httpapi/downloads_handlers.go backend/internal/httpapi/downloads_handlers_test.go
git commit -m "feat: canonicalize youtube channel urls; reject channel links at /api/downloads"
```

---

## Task 3: yt-dlp wrapper — `ResolveChannel` + `ChannelVideos`

**Files:**
- Create: `backend/internal/ytdlp/channel.go`, `backend/internal/ytdlp/channel_test.go`
- Reference: `backend/internal/ytdlp/meta.go` (exec + JSON-parse pattern), `backend/internal/ytdlp/ytdlp_test.go` (how the fake bin stub is built)

**Interfaces:**
- Produces:
  - `type ChannelEntry struct { ID, Title, URL string; DurationSeconds int; ThumbnailURL, LiveStatus string }`
  - `(*Runner) ResolveChannel(ctx context.Context, channelURL string) (ucid, name string, err error)` — runs `<bin> -J --flat-playlist --playlist-items 0 <channelURL>`; parses `channel_id` (fallback `id`) and `channel`/`title`/`uploader`.
  - `(*Runner) ChannelVideos(ctx context.Context, ucid string, n int) ([]ChannelEntry, error)` — runs `<bin> -J --flat-playlist --playlist-items ":N:1" https://www.youtube.com/channel/{ucid}/videos`; parses the `entries` array.
  - Both go through `r.cookieGate()` then `r.exec(...)`, so both inherit the cookie gate + 20s throttle, and both return `ErrNoCookie` without invoking the binary when no cookie is configured.

- [ ] **Step 1: Failing test — cookie gate on both methods.** Add to `backend/internal/ytdlp/channel_test.go` (reuse the `fakeBinTouching`/marker helper from `ytdlp_test.go`; if it isn't exported, replicate the same pattern):
```go
func TestChannelVideos_noCookie_doesNotCallBinary(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: func() (string, string) { return "", "absent" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	if _, err := r.ChannelVideos(context.Background(), "UCabcdefghijklmnopqrstuv", 50); !errors.Is(err, ErrNoCookie) {
		t.Fatalf("want ErrNoCookie, got %v", err)
	}
	if _, e := os.Stat(called); e == nil {
		t.Fatal("binary must not run without a cookie")
	}
}

func TestResolveChannel_noCookie_doesNotCallBinary(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: func() (string, string) { return "", "absent" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	if _, _, err := r.ResolveChannel(context.Background(), "https://www.youtube.com/@x"); !errors.Is(err, ErrNoCookie) {
		t.Fatalf("want ErrNoCookie, got %v", err)
	}
	if _, e := os.Stat(called); e == nil {
		t.Fatal("binary must not run without a cookie")
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (no such methods). `cd backend && go test ./internal/ytdlp/ -run 'Channel|Resolve' -v`.

- [ ] **Step 3: Implement `channel.go`.**
```go
package ytdlp

import (
	"context"
	"encoding/json"
	"fmt"
)

// ChannelEntry is one video from a flat channel listing. Flat entries are
// metadata-poor by design (no description/published_at/availability): only
// the fields below are reliably present, and ThumbnailURL is a REMOTE url,
// never a local path.
type ChannelEntry struct {
	ID              string
	Title           string
	URL             string
	DurationSeconds int
	ThumbnailURL    string
	LiveStatus      string
}

// flatEntry mirrors one yt-dlp flat-playlist entry. duration is a float
// (yt-dlp emits it as a number, and may omit it entirely in flat mode — in
// which case DurationSeconds is 0 and the caller's `<min` filter fails open).
type flatEntry struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Duration   float64 `json:"duration"`
	LiveStatus string  `json:"live_status"`
	Thumbnails []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

type flatListing struct {
	ID       string      `json:"id"`
	Channel  string      `json:"channel"`
	Title    string      `json:"title"`
	Uploader string      `json:"uploader"`
	Ch       string      `json:"channel_id"`
	Entries  []flatEntry `json:"entries"`
}

// ChannelVideos returns up to n most-recent uploads from a channel's /videos
// tab via a single flat-playlist call. Querying only the /videos tab means
// shorts and livestreams (separate tabs) are excluded by construction. The
// call goes through the cookie gate + throttle like every other Runner call.
func (r *Runner) ChannelVideos(ctx context.Context, ucid string, n int) ([]ChannelEntry, error) {
	cookieText, err := r.cookieGate()
	if err != nil {
		return nil, err
	}
	items := fmt.Sprintf(":%d:1", n)
	url := "https://www.youtube.com/channel/" + ucid + "/videos"
	out, err := r.exec(ctx, cookieText, "-J", "--flat-playlist", "--skip-download", "--playlist-items", items, url)
	if err != nil {
		return nil, err
	}
	var raw flatListing
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("ytdlp: parse channel listing json: %w", err)
	}
	entries := make([]ChannelEntry, 0, len(raw.Entries))
	for _, e := range raw.Entries {
		if e.ID == "" {
			continue
		}
		thumb := ""
		if len(e.Thumbnails) > 0 {
			thumb = e.Thumbnails[len(e.Thumbnails)-1].URL
		}
		entries = append(entries, ChannelEntry{
			ID:              e.ID,
			Title:           e.Title,
			URL:             "https://www.youtube.com/watch?v=" + e.ID,
			DurationSeconds: int(e.Duration),
			ThumbnailURL:    thumb,
			LiveStatus:      e.LiveStatus,
		})
	}
	return entries, nil
}

// ResolveChannel resolves a channel URL (a @handle, /c/, /user/, or /channel/
// URL) to its UCID + display name via a metadata-only flat call
// (--playlist-items 0 fetches no entries). Used at explicit channel-add time.
func (r *Runner) ResolveChannel(ctx context.Context, channelURL string) (ucid, name string, err error) {
	cookieText, gerr := r.cookieGate()
	if gerr != nil {
		return "", "", gerr
	}
	out, xerr := r.exec(ctx, cookieText, "-J", "--flat-playlist", "--skip-download", "--playlist-items", "0", channelURL)
	if xerr != nil {
		return "", "", xerr
	}
	var raw flatListing
	if err := json.Unmarshal(out, &raw); err != nil {
		return "", "", fmt.Errorf("ytdlp: parse channel json: %w", err)
	}
	ucid = raw.Ch
	if ucid == "" {
		ucid = raw.ID
	}
	name = raw.Channel
	if name == "" {
		name = raw.Uploader
	}
	if name == "" {
		name = raw.Title
	}
	if ucid == "" {
		return "", "", fmt.Errorf("ytdlp: could not resolve channel id from %q", channelURL)
	}
	return ucid, name, nil
}
```

- [ ] **Step 4: Run — expect PASS** (cookie-gate tests). 

- [ ] **Step 5: Failing test — parse a flat listing + resolve.** Add tests that point the Runner at a stub bin printing canned JSON. Add a helper stub (bash) via the same mechanism `ytdlp_test.go` uses to make a fake bin that echoes a fixed JSON file; assert parse:
```go
func TestChannelVideos_parsesEntries(t *testing.T) {
	const listing = `{"id":"UCabc","channel":"Chan","entries":[
	  {"id":"vid00000001","title":"A","duration":600,"live_status":"not_live","thumbnails":[{"url":"https://t/a.jpg"}]},
	  {"id":"vid00000002","title":"B","duration":120,"live_status":"is_upcoming"}
	]}`
	r := New(RunnerConfig{
		Bin:            fakeBinPrinting(t, listing), // helper: stub that prints its canned stdout, exit 0
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	got, err := r.ChannelVideos(context.Background(), "UCabc", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "vid00000001" || got[0].DurationSeconds != 600 || got[0].ThumbnailURL != "https://t/a.jpg" {
		t.Fatalf("entry 0 = %+v", got[0])
	}
	if got[1].LiveStatus != "is_upcoming" {
		t.Fatalf("entry 1 live_status = %q", got[1].LiveStatus)
	}
	if got[0].URL != "https://www.youtube.com/watch?v=vid00000001" {
		t.Fatalf("entry 0 url = %q", got[0].URL)
	}
}

func TestResolveChannel_parsesUcidAndName(t *testing.T) {
	const j = `{"id":"UCxyz","channel_id":"UCxyz","channel":"My Channel","entries":[]}`
	r := New(RunnerConfig{
		Bin:            fakeBinPrinting(t, j),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	ucid, name, err := r.ResolveChannel(context.Background(), "https://www.youtube.com/@x")
	if err != nil {
		t.Fatal(err)
	}
	if ucid != "UCxyz" || name != "My Channel" {
		t.Fatalf("got (%q,%q), want (UCxyz, My Channel)", ucid, name)
	}
}
```
Implement `fakeBinPrinting(t, stdout)` in the test file if no equivalent helper exists: write a small executable shell script to a temp dir that `cat`s a canned file (or `printf`s the literal) to stdout and exits 0, and return its path. (Model it on the existing `fake-ytdlp` stub in `internal/ytdlp/testdata/`.)

- [ ] **Step 6: Run — expect PASS.** `cd backend && go test ./internal/ytdlp/ -v`.

- [ ] **Step 7: Commit.**
```bash
git add backend/internal/ytdlp/channel.go backend/internal/ytdlp/channel_test.go
git commit -m "feat: yt-dlp ChannelVideos (flat /videos listing) + ResolveChannel through the cookie/throttle gate"
```

---

## Task 4: `channels` store — track + subscriptions + claim-due

**Files:**
- Create: `backend/internal/channels/store.go`, `backend/internal/channels/store_test.go`

**Interfaces:**
- Consumes: a migrated `*sql.DB`.
- Produces:
```go
type Channel struct { ID, Handle, Name, AvatarPath, AddedAt string }
type Subscription struct {
	ChannelID      string
	Autodownload   bool
	FormatOverride string
	BaselinedAt    string // "" when NULL (not yet baselined)
	LastScannedAt  string
	NextScanAt     string
	CreatedAt      string
}
type ListItem struct {
	Channel
	Subscribed      bool
	Autodownload    bool
	FormatOverride  string
	PendingCount    int
	DownloadedCount int
}
func New(db *sql.DB) *Store
func (s *Store) Upsert(c Channel) error                 // track (refreshes handle/name only)
func (s *Store) Get(id string) (*Channel, error)        // nil if absent
func (s *Store) List(filter string) ([]ListItem, error) // filter: all|subscribed|tracked
func (s *Store) Subscribe(channelID, nextScanAt string) error       // idempotent
func (s *Store) Unsubscribe(channelID string) (bool, error)
func (s *Store) UpdateConfig(channelID string, autodownload bool, formatOverride string) error
func (s *Store) ClaimDue(now string) (*Subscription, error) // oldest next_scan_at <= now, nil if none
func (s *Store) MarkScanned(channelID string, baseline bool, lastScannedAt, nextScanAt string) error
func (s *Store) Backoff(channelID, nextScanAt string) error
```

- [ ] **Step 1: Failing test — track, subscribe, config, claim-due ordering.** `backend/internal/channels/store_test.go`:
```go
func TestChannels_trackSubscribeClaim(t *testing.T) {
	st := newTestStore(t) // opens a migrated temp DB and returns *Store (helper below)

	if err := st.Upsert(Channel{ID: "UC1", Handle: "@one", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Upsert(Channel{ID: "UC2", Name: "Two"}); err != nil {
		t.Fatal(err)
	}
	// Tracked only → ClaimDue returns nothing.
	if sub, err := st.ClaimDue("2999-01-01 00:00:00"); err != nil || sub != nil {
		t.Fatalf("no subscriptions yet: sub=%v err=%v", sub, err)
	}
	// Subscribe both, UC1 due earlier.
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC2", "2000-06-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	sub, err := st.ClaimDue("2999-01-01 00:00:00")
	if err != nil || sub == nil || sub.ChannelID != "UC1" {
		t.Fatalf("want UC1 first, got %v err=%v", sub, err)
	}
	if sub.BaselinedAt != "" {
		t.Fatalf("new subscription must have NULL baselined_at, got %q", sub.BaselinedAt)
	}
	// Config update reflected in List.
	if err := st.UpdateConfig("UC1", true, "bestvideo+bestaudio"); err != nil {
		t.Fatal(err)
	}
	items, err := st.List("subscribed")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("subscribed count = %d, want 2", len(items))
	}
}

func TestChannels_unsubscribeKeepsChannel(t *testing.T) {
	st := newTestStore(t)
	st.Upsert(Channel{ID: "UC1", Name: "One"})
	st.Subscribe("UC1", "2000-01-01 00:00:00")
	ok, err := st.Unsubscribe("UC1")
	if err != nil || !ok {
		t.Fatalf("unsubscribe: ok=%v err=%v", ok, err)
	}
	if c, err := st.Get("UC1"); err != nil || c == nil {
		t.Fatal("channel must stay tracked after unsubscribe")
	}
	items, _ := st.List("tracked")
	if len(items) != 1 {
		t.Fatalf("tracked count = %d, want 1", len(items))
	}
}
```
Add the `newTestStore(t)` helper in the test file (open a temp DB via `store.Open` + `store.Migrate`, return `New(db)`), mirroring how `internal/jobs/store_test.go` opens its DB.

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Implement `channels/store.go`.** Key queries:
  - `Upsert`: `INSERT INTO channels (id, handle, name) VALUES (?,?,?) ON CONFLICT(id) DO UPDATE SET handle=excluded.handle, name=excluded.name` (leave `avatar_path`/`added_at` untouched).
  - `Get`: `SELECT id, handle, name, avatar_path, added_at FROM channels WHERE id=?` → `nil, nil` on `sql.ErrNoRows`.
  - `List(filter)`: LEFT JOIN subscriptions; compute `subscribed = subscriptions.channel_id IS NOT NULL`; correlated subqueries for counts:
    ```sql
    SELECT c.id, c.handle, c.name, c.avatar_path, c.added_at,
           s.channel_id IS NOT NULL AS subscribed,
           COALESCE(s.autodownload,0), COALESCE(s.format_override,''),
           (SELECT count(*) FROM channel_videos cv WHERE cv.channel_id=c.id AND cv.state='pending'),
           (SELECT count(*) FROM videos v WHERE v.channel_id=c.id AND v.status='downloaded')
    FROM channels c LEFT JOIN subscriptions s ON s.channel_id=c.id
    ```
    Then filter in SQL: `all` → no extra clause; `subscribed` → `WHERE s.channel_id IS NOT NULL`; `tracked` → `WHERE s.channel_id IS NULL`. `ORDER BY c.name COLLATE NOCASE, c.id`.
  - `Subscribe`: `INSERT INTO subscriptions (channel_id, next_scan_at) VALUES (?,?) ON CONFLICT(channel_id) DO NOTHING` (idempotent; keeps an existing subscription's config/baseline).
  - `Unsubscribe`: `DELETE FROM subscriptions WHERE channel_id=?`; return `RowsAffected()>0`.
  - `UpdateConfig`: `UPDATE subscriptions SET autodownload=?, format_override=? WHERE channel_id=?` (booleans stored 0/1).
  - `ClaimDue(now)`: `SELECT channel_id, autodownload, format_override, baselined_at, last_scanned_at, next_scan_at, created_at FROM subscriptions WHERE next_scan_at <= ? ORDER BY next_scan_at ASC LIMIT 1`; map NULL `baselined_at`/`last_scanned_at` to "". `nil,nil` on no row. (Single-goroutine scheduler → no atomic-claim UPDATE needed.)
  - `MarkScanned`: when `baseline` is true, also set `baselined_at`: `UPDATE subscriptions SET last_scanned_at=?, next_scan_at=?, baselined_at=COALESCE(baselined_at, ?) WHERE channel_id=?` (pass `lastScannedAt` for the baseline arg so a first scan stamps it; a later scan's COALESCE keeps the original). When `baseline` is false, omit the baselined_at set.
  - `Backoff`: `UPDATE subscriptions SET next_scan_at=? WHERE channel_id=?` (leaves baseline untouched).

- [ ] **Step 4: Run — expect PASS.** `cd backend && go test ./internal/channels/ -v`.

- [ ] **Step 5: Commit.**
```bash
git add backend/internal/channels
git commit -m "feat: channels + subscriptions store (track/subscribe/config/claim-due/mark-scanned)"
```

---

## Task 5: `channel_videos` ledger store

**Files:**
- Create: `backend/internal/channelvideos/store.go`, `backend/internal/channelvideos/store_test.go`

**Interfaces:**
- Produces:
```go
type Entry struct {
	VideoID, ChannelID, Title, URL, ThumbnailURL, State, DiscoveredAt, DecidedAt string
	DurationSeconds int
}
func New(db *sql.DB) *Store
func (s *Store) Exists(videoID string) (bool, error)          // ledger membership (dedup)
func (s *Store) Insert(e Entry) error                         // e.State must be set
func (s *Store) SetState(videoID, state string) error         // stamps decided_at
func (s *Store) Get(videoID string) (*Entry, error)           // nil if absent
func (s *Store) ListPending() ([]Entry, error)                // state='pending', newest first
```

- [ ] **Step 1: Failing test — insert, dedup, list-pending, set-state.** `backend/internal/channelvideos/store_test.go`:
```go
func TestLedger_insertPendingAndDecide(t *testing.T) {
	st := newTestStore(t) // migrated temp DB; a channels row UC1 must exist first (FK)
	seedChannel(t, st, "UC1")

	if err := st.Insert(Entry{VideoID: "v1", ChannelID: "UC1", Title: "A", DurationSeconds: 600, State: "pending", ThumbnailURL: "https://t/a.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Insert(Entry{VideoID: "v2", ChannelID: "UC1", Title: "B", State: "seen"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Exists("v1"); !ok {
		t.Fatal("v1 must exist")
	}
	if ok, _ := st.Exists("nope"); ok {
		t.Fatal("nope must not exist")
	}
	pend, err := st.ListPending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 1 || pend[0].VideoID != "v1" || pend[0].ThumbnailURL != "https://t/a.jpg" {
		t.Fatalf("pending = %+v", pend)
	}
	if err := st.SetState("v1", "ignored"); err != nil {
		t.Fatal(err)
	}
	pend, _ = st.ListPending()
	if len(pend) != 0 {
		t.Fatalf("after ignore, pending = %d, want 0", len(pend))
	}
}
```
`seedChannel` inserts a `channels` row directly (`INSERT INTO channels (id,name) VALUES (?, 'x')`) so the FK is satisfied.

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Implement `channelvideos/store.go`.**
  - `Exists`: `SELECT 1 FROM channel_videos WHERE video_id=?` → bool.
  - `Insert`: `INSERT INTO channel_videos (video_id, channel_id, title, duration_seconds, url, thumbnail_url, state) VALUES (?,?,?,?,?,?,?)` (use `sql.NullInt64` for duration when 0-unknown, or store 0 — store the int directly; 0 means "unknown" and the filter fails open).
  - `SetState`: `UPDATE channel_videos SET state=?, decided_at=datetime('now') WHERE video_id=?`.
  - `Get`: SELECT all columns; NULL `duration_seconds`/`decided_at` → 0/"".
  - `ListPending`: `SELECT ... WHERE state='pending' ORDER BY discovered_at DESC, video_id DESC`.

- [ ] **Step 4: Run — expect PASS.** `cd backend && go test ./internal/channelvideos/ -v`.

- [ ] **Step 5: Commit.**
```bash
git add backend/internal/channelvideos
git commit -m "feat: channel_videos scan ledger store (exists/insert/set-state/list-pending)"
```

---

## Task 6: Per-channel format override plumbing

**Files:**
- Modify: `backend/internal/videos/store.go` (carry `requested_format` on Upsert; add `SetRequestedFormat`)
- Modify: `backend/internal/download/worker.go` (prefer `video.RequestedFormat`)
- Test: `backend/internal/videos/store_test.go` (extend), `backend/internal/download/worker_test.go` (extend)

**Interfaces:**
- Produces: `videos.Video.RequestedFormat string`; `videos.Store.SetRequestedFormat(id, format string) error`. Worker builds `DownloadReq` preferring the video's requested format over the global settings preset.

- [ ] **Step 1: Failing test — Upsert carries requested_format; setter updates it.** Add to `backend/internal/videos/store_test.go`:
```go
func TestUpsert_requestedFormat(t *testing.T) {
	st := newTestStore(t)
	if err := st.Upsert(Video{ID: "v1", URL: "u", RequestedFormat: "bestvideo+bestaudio"}); err != nil {
		t.Fatal(err)
	}
	v, _ := st.Get("v1")
	if v.RequestedFormat != "bestvideo+bestaudio" {
		t.Fatalf("requested_format = %q", v.RequestedFormat)
	}
	if err := st.SetRequestedFormat("v1", "worst"); err != nil {
		t.Fatal(err)
	}
	v, _ = st.Get("v1")
	if v.RequestedFormat != "worst" {
		t.Fatalf("after set, requested_format = %q", v.RequestedFormat)
	}
}
```

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Implement in `videos/store.go`.**
  - Add `RequestedFormat string` to the `Video` struct.
  - In `Upsert`: add `requested_format` to the INSERT column list + values, and to the `ON CONFLICT DO UPDATE SET` list (`requested_format = excluded.requested_format`), and add `v.RequestedFormat` to the args (matching position).
  - Add `requested_format` to `videoColumns` (in the correct order, after `format_used`) and to `scanVideo`'s Scan targets (`&v.RequestedFormat`) so Get/List populate it. (Keep column/scan order aligned across `videoColumns`, `scanVideo`.)
  - Add:
    ```go
    // SetRequestedFormat overrides the yt-dlp format string used for this
    // video's next download (empty = use the global preset). Set by the scan
    // scheduler from a channel's format_override before enqueueing.
    func (s *Store) SetRequestedFormat(id, format string) error {
    	_, err := s.db.ExecContext(context.Background(),
    		`UPDATE videos SET requested_format = ? WHERE id = ?`, format, id)
    	if err != nil {
    		return fmt.Errorf("set requested_format %s: %w", id, err)
    	}
    	return nil
    }
    ```

- [ ] **Step 4: Run — expect PASS.** `cd backend && go test ./internal/videos/ -v`.

- [ ] **Step 5: Failing test — worker prefers requested_format.** Add to `backend/internal/download/worker_test.go` (follow the existing worker test's fake-Runner setup that captures the `DownloadReq`):
```go
func TestWorker_prefersRequestedFormat(t *testing.T) {
	// Seed a video with a per-channel override; the fake Runner records the
	// DownloadReq it receives.
	h := newWorkerHarness(t) // existing helper
	h.videos.Upsert(videos.Video{ID: "v1", URL: "https://www.youtube.com/watch?v=v1", RequestedFormat: "bestvideo+bestaudio"})
	h.videos.SetStatus("v1", "queued", "")
	h.jobs.Enqueue("v1", 0)

	req := h.runUntilDownloadReq(t) // helper that runs one claim/process and returns the captured req
	if req.Format != "custom" || req.CustomFormat != "bestvideo+bestaudio" {
		t.Fatalf("req = {Format:%q Custom:%q}, want custom/bestvideo+bestaudio", req.Format, req.CustomFormat)
	}
}
```
(If the existing worker tests don't already expose a "capture the DownloadReq" seam, add a minimal one: the fake Runner in the test package stores the last `req ytdlp.DownloadReq` it was called with; assert on it after one process cycle. Reuse the existing fake if it already captures — check `worker_test.go` first.)

- [ ] **Step 6: Run — expect FAIL** (worker still uses settings format). 

- [ ] **Step 7: Implement in `worker.go`.** In `process`, replace the `req := ytdlp.DownloadReq{...}` construction with:
```go
	format := set.FormatPreset
	custom := set.FormatCustom
	if video.RequestedFormat != "" {
		// A per-channel format override is a free-form yt-dlp format string;
		// route it through the "custom" preset slot (format.Resolve("custom",x)==x).
		format = "custom"
		custom = video.RequestedFormat
	}
	req := ytdlp.DownloadReq{
		URL:          video.URL,
		VideoID:      video.ID,
		Format:       format,
		CustomFormat: custom,
		LimitRate:    set.LimitRate,
	}
```

- [ ] **Step 8: Run — expect PASS.** `cd backend && go test ./internal/download/ -race -v`.

- [ ] **Step 9: Commit.**
```bash
git add backend/internal/videos backend/internal/download
git commit -m "feat: per-video requested_format override honored by the download worker"
```

---

## Task 7: Scan scheduler goroutine

**Files:**
- Create: `backend/internal/scan/scheduler.go`, `backend/internal/scan/scheduler_test.go`
- Reference: `backend/internal/download/worker.go` (loop/recover/ctx-cancel/pause structure)

**Interfaces:**
- Consumes: `channels.Store`, `channelvideos.Store`, `jobs.Store`, `videos.Store`, `settings.Store`, and a `ChannelLister` (the Runner).
- Produces:
```go
type ChannelLister interface {
	ChannelVideos(ctx context.Context, ucid string, n int) ([]ytdlp.ChannelEntry, error)
}
type Deps struct {
	Channels     *channels.Store
	Ledger       *channelvideos.Store
	Videos       *videos.Store
	Jobs         *jobs.Store
	Settings     *settings.Store
	Lister       ChannelLister
	CookieStatus func(ctx context.Context) string // settings.CookieStatus
	Now          func() time.Time                 // injectable clock (defaults to time.Now)
	PollInterval time.Duration                    // idle re-check (default 30s)
	Logger       *slog.Logger
	// test seams:
	listSize     int                              // default 50
}
func New(d Deps) *Scheduler
func (s *Scheduler) Run(ctx context.Context)  // blocks until ctx cancelled
func (s *Scheduler) scanOnce(ctx context.Context, sub *channels.Subscription) error // exported-for-test via a thin wrapper or lowercase + same-package test
```

**Constants (named in scheduler.go):** `scanInterval = 24 * time.Hour`, `scanJitter = 3 * time.Hour`, `betweenChannels = 60 * time.Second`, `defaultListSize = 50`, `scanBackoff = time.Hour`, `minPendingDurationFallback`… (min duration comes from settings, not a const).

- [ ] **Step 1: Failing test — first-run baseline records `seen` and queues nothing.** `backend/internal/scan/scheduler_test.go`:
```go
func TestScan_firstRunBaseline_queuesNothing(t *testing.T) {
	h := newScanHarness(t) // migrated DB + stores + a fake Lister
	h.trackAndSubscribe("UC1", false /*autodownload*/, "" /*format*/)
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "v1", Title: "A", DurationSeconds: 600, LiveStatus: "not_live"},
		{ID: "v2", Title: "B", DurationSeconds: 600, LiveStatus: "not_live"},
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	// Baseline: both recorded as 'seen', nothing pending, no jobs enqueued.
	if p, _ := h.ledger.ListPending(); len(p) != 0 {
		t.Fatalf("baseline pending = %d, want 0", len(p))
	}
	if jobsList, _ := h.jobs.List(); len(jobsList) != 0 {
		t.Fatalf("baseline jobs = %d, want 0", len(jobsList))
	}
	ok1, _ := h.ledger.Exists("v1")
	ok2, _ := h.ledger.Exists("v2")
	if !ok1 || !ok2 {
		t.Fatal("baseline must record all current ids as seen")
	}
	// baselined_at now set.
	sub2, _ := h.channels.ClaimDue("2999-01-01 00:00:00")
	if sub2 != nil && sub2.BaselinedAt == "" {
		t.Fatal("baselined_at must be set after first scan")
	}
}
```

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Failing test — subsequent new id → pending (non-autodownload) / enqueued (autodownload); filters + dedup.** Add:
```go
func TestScan_subsequentNewVideo_pendingVsAutodownload(t *testing.T) {
	// Non-autodownload: new id after baseline → pending.
	h := newScanHarness(t)
	h.trackAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", []string{"old1"}) // seed ledger 'seen' + baselined_at set
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "old1", DurationSeconds: 600, LiveStatus: "not_live"},        // dedup: skip
		{ID: "newp", DurationSeconds: 600, LiveStatus: "not_live"},        // NEW → pending
		{ID: "short", DurationSeconds: 60, LiveStatus: "not_live"},        // <180s → seen
		{ID: "up", DurationSeconds: 600, LiveStatus: "is_upcoming"},       // upcoming → seen
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	p, _ := h.ledger.ListPending()
	if len(p) != 1 || p[0].VideoID != "newp" {
		t.Fatalf("pending = %+v, want [newp]", p)
	}
	if jobsList, _ := h.jobs.List(); len(jobsList) != 0 {
		t.Fatalf("non-autodownload must not enqueue; got %d jobs", len(jobsList))
	}
}

func TestScan_autodownloadEnqueuesWithFormatOverride(t *testing.T) {
	h := newScanHarness(t)
	h.trackAndSubscribe("UC1", true /*autodownload*/, "bestvideo+bestaudio")
	h.markBaselined("UC1", nil)
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "newv", Title: "N", URL: "https://www.youtube.com/watch?v=newv", DurationSeconds: 600, LiveStatus: "not_live"},
	})
	sub, _ := h.channels.ClaimDue(h.nowStr())
	if err := h.sched.scanOnce(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	// videos row upserted + status queued + requested_format set + job enqueued at priority 0.
	v, _ := h.videos.Get("newv")
	if v == nil || v.Status != "queued" || v.RequestedFormat != "bestvideo+bestaudio" {
		t.Fatalf("video = %+v", v)
	}
	jobsList, _ := h.jobs.List()
	if len(jobsList) != 1 || jobsList[0].Priority != 0 {
		t.Fatalf("jobs = %+v, want one at priority 0", jobsList)
	}
	// ledger marked queued (not pending).
	if p, _ := h.ledger.ListPending(); len(p) != 0 {
		t.Fatalf("autodownloaded id must not be pending; got %+v", p)
	}
}
```

- [ ] **Step 4: Run — expect FAIL.**

- [ ] **Step 5: Implement `scan/scheduler.go`.** Core structure (mirrors the worker):
```go
package scan

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/trick77/vark/internal/channels"
	"github.com/trick77/vark/internal/channelvideos"
	"github.com/trick77/vark/internal/jobs"
	"github.com/trick77/vark/internal/settings"
	"github.com/trick77/vark/internal/videos"
	"github.com/trick77/vark/internal/ytdlp"
)

const (
	scanInterval    = 24 * time.Hour
	scanJitter      = 3 * time.Hour
	betweenChannels = 60 * time.Second
	defaultListSize = 50
	scanBackoff     = time.Hour
	autoPriority    = 0  // below manual (10), matching Phase 1
	sqlTimeLayout   = "2006-01-02 15:04:05" // SQLite datetime('now') text form (UTC)
)

type ChannelLister interface {
	ChannelVideos(ctx context.Context, ucid string, n int) ([]ytdlp.ChannelEntry, error)
}

type Scheduler struct {
	d            Deps
	lastScanTime time.Time // in-memory; enforces betweenChannels spacing
	rand         func() float64
}

func New(d Deps) *Scheduler {
	if d.Now == nil { d.Now = time.Now }
	if d.PollInterval <= 0 { d.PollInterval = 30 * time.Second }
	if d.Logger == nil { d.Logger = slog.Default() }
	if d.listSize <= 0 { d.listSize = defaultListSize }
	return &Scheduler{d: d, rand: pseudoRand()}
}

func (s *Scheduler) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil { return }
		// Cookie gate mirror: no valid cookie → don't scan (don't hammer).
		if s.d.CookieStatus(ctx) != "valid" {
			if !s.sleep(ctx, s.d.PollInterval) { return }
			continue
		}
		nowStr := s.d.Now().UTC().Format(sqlTimeLayout)
		sub, err := s.d.Channels.ClaimDue(nowStr)
		if err != nil {
			s.d.Logger.Error("scan: claim due failed", "err", err)
			if !s.sleep(ctx, s.d.PollInterval) { return }
			continue
		}
		if sub == nil {
			if !s.sleep(ctx, s.d.PollInterval) { return }
			continue
		}
		// ≥60s between channel scans.
		if wait := betweenChannels - s.d.Now().Sub(s.lastScanTime); wait > 0 {
			if !s.sleep(ctx, wait) { return }
		}
		s.lastScanTime = s.d.Now()
		s.safely(sub.ChannelID, func() {
			if err := s.scanOnce(ctx, sub); err != nil {
				s.d.Logger.Warn("scan failed; backing off", "channel", sub.ChannelID, "err", err)
				next := s.d.Now().Add(scanBackoff).UTC().Format(sqlTimeLayout)
				if berr := s.d.Channels.Backoff(sub.ChannelID, next); berr != nil {
					s.d.Logger.Error("scan: backoff failed", "channel", sub.ChannelID, "err", berr)
				}
			}
		})
	}
}

func (s *Scheduler) scanOnce(ctx context.Context, sub *channels.Subscription) error {
	set, err := s.d.Settings.Get(ctx)
	if err != nil {
		return fmt.Errorf("scan: settings: %w", err)
	}
	entries, err := s.d.Lister.ChannelVideos(ctx, sub.ChannelID, s.d.listSize)
	if err != nil {
		return fmt.Errorf("scan: list %s: %w", sub.ChannelID, err)
	}
	baseline := sub.BaselinedAt == ""
	for _, e := range entries {
		exists, err := s.d.Ledger.Exists(e.ID)
		if err != nil { return err }
		if exists { continue } // dedup vs ledger
		if v, err := s.d.Videos.Get(e.ID); err == nil && v != nil {
			continue // dedup vs videos (manually added / already downloaded)
		}
		entry := channelvideos.Entry{
			VideoID: e.ID, ChannelID: sub.ChannelID, Title: e.Title,
			DurationSeconds: e.DurationSeconds, URL: e.URL, ThumbnailURL: e.ThumbnailURL,
		}
		switch {
		case baseline:
			entry.State = "seen"
		case !passesFilters(e, set.MinVideoDurationSeconds):
			entry.State = "seen"
		case sub.Autodownload:
			entry.State = "queued"
		default:
			entry.State = "pending"
		}
		if err := s.d.Ledger.Insert(entry); err != nil { return err }
		if entry.State == "queued" {
			if err := s.enqueueAuto(e, sub); err != nil { return err }
		}
	}
	next := s.d.Now().Add(s.jitteredInterval()).UTC().Format(sqlTimeLayout)
	lastScanned := s.d.Now().UTC().Format(sqlTimeLayout)
	return s.d.Channels.MarkScanned(sub.ChannelID, baseline, lastScanned, next)
}

// passesFilters drops sub-min-duration and upcoming/live entries. Shorts and
// finished livestreams are already excluded by querying only the /videos tab.
// A zero duration (yt-dlp omitted it in flat mode) FAILS OPEN — the video is
// kept, since we'd rather offer a maybe-short video than silently drop uploads.
func passesFilters(e ytdlp.ChannelEntry, minDuration int) bool {
	if e.LiveStatus == "is_upcoming" || e.LiveStatus == "is_live" {
		return false
	}
	if e.DurationSeconds > 0 && e.DurationSeconds < minDuration {
		return false
	}
	return true
}

func (s *Scheduler) enqueueAuto(e ytdlp.ChannelEntry, sub *channels.Subscription) error {
	if err := s.d.Videos.Upsert(videos.Video{
		ID: e.ID, URL: e.URL, Title: e.Title, ChannelID: sub.ChannelID,
		DurationSeconds: int64(e.DurationSeconds), RequestedFormat: sub.FormatOverride,
		// NOTE: flat listings are metadata-poor — published_at/description/
		// availability are intentionally left sparse here (no per-video -J call,
		// to respect the throttle budget). thumbnail_path stays empty (local
		// path); the remote thumbnail lives on the ledger row.
	}); err != nil {
		return err
	}
	if err := s.d.Videos.SetStatus(e.ID, "queued", ""); err != nil {
		return err
	}
	if _, err := s.d.Jobs.Enqueue(e.ID, autoPriority); err != nil {
		return err
	}
	return nil
}
```
Add `sleep`, `safely` (copy the worker's `recover()` helper), `jitteredInterval` (`scanInterval + rand*scanJitter*2 - scanJitter`, clamped ≥ 1h), and `pseudoRand`. Keep `scanOnce` same-package-testable (lowercase but tested from `package scan`).

- [ ] **Step 6: Run — expect PASS** (both new-video tests + baseline test). `cd backend && go test ./internal/scan/ -race -v`.

- [ ] **Step 7: Failing test — no cookie means no scan.** Add:
```go
func TestScan_noCookie_skipsScan(t *testing.T) {
	h := newScanHarness(t)
	h.cookieStatus = "absent" // harness wires CookieStatus to return this
	h.trackAndSubscribe("UC1", false, "")
	h.lister.set("UC1", []ytdlp.ChannelEntry{{ID: "v1", DurationSeconds: 600}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sched.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	// Lister was never called (no cookie): nothing recorded.
	if ok, _ := h.ledger.Exists("v1"); ok {
		t.Fatal("must not scan without a valid cookie")
	}
	if h.lister.calls != 0 {
		t.Fatalf("lister called %d times without cookie", h.lister.calls)
	}
}
```

- [ ] **Step 8: Run — expect PASS.** (Implementation already gates on `CookieStatus`.)

- [ ] **Step 9: Commit.**
```bash
git add backend/internal/scan
git commit -m "feat: channel scan scheduler (baseline, filters, dedup, pending/autodownload, cookie-gated, 60s spacing)"
```

---

## Task 8: Channels HTTP API + auto-track hook

**Files:**
- Create: `backend/internal/httpapi/channels_handlers.go`, `backend/internal/httpapi/channels_handlers_test.go`
- Modify: `backend/internal/httpapi/server.go` (Deps + routes), `backend/internal/httpapi/downloads_handlers.go` (auto-track)

**Interfaces:**
- Consumes: `channels.Store`, and a `ChannelResolver` interface `{ ResolveChannel(ctx, url) (ucid, name string, err error) }` (the Runner satisfies it).
- Produces routes: `POST /api/channels` `{url, subscribe?}`, `GET /api/channels?filter=`, `PUT /api/channels/{id}` `{autodownload?, format_override?}`, `POST /api/channels/{id}/subscribe`, `POST /api/channels/{id}/unsubscribe`. (`DELETE` is Task 10.) New `Deps` fields: `Channels *channels.Store`, `ChannelResolver ChannelResolver`.

- [ ] **Step 1: Failing test — POST tracks (and optionally subscribes); no cookie → 409.** `channels_handlers_test.go`:
```go
func TestChannelsPost_tracksAndSubscribes(t *testing.T) {
	h := newChannelsTestServer(t, testResolver{ucid: "UCxyz", name: "My Channel"}) // cookie present
	rr := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@x", "subscribe": true})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	// It is now tracked AND subscribed.
	list := getJSON(t, h, "/api/channels?filter=subscribed")
	if !strings.Contains(list, "UCxyz") {
		t.Fatalf("subscribed list missing channel: %s", list)
	}
}

func TestChannelsPost_noCookie_409(t *testing.T) {
	h := newChannelsTestServer(t, testResolver{err: ytdlp.ErrNoCookie})
	rr := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@x"})
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
}
```

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Implement `channels_handlers.go`.**
```go
type ChannelResolver interface {
	ResolveChannel(ctx context.Context, url string) (ucid, name string, err error)
}

type channelsPostRequest struct {
	URL       string `json:"url"`
	Subscribe bool   `json:"subscribe"`
}

func (s *server) handleChannelsPost(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil || s.channelResolver == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	var req channelsPostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.URL) == "" {
		writeJSONError(w, http.StatusBadRequest, "url is required")
		return
	}
	channelURL, _, kind, err := ytdlp.Canonicalize(req.URL)
	if err != nil || kind != "channel" {
		writeJSONError(w, http.StatusBadRequest, "Paste a channel link (a /channel/, /@handle, /c/, or /user/ URL)")
		return
	}
	ucid, name, err := s.channelResolver.ResolveChannel(r.Context(), channelURL)
	if err != nil {
		if errors.Is(err, ytdlp.ErrNoCookie) {
			writeJSONError(w, http.StatusConflict, "cookie required")
			return
		}
		writeJSONError(w, http.StatusBadGateway, "resolve channel failed: "+err.Error())
		return
	}
	handle := ""
	if strings.HasPrefix(req.URL, "https://www.youtube.com/@") || strings.Contains(req.URL, "/@") {
		// best-effort: keep the @handle if the user pasted one
		if i := strings.Index(req.URL, "/@"); i >= 0 {
			handle = "@" + strings.TrimPrefix(req.URL[i+2:], "")
		}
	}
	if err := s.channels.Upsert(channels.Channel{ID: ucid, Name: name, Handle: handle}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "track channel failed")
		return
	}
	if req.Subscribe {
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		if err := s.channels.Subscribe(ucid, now); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "subscribe failed")
			return
		}
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{"id": ucid, "name": name, "subscribed": req.Subscribe})
}
```
Also implement `handleChannelsList` (calls `s.channels.List(filter)` with `filter` from `r.URL.Query().Get("filter")`, default "all"; serialize a JSON array of `{id,handle,name,subscribed,autodownload,format_override,pending_count,downloaded_count}`), `handleChannelsPut` (decode `{autodownload *bool, format_override *string}`, `UpdateConfig`; 400 if not subscribed — detect via a `List`/`Get`+subscription check or have `UpdateConfig` return rows-affected), `handleChannelsSubscribe` (`Subscribe(id, now)`), `handleChannelsUnsubscribe` (`Unsubscribe(id)`).

- [ ] **Step 4: Add Deps + server fields + routes.** In `server.go`:
  - `Deps`: add `Channels *channels.Store` and `ChannelResolver ChannelResolver`.
  - `server` struct: add `channels *channels.Store` and `channelResolver ChannelResolver`; populate in `New`.
  - Register:
    ```go
    mux.Handle("POST /api/channels", s.requireAuth(http.HandlerFunc(s.handleChannelsPost)))
    mux.Handle("GET /api/channels", s.requireAuth(http.HandlerFunc(s.handleChannelsList)))
    mux.Handle("PUT /api/channels/{id}", s.requireAuth(http.HandlerFunc(s.handleChannelsPut)))
    mux.Handle("POST /api/channels/{id}/subscribe", s.requireAuth(http.HandlerFunc(s.handleChannelsSubscribe)))
    mux.Handle("POST /api/channels/{id}/unsubscribe", s.requireAuth(http.HandlerFunc(s.handleChannelsUnsubscribe)))
    ```

- [ ] **Step 5: Run — expect PASS.** `cd backend && go test ./internal/httpapi/ -run Channels -v`.

- [ ] **Step 6: Failing test — video-add auto-tracks its channel.** Add to `channels_handlers_test.go` (or downloads test) using a fake `DownloadsRunner` whose `Metadata` returns a `ChannelID`/`Channel`, and a real `channels.Store`:
```go
func TestDownloadsPost_autoTracksChannel(t *testing.T) {
	h := newDownloadsWithChannels(t) // downloads deps + a real channels store; fake runner returns ChannelID "UCauto", Channel "Auto"
	rr := postJSON(t, h, "/api/downloads", map[string]string{"url": "https://youtu.be/vid00000001"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	list := getJSON(t, h, "/api/channels?filter=tracked")
	if !strings.Contains(list, "UCauto") {
		t.Fatalf("channel not auto-tracked: %s", list)
	}
}
```

- [ ] **Step 7: Run — expect FAIL.**

- [ ] **Step 8: Implement the auto-track hook.** In `handleDownloadsPost` (`downloads_handlers.go`), after the successful `s.videos.Upsert(...)` and before/after enqueue, add (nil-safe, non-fatal — a tracking failure must not fail the download):
```go
	if s.channels != nil && meta.ChannelID != "" {
		if err := s.channels.Upsert(channels.Channel{ID: meta.ChannelID, Name: meta.Channel}); err != nil {
			// Non-fatal: the video is still queued; just log.
			slog.Warn("auto-track channel failed", "channel_id", meta.ChannelID, "err", err)
		}
	}
```
(Add the `channels` and `log/slog` imports.)

- [ ] **Step 9: Run — expect PASS.** `cd backend && go test ./internal/httpapi/ -v`.

- [ ] **Step 10: Commit.**
```bash
git add backend/internal/httpapi/channels_handlers.go backend/internal/httpapi/channels_handlers_test.go backend/internal/httpapi/server.go backend/internal/httpapi/downloads_handlers.go
git commit -m "feat: channels API (track/list/config/subscribe/unsubscribe) + auto-track on video add"
```

---

## Task 9: Pending HTTP API

**Files:**
- Modify: `backend/internal/httpapi/channels_handlers.go` (add pending handlers), `backend/internal/httpapi/server.go` (routes + Deps), test `channels_handlers_test.go`

**Interfaces:**
- Consumes: `channelvideos.Store` (Deps field `Ledger *channelvideos.Store`), `videos.Store`, `jobs.Store`.
- Produces routes: `GET /api/pending` (ledger `state='pending'`), `POST /api/pending/{id}/download` (enqueue at manual priority 10), `POST /api/pending/{id}/ignore`.

- [ ] **Step 1: Failing test — list pending; download promotes; ignore hides.** Add:
```go
func TestPending_listDownloadIgnore(t *testing.T) {
	h := newPendingTestServer(t) // channels+ledger+videos+jobs stores wired
	h.seedChannel("UC1")
	h.ledger.Insert(channelvideos.Entry{VideoID: "p1", ChannelID: "UC1", Title: "A", URL: "https://www.youtube.com/watch?v=p1", DurationSeconds: 600, State: "pending"})
	h.ledger.Insert(channelvideos.Entry{VideoID: "p2", ChannelID: "UC1", Title: "B", URL: "https://www.youtube.com/watch?v=p2", DurationSeconds: 600, State: "pending"})

	if body := getJSON(t, h, "/api/pending"); !strings.Contains(body, "p1") || !strings.Contains(body, "p2") {
		t.Fatalf("pending list = %s", body)
	}
	// Download p1 → videos row queued + job at priority 10 + ledger no longer pending.
	if rr := postJSON(t, h, "/api/pending/p1/download", nil); rr.Code != http.StatusOK {
		t.Fatalf("download status = %d", rr.Code)
	}
	v, _ := h.videos.Get("p1")
	if v == nil || v.Status != "queued" {
		t.Fatalf("p1 video = %+v", v)
	}
	jl, _ := h.jobs.List()
	if len(jl) != 1 || jl[0].VideoID != "p1" || jl[0].Priority != 10 {
		t.Fatalf("jobs = %+v", jl)
	}
	// Ignore p2 → gone from pending.
	if rr := postJSON(t, h, "/api/pending/p2/ignore", nil); rr.Code != http.StatusOK {
		t.Fatalf("ignore status = %d", rr.Code)
	}
	if body := getJSON(t, h, "/api/pending"); strings.Contains(body, "p2") || strings.Contains(body, "p1") {
		t.Fatalf("pending should be empty: %s", body)
	}
}
```

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Implement the pending handlers** in `channels_handlers.go`:
```go
func (s *server) handlePendingList(w http.ResponseWriter, r *http.Request) {
	if s.ledger == nil {
		writeJSON(w, []any{})
		return
	}
	items, err := s.ledger.ListPending()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "list pending failed")
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, e := range items {
		out = append(out, map[string]any{
			"video_id": e.VideoID, "channel_id": e.ChannelID, "title": e.Title,
			"duration_seconds": e.DurationSeconds, "url": e.URL, "thumbnail_url": e.ThumbnailURL,
		})
	}
	writeJSON(w, out)
}

func (s *server) handlePendingDownload(w http.ResponseWriter, r *http.Request) {
	if s.ledger == nil || s.videos == nil || s.jobs == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "pending is not configured")
		return
	}
	id := r.PathValue("id")
	e, err := s.ledger.Get(id)
	if err != nil || e == nil || e.State != "pending" {
		writeJSONError(w, http.StatusNotFound, "pending item not found")
		return
	}
	if err := s.videos.Upsert(videos.Video{
		ID: e.VideoID, URL: e.URL, Title: e.Title, ChannelID: e.ChannelID,
		DurationSeconds: int64(e.DurationSeconds),
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "save video failed")
		return
	}
	if err := s.videos.SetStatus(e.VideoID, "queued", ""); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "save video failed")
		return
	}
	if _, err := s.jobs.Enqueue(e.VideoID, downloadPriority); err != nil { // downloadPriority == 10
		writeJSONError(w, http.StatusInternalServerError, "enqueue failed")
		return
	}
	if err := s.ledger.SetState(e.VideoID, "queued"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "update pending failed")
		return
	}
	writeJSON(w, map[string]string{"status": "queued"})
}

func (s *server) handlePendingIgnore(w http.ResponseWriter, r *http.Request) {
	if s.ledger == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "pending is not configured")
		return
	}
	id := r.PathValue("id")
	e, err := s.ledger.Get(id)
	if err != nil || e == nil {
		writeJSONError(w, http.StatusNotFound, "pending item not found")
		return
	}
	if err := s.ledger.SetState(id, "ignored"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "ignore failed")
		return
	}
	writeJSON(w, map[string]string{"status": "ignored"})
}
```

- [ ] **Step 4: Add Deps + routes.** In `server.go`: `Deps.Ledger *channelvideos.Store` + `server.ledger`; register:
```go
mux.Handle("GET /api/pending", s.requireAuth(http.HandlerFunc(s.handlePendingList)))
mux.Handle("POST /api/pending/{id}/download", s.requireAuth(http.HandlerFunc(s.handlePendingDownload)))
mux.Handle("POST /api/pending/{id}/ignore", s.requireAuth(http.HandlerFunc(s.handlePendingIgnore)))
```

- [ ] **Step 5: Run — expect PASS.** `cd backend && go test ./internal/httpapi/ -run Pending -v`.

- [ ] **Step 6: Commit.**
```bash
git add backend/internal/httpapi
git commit -m "feat: pending API (list, download-now at manual priority, ignore)"
```

---

## Task 10: Delete-channel cascade

**Files:**
- Modify: `backend/internal/channels/store.go` (add `VideoRefs` + `DeleteCascade`), `backend/internal/jobs/store.go` (add `ActiveIDsForVideos`), `backend/internal/httpapi/channels_handlers.go` (DELETE handler), `backend/internal/httpapi/server.go` (route)
- Test: `backend/internal/channels/store_test.go`, `backend/internal/httpapi/channels_handlers_test.go`

**⚠ Deliberate behavior (user sign-off "remove EVERYTHING"):** deleting a channel removes **all** of its downloaded videos, **including favorited "Kept forever" ones** — this intentionally overrides the Phase-1 favorite/retention invariant for this one explicit action. The UI guards it behind a confirm.

**Interfaces:**
- Produces:
```go
// channels
type VideoRef struct{ VideoID, MediaPath, ThumbnailPath string }
func (s *Store) VideoRefs(channelID string) ([]VideoRef, error) // videos rows for the channel
func (s *Store) DeleteCascade(channelID string) error           // deletes videos rows then channel (FK-cascades jobs, subscription, channel_videos) in one tx
// jobs
func (s *Store) ActiveIDsForVideos(videoIDs []string) ([]int64, error) // pending|running job ids
```
- The DELETE handler orchestrates in this order (per the async-cancel hazard): **read refs → cancel active jobs → delete rows → unlink files** (using the refs read before deletion).

- [ ] **Step 1: Failing test — DeleteCascade removes rows across tables.** In `channels/store_test.go`:
```go
func TestDeleteCascade_removesEverything(t *testing.T) {
	st := newTestStore(t)
	db := st.DB() // expose the underlying *sql.DB via a small accessor, or build via the shared helper
	st.Upsert(Channel{ID: "UC1", Name: "One"})
	st.Subscribe("UC1", "2000-01-01 00:00:00")
	// A downloaded video + a job + a ledger row, all for UC1.
	mustExec(t, db, `INSERT INTO videos (id,url,channel_id,status,media_path) VALUES ('v1','u','UC1','downloaded','/m/v1.mp4')`)
	mustExec(t, db, `INSERT INTO download_jobs (video_id, state) VALUES ('v1','done')`)
	mustExec(t, db, `INSERT INTO channel_videos (video_id, channel_id, state) VALUES ('v1','UC1','queued')`)

	refs, err := st.VideoRefs("UC1")
	if err != nil || len(refs) != 1 || refs[0].MediaPath != "/m/v1.mp4" {
		t.Fatalf("refs = %+v err=%v", refs, err)
	}
	if err := st.DeleteCascade("UC1"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM channels WHERE id='UC1'`,
		`SELECT count(*) FROM subscriptions WHERE channel_id='UC1'`,
		`SELECT count(*) FROM channel_videos WHERE channel_id='UC1'`,
		`SELECT count(*) FROM videos WHERE channel_id='UC1'`,
		`SELECT count(*) FROM download_jobs WHERE video_id='v1'`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%q → n=%d err=%v, want 0", q, n, err)
		}
	}
}
```

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Implement `VideoRefs` + `DeleteCascade`.** In `channels/store.go`:
```go
func (s *Store) VideoRefs(channelID string) ([]VideoRef, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id, media_path, thumbnail_path FROM videos WHERE channel_id = ?`, channelID)
	if err != nil { return nil, fmt.Errorf("video refs: %w", err) }
	defer rows.Close()
	var out []VideoRef
	for rows.Next() {
		var r VideoRef
		if err := rows.Scan(&r.VideoID, &r.MediaPath, &r.ThumbnailPath); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCascade(channelID string) error {
	tx, err := s.db.Begin()
	if err != nil { return err }
	defer tx.Rollback()
	// videos has no FK to channels, so delete its rows explicitly (this
	// FK-cascades download_jobs). Deleting the channel row FK-cascades the
	// subscription and channel_videos rows.
	if _, err := tx.Exec(`DELETE FROM videos WHERE channel_id = ?`, channelID); err != nil {
		return fmt.Errorf("delete videos for channel: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM channels WHERE id = ?`, channelID); err != nil {
		return fmt.Errorf("delete channel: %w", err)
	}
	return tx.Commit()
}
```
(If `channels.Store` doesn't already keep a `*sql.DB`, it does — `New(db *sql.DB)`. Add a `DB() *sql.DB` accessor only if the test needs it, or seed via the test's own handle.)

- [ ] **Step 4: Run — expect PASS.** `cd backend && go test ./internal/channels/ -v`.

- [ ] **Step 5: Failing test — jobs.ActiveIDsForVideos.** In `jobs/store_test.go`:
```go
func TestActiveIDsForVideos(t *testing.T) {
	st := newTestStore(t)
	seedVideo(t, st, "v1"); seedVideo(t, st, "v2")
	id1, _ := st.Enqueue("v1", 0) // pending
	st.Enqueue("v2", 0)
	// mark v2's job done so it's excluded
	// (claim then finish, or exec directly)
	ids, err := st.ActiveIDsForVideos([]string{"v1", "v2", "v3"})
	if err != nil { t.Fatal(err) }
	if len(ids) < 1 || ids[0] != id1 {
		t.Fatalf("ids = %v, want [%d]", ids, id1)
	}
}
```

- [ ] **Step 6: Run — FAIL → Step 7: Implement `ActiveIDsForVideos`.**
```go
func (s *Store) ActiveIDsForVideos(videoIDs []string) ([]int64, error) {
	if len(videoIDs) == 0 { return nil, nil }
	ph := strings.Repeat("?,", len(videoIDs))
	ph = ph[:len(ph)-1]
	args := make([]any, len(videoIDs))
	for i, v := range videoIDs { args[i] = v }
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id FROM download_jobs WHERE state IN ('pending','running') AND video_id IN (`+ph+`)`, args...)
	if err != nil { return nil, fmt.Errorf("active job ids: %w", err) }
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil { return nil, err }
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
```
(Add `strings` import.)

- [ ] **Step 8: Failing test — DELETE endpoint cancels a mid-download job then cascades.** In `channels_handlers_test.go`:
```go
func TestChannelsDelete_cancelsAndCascades(t *testing.T) {
	h := newChannelsDeleteServer(t) // real channels/jobs/videos stores + a recording fake worker
	h.seedChannel("UC1")
	h.seedDownloadedVideo("UC1", "v1", "/tmp/does-not-matter.mp4")
	jid, _ := h.jobs.Enqueue("v1", 0) // pending job for a channel video

	rr := doDelete(t, h, "/api/channels/UC1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !h.worker.canceled[jid] {
		t.Fatalf("worker.Cancel(%d) was not called", jid)
	}
	if c, _ := h.channels.Get("UC1"); c != nil {
		t.Fatal("channel still present after delete")
	}
}
```

- [ ] **Step 9: Run — FAIL → Step 10: Implement the DELETE handler** in `channels_handlers.go`:
```go
func (s *server) handleChannelsDelete(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")
	// 1. Read refs BEFORE deleting (we need media paths after the rows are gone).
	refs, err := s.channels.VideoRefs(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	// 2. Cancel any active jobs for those videos (kills a live child). The
	//    worker settles asynchronously; that's fine — we delete the rows next,
	//    and its late settle-write hits zero rows.
	if s.worker != nil && s.jobs != nil {
		vids := make([]string, len(refs))
		for i, rf := range refs { vids[i] = rf.VideoID }
		if jobIDs, err := s.jobs.ActiveIDsForVideos(vids); err == nil {
			for _, jid := range jobIDs { s.worker.Cancel(jid) }
		}
	}
	// 3. Delete rows (FK-cascades jobs, subscription, ledger).
	if err := s.channels.DeleteCascade(id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	// 4. Unlink media/thumbnail files using the refs captured in step 1.
	for _, rf := range refs {
		if rf.MediaPath != "" { media.RemoveIfInside(s.mediaDir, rf.MediaPath) }
		if rf.ThumbnailPath != "" { media.RemoveIfInside(s.mediaDir, rf.ThumbnailPath) }
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}
```
Use the existing Phase-1 media path-safety helper (check `internal/media/media.go` for the exact function name — it's the same one `handleDeleteVideo` uses to unlink inside `mediaDir`; call THAT, don't invent `RemoveIfInside` if the real name differs). Register route in `server.go`:
```go
mux.Handle("DELETE /api/channels/{id}", s.requireAuth(http.HandlerFunc(s.handleChannelsDelete)))
```

- [ ] **Step 11: Run — expect PASS.** `cd backend && go test ./internal/channels/ ./internal/jobs/ ./internal/httpapi/ -race -v`.

- [ ] **Step 12: Commit.**
```bash
git add backend/internal/channels backend/internal/jobs backend/internal/httpapi
git commit -m "feat: delete channel cascade (cancel jobs, remove videos+media+ledger+subscription)"
```

---

## Task 11: main.go wiring — start the scheduler, wire stores + runner

**Files:**
- Modify: `backend/cmd/vark/main.go`
- Test: manual smoke (no unit test — this is process wiring; the handlers/scheduler are already unit-tested)

**Interfaces:**
- Consumes everything built above. Produces a running scheduler goroutine and a fully-populated `httpapi.Deps`.

- [ ] **Step 1: Construct the new stores.** After `videosStore := videos.New(db)` add:
```go
	channelsStore := channels.New(db)
	ledgerStore := channelvideos.New(db)
```
(Add imports `github.com/trick77/vark/internal/channels`, `.../channelvideos`, `.../scan`.)

- [ ] **Step 2: Build the scheduler and start it under the worker WaitGroup.** After the sweeper is constructed, before `workerWG.Add(3)`, build:
```go
	scheduler := scan.New(scan.Deps{
		Channels:     channelsStore,
		Ledger:       ledgerStore,
		Videos:       videosStore,
		Jobs:         jobsStore,
		Settings:     settingsStore,
		Lister:       runner,
		CookieStatus: func(ctx context.Context) string { return settingsStore.CookieStatus(ctx) },
	})
```
Change `workerWG.Add(3)` to `workerWG.Add(4)` and add a fourth goroutine:
```go
	go func() {
		defer workerWG.Done()
		slog.Info("scan scheduler started")
		scheduler.Run(ctx)
	}()
```

- [ ] **Step 3: Populate the new `httpapi.Deps` fields.** In the `deps := httpapi.Deps{...}` literal add:
```go
		Channels:        channelsStore,
		ChannelResolver: runner,
		Ledger:          ledgerStore,
```

- [ ] **Step 4: Build + vet.** `cd backend && go build ./... && go vet ./...`. Expected: clean.

- [ ] **Step 5: Manual smoke (no cookie path).** 
```bash
cd backend && CGO_ENABLED=0 go build -o ../bin/vark ./cmd/vark
VARK_SESSION_SECRET=dev VARK_AUTH_MODE=dev VARK_ADDR=127.0.0.1:8080 \
  VARK_DB_PATH=./data/vark.db VARK_MEDIA_DIR=./data/media VARK_YTDLP_DIR=./data/bin ../bin/vark &
sleep 1
curl -c j -s localhost:8080/api/auth/login >/dev/null
curl -b j -s localhost:8080/api/channels?filter=all   # → []
curl -b j -s localhost:8080/api/pending                # → []
kill %1
```
Expected: both return `[]` (or `[]`-equivalent), no panic, log shows "scan scheduler started". (No cookie → the scheduler logs nothing scanned, which is correct.)

- [ ] **Step 6: Commit.**
```bash
git add backend/cmd/vark/main.go
git commit -m "feat: wire channel stores + scan scheduler goroutine into main"
```

---

## Task 12: Frontend — API client + Settings min-duration control

**Files:**
- Create: `ui/src/api/channels.ts`, `ui/src/api/pending.ts`
- Modify: `ui/src/api/types.ts`, `ui/src/api/index.ts`, `ui/src/views/Settings.tsx`
- Test: `ui/src/api/channels.test.ts`, `ui/src/views/Settings.test.tsx` (extend)

**Interfaces:**
- Produces (types): `Channel { id; handle; name; subscribed; autodownload; format_override; pending_count; downloaded_count }`, `PendingItem { video_id; channel_id; title; duration_seconds; url; thumbnail_url }`.
- Produces (api): `listChannels(filter)`, `addChannel(url, subscribe)`, `updateChannel(id, patch)`, `subscribeChannel(id)`, `unsubscribeChannel(id)`, `deleteChannel(id)`, `listPending()`, `downloadPending(id)`, `ignorePending(id)`. All via the existing `api.get/post/put/del` in `http.ts`. Re-export from `api/index.ts`.

- [ ] **Step 1: Failing test — channels client hits the right endpoints.** `ui/src/api/channels.test.ts` (mock `fetch`, mirror `http.test.ts` style):
```ts
import { describe, it, expect, vi, beforeEach } from "vitest";
import { listChannels, addChannel } from "./channels";

beforeEach(() => { vi.restoreAllMocks(); });

it("addChannel posts url + subscribe", async () => {
  const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(JSON.stringify({ id: "UC1", name: "One", subscribed: true }), { status: 201 }),
  );
  await addChannel("https://www.youtube.com/@x", true);
  const [url, init] = f.mock.calls[0];
  expect(url).toContain("/api/channels");
  expect(JSON.parse(init!.body as string)).toEqual({ url: "https://www.youtube.com/@x", subscribe: true });
});

it("listChannels passes the filter", async () => {
  const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("[]", { status: 200 }));
  await listChannels("subscribed");
  expect(f.mock.calls[0][0]).toContain("filter=subscribed");
});
```

- [ ] **Step 2: Run — expect FAIL.** `cd ui && npx vitest --run src/api/channels.test.ts`.

- [ ] **Step 3: Implement `api/channels.ts` + `api/pending.ts`.** Follow `ui/src/api/videos.ts` for the exact `api.get/post/put/del` helper names (check that file for the real client surface — use whatever it uses, e.g. `get`, `post`). Example:
```ts
import { get, post, put, del } from "./http";
import type { Channel } from "./types";

export function listChannels(filter: "all" | "subscribed" | "tracked" = "all"): Promise<Channel[]> {
  return get(`/api/channels?filter=${encodeURIComponent(filter)}`);
}
export function addChannel(url: string, subscribe: boolean): Promise<{ id: string; name: string; subscribed: boolean }> {
  return post("/api/channels", { url, subscribe });
}
export function updateChannel(id: string, patch: { autodownload?: boolean; format_override?: string }): Promise<Channel> {
  return put(`/api/channels/${encodeURIComponent(id)}`, patch);
}
export function subscribeChannel(id: string) { return post(`/api/channels/${encodeURIComponent(id)}/subscribe`, {}); }
export function unsubscribeChannel(id: string) { return post(`/api/channels/${encodeURIComponent(id)}/unsubscribe`, {}); }
export function deleteChannel(id: string) { return del(`/api/channels/${encodeURIComponent(id)}`); }
```
(If `http.ts` doesn't export `del`, use its actual delete helper name.) `pending.ts`:
```ts
import { get, post } from "./http";
import type { PendingItem } from "./types";
export function listPending(): Promise<PendingItem[]> { return get("/api/pending"); }
export function downloadPending(id: string) { return post(`/api/pending/${encodeURIComponent(id)}/download`, {}); }
export function ignorePending(id: string) { return post(`/api/pending/${encodeURIComponent(id)}/ignore`, {}); }
```
Add the `Channel`/`PendingItem` types to `types.ts` and re-export the functions from `index.ts`.

- [ ] **Step 4: Run — expect PASS.**

- [ ] **Step 5: Failing test — Settings shows + saves min duration.** In `ui/src/views/Settings.test.tsx` add a case asserting a labeled control for the minimum video length that, when changed and saved, includes `min_video_duration_seconds` in the PUT body (mirror the existing retention/throttle assertions in that file).

- [ ] **Step 6: Run — FAIL → Step 7: Implement in `Settings.tsx`.** Add a numeric control bound to `min_video_duration_seconds` (label e.g. "Ignore channel videos shorter than (seconds)"), include it in the settings load + PUT payload exactly like the existing `retention_days`/`throttle_base_seconds` controls.

- [ ] **Step 8: Run — expect PASS.** `cd ui && npx vitest --run`.

- [ ] **Step 9: Commit.**
```bash
git add ui/src/api ui/src/views/Settings.tsx ui/src/views/Settings.test.tsx
git commit -m "feat(ui): channels/pending API client + settings min-duration control"
```

---

## Task 13: Frontend — Channels view + rail nav + Add routing

**Files:**
- Modify: `ui/src/shell/Rail.tsx` (add "Channels" nav item + ViewId), `ui/src/App.tsx` (route + VIEW_META), `ui/src/views/Add.tsx` (route channel URLs)
- Create: `ui/src/views/Channels.tsx`, `ui/src/views/Channels.test.tsx`

**Interfaces:**
- Consumes: `listChannels`, `addChannel`, `updateChannel`, `subscribeChannel`, `unsubscribeChannel`, `deleteChannel`.
- Produces: a `channels` `ViewId`; a Channels view with filter chips + per-channel controls.

- [ ] **Step 1: Failing test — Channels view lists + toggles subscribe.** `ui/src/views/Channels.test.tsx`: mock `listChannels` to return one tracked + one subscribed channel; assert both render; clicking the tracked one's Subscribe calls `subscribeChannel`; the filter chips switch the query.

- [ ] **Step 2: Run — FAIL → Step 3: Implement `Channels.tsx`.** Filter chips (All/Subscribed/Tracked) → `listChannels(filter)`; each row: name/handle, pending/downloaded counts, Subscribe/Unsubscribe toggle, Autodownload toggle (`updateChannel`), format-override text field (`updateChannel`), and a Delete button behind a `window.confirm("Delete this channel and ALL its downloaded videos?")` → `deleteChannel`. Use `Icon` from `../icons` (stroke-width already set) and existing card/panel classes for visual consistency.

- [ ] **Step 4: Add the rail nav item + routing.** In `Rail.tsx`: extend `ViewId` with `"channels"`; add `{ id: "channels", label: "Channels", icon: "..." }` to the "Collect" section (pick an existing lucide name already in `icons.tsx`, e.g. a `tv`/`users`/`rss` glyph — verify it's registered in `icons.tsx`; if not, register it there). In `App.tsx`: add `channels: { title: "Channels" }` to `VIEW_META`, and a `case "channels": return <Channels .../>;` in `ViewSwitch`.

- [ ] **Step 5: Failing test — Add routes a channel URL to addChannel.** In an `Add.test.tsx` (create if absent), assert that submitting a channel URL (`https://www.youtube.com/@x`) calls `addChannel` (not the video download), and a video URL still calls the download path.

- [ ] **Step 6: Run — FAIL → Step 7: Implement in `Add.tsx`.** On submit, detect a channel URL (regex for `/channel/`, `/@`, `/c/`, `/user/`, mirroring the backend kinds) → call `addChannel(url, subscribe=false)` and show a "Tracked <name>" confirmation; otherwise keep the existing video download path. (A dedicated "and subscribe" checkbox is optional; default track-only, since the Channels view is where subscribe lives.)

- [ ] **Step 8: Run — expect PASS.** `cd ui && npx vitest --run`.

- [ ] **Step 9: Commit.**
```bash
git add ui/src/views/Channels.tsx ui/src/views/Channels.test.tsx ui/src/shell/Rail.tsx ui/src/App.tsx ui/src/views/Add.tsx ui/src/views/Add.test.tsx
git commit -m "feat(ui): channels view + rail nav + add-box channel routing"
```

---

## Task 14: Frontend — New & pending view + badge rewire

**Files:**
- Create: `ui/src/views/Pending.tsx`, `ui/src/views/Pending.test.tsx`
- Modify: `ui/src/App.tsx` (render `Pending`, rewire `pendingCount`)

**Interfaces:**
- Consumes: `listPending`, `downloadPending`, `ignorePending`.
- Produces: the pending grid; the rail's "New & pending" badge now reflects `listPending().length`, not the download-jobs count.

- [ ] **Step 1: Failing test — Pending view renders items + acts.** `ui/src/views/Pending.test.tsx`: mock `listPending` → two items; assert both render (title + remote thumbnail via `thumbnail_url`); clicking Download calls `downloadPending(id)` and removes the row; clicking Ignore calls `ignorePending(id)` and removes the row.

- [ ] **Step 2: Run — FAIL → Step 3: Implement `Pending.tsx`.** Grid of pending cards (thumbnail from `thumbnail_url` rendered directly as `<img src=...>`, title, channel, duration), each with Download-now and Ignore buttons; on success, refetch or optimistically drop the row.

- [ ] **Step 4: Rewire the badge + route.** In `App.tsx`:
  - Replace `case "pending": return <p>…</p>;` in `ViewSwitch` with `return <Pending />;` (import it).
  - Replace the jobs-derived `pendingCount` (currently `jobs.filter(...).length` at `App.tsx:175`) with a real pending count: add state `const [pendingCount, setPendingCount] = useState(0)` and a `useEffect` (gated on `authChecked && user`) that calls `listPending().then(p => setPendingCount(p.length))`; also refetch when navigating to the pending view. Keep passing `pendingCount` to `<Rail .../>` (the prop already exists). The DownloadDock in the rail continues to use `jobs` for the live queue — do NOT change that.

- [ ] **Step 5: Run — expect PASS.** `cd ui && npx vitest --run`.

- [ ] **Step 6: Commit.**
```bash
git add ui/src/views/Pending.tsx ui/src/views/Pending.test.tsx ui/src/App.tsx
git commit -m "feat(ui): new & pending view + rail badge from ledger pending count"
```

---

## Task 15: End-to-end wire-up + verification + docs

**Files:**
- Modify: `README.md` (dev-DB-recreate note; channels/subscriptions overview), `compose.yaml`/`compose.dev.yaml` (no new volumes needed — channels reuse the same DB/media; only touch if a var is missing), `docs/superpowers/plans/2026-07-18-vark-phase-2-channels.md` (check off steps)

- [ ] **Step 1: Full build + all tests.** 
```bash
make fe-build && make build && make test && make fe-test
```
Expected: all PASS, `bin/vark` present, `-race` clean.

- [ ] **Step 2: README note — recreate dev DB.** Add a short "Phase 2 upgrade" note: because migrations were squashed into `0001_init.sql`, an existing dev database won't pick up the new tables — delete it (`rm ./data/vark.db*`) and restart to re-migrate. Add a one-paragraph "Channels & subscriptions" overview (track vs subscribe, autodownload, New & pending, daily scan cadence + throttle).

- [ ] **Step 3: Live end-to-end (dev auth, real cookie — MANUAL).** Drive the app (see the `verify`/`run` skills):
  - Boot with a real DB/media dir; sign in (dev auto-login); paste a real cookie in Settings → status "active".
  - **Add** a channel by `@handle` under Channels → it appears tracked with the resolved name; Subscribe it.
  - Wait for (or force) the scheduler's first pass → the channel shows `baselined_at` set and **nothing** queued/pending (first-run baseline).
  - Simulate a "new upload" by subscribing to a channel and, on the NEXT scan, confirm a genuinely-new id lands in **New & pending** (non-autodownload) → **Download now** enqueues it (dock shows progress) → it appears in Library `downloaded`; **Ignore** removes another.
  - Flip a channel to **Autodownload** (+ a format override) → next new id enqueues automatically at low priority and downloads with the override format.
  - Paste a **video** URL in Add → confirm its channel is silently **tracked** (appears under Channels → Tracked).
  - **Delete** a channel with a downloaded video (favorite one first) → confirm the video + media file are gone (EVERYTHING cascade, favorite included), and a mid-download delete cancels the running job.
  - Confirm the ≥60s spacing + 20s throttle by watching the logs across multiple subscribed channels.
- [ ] **Step 4: Commit.**
```bash
git add README.md docs/superpowers/plans/2026-07-18-vark-phase-2-channels.md compose.yaml compose.dev.yaml
git commit -m "docs: phase-2 channels e2e verification + dev-db-recreate note"
```
- [ ] **Step 5: Open PR** `feat/phase-2-channels` → `master`; confirm CI (backend + UI, `-race`) green; the whole-branch Fable review + `finishing-a-development-branch` gate the merge.

---

## Self-Review (author checklist — completed)

- **Spec coverage:** track vs subscribe (T4), 3 tables + squashed migration + settings/videos columns (T1), `channel` URL kind + downloads reject (T2), `ChannelVideos`/`ResolveChannel` through the cookie/throttle gate (T3), ledger (T5), per-channel format override (T6), scan scheduler with first-run baseline / filters / dedup / pending-vs-autodownload / cookie-gate / 60s spacing (T7), channels API + auto-track (T8), pending API (T9), delete-cascade incl. favorite override + mid-download cancel (T10), main wiring + scheduler goroutine (T11), FE api + settings control (T12), Channels view + nav + Add routing (T13), New & pending + badge rewire (T14), e2e + docs (T15). **Deferred correctly to the next phase:** auto-unsubscribe of stale channels + stale filter — in no task.
- **Advisor corrections folded in:** pending under `/api/pending` reading `channel_videos` (T9), remote-`thumbnail_url` vs local-`thumbnail_path` kept separate (T7 enqueueAuto, T9, T14), sparse autodownload metadata stated explicitly (T7), badge rewired off the jobs filter (T14), delete-cascade sequencing cancel→delete→unlink + favorite override called out (T10), canonicalize early-return + downloads channel-reject (T2), autodownload `SetStatus("queued")` (T7).
- **Type consistency:** `ChannelEntry` (T3) consumed by `ChannelLister`/scheduler (T7); `channels.Store`/`Subscription`/`ClaimDue`/`MarkScanned` (T4) used by the scheduler (T7) and handlers (T8,10); `channelvideos.Store`/`Entry`/`ListPending`/`SetState` (T5) used by scheduler (T7) + pending API (T9); `videos.Video.RequestedFormat` + `SetRequestedFormat` (T6) used by worker (T6) + scheduler (T7); `settings.Settings.MinVideoDurationSeconds` (T1) read by the scheduler filter (T7); `downloadPriority`=10 (existing) reused for pending download-now (T9); `autoPriority`=0 for scheduler enqueue (T7).
- **Placeholder scan:** no TBD/TODO; each code step shows real code. Two deliberate "verify the real helper name in the existing file" notes (the media unlink helper in T10; the `http.ts` client method names in T12) point at concrete existing files rather than leaving behavior unspecified.
