# TubeArchivist Import — Phase A (Subscriptions) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `peeq import-ta-channels` subcommand that reads TubeArchivist's subscribed channels over its REST API and creates the matching tracked-channel and subscription rows in peeq.

**Architecture:** A new `internal/taimport` package with three pieces — an HTTP client for TA's paginated `/api/channel/` endpoint, a pure mapper turning a TA channel into peeq's `channels.Channel`, and a runner that walks pages and writes rows. `cmd/peeq/main.go` grows a subcommand dispatch in front of the existing `run()`. No schema migration, no HTTP route, no UI.

**Tech Stack:** Go 1.x stdlib only (`net/http`, `encoding/json`, `flag`). SQLite via the existing `internal/store`. Tests use `net/http/httptest` and temp-file SQLite.

## Global Constraints

- **Never call a real TubeArchivist instance, LLM, or embeddings endpoint in tests.** Fake with `httptest`. (`AGENTS.md`)
- **Never edit an already-applied migration.** This plan adds no migration at all. (`AGENTS.md`)
- Tests are package-internal `*_test.go` files next to the source. Hand-written fakes, no mocking library.
- Run backend tests with `cd backend && go test ./...` (or `make test` from the repo root).
- Commit as `trick77@users.noreply.github.com` (global git config already set — do not override).
- Work happens in the worktree at `/Users/jan/localgit/vark-ta-import` on branch `feat/tubearchivist-import`.
- Go module path is `github.com/trick77/peeq`.
- Timestamps written to `next_scan_at` use the format `"2006-01-02 15:04:05"` in UTC, matching `httpapi/channels_handlers.go:130`.

## Spec

`docs/superpowers/specs/2026-07-21-peeq-tubearchivist-import-design.html`, section "Phase A — subscriptions".

## File Structure

| File | Responsibility |
|---|---|
| `backend/internal/taimport/client.go` | HTTP client for TA's API. Token auth, pagination, 404-means-empty handling. Knows nothing about peeq's schema. |
| `backend/internal/taimport/client_test.go` | Client tests against `httptest`. |
| `backend/internal/taimport/channels.go` | Pure mapping (TA channel → `channels.Channel`) plus the `ImportChannels` runner. |
| `backend/internal/taimport/channels_test.go` | Mapper tests (pure) and runner tests (temp SQLite + fake client). |
| `backend/cmd/peeq/main.go` | Subcommand dispatch + the `import-ta-channels` command wiring. |
| `backend/cmd/peeq/importcmd.go` | Flag parsing and boot for the subcommand, kept out of `main.go` which is already long. |

**Why this split:** the client is the only part that touches the network, so isolating it is what makes everything else testable without `httptest`. The mapper is pure and gets the cheapest, highest-value tests.

---

### Task 1: TA API client — a single page

**Files:**
- Create: `backend/internal/taimport/client.go`
- Test: `backend/internal/taimport/client_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `type Channel struct { ID, Name string; Active, Subscribed bool }`
  - `type Client struct { ... }`
  - `func NewClient(baseURL, token string, hc *http.Client) *Client`
  - `func (c *Client) ChannelPage(ctx context.Context, page int) ([]Channel, bool, error)` — returns the page's channels and whether more pages follow.

**Background for the implementer:** TubeArchivist is a self-hosted YouTube archiver. Its API is Django REST Framework. Two things about it drive this design and are easy to get wrong:

1. Auth uses the DRF scheme `Authorization: Token <key>` — **not** `Bearer`.
2. **An empty result returns HTTP 404**, not 200-with-empty-list. A naive client treats that as a failure; here it means "no more pages" and must terminate the walk cleanly.

The response shape is `{"data": [...], "paginate": {...}}`. We deliberately ignore `paginate` and decide "is there more?" from whether `data` came back non-empty — TA's `paginate` block varies with user config and is more fragile than just walking until empty.

- [ ] **Step 1: Write the failing test**

Create `backend/internal/taimport/client_test.go`:

```go
package taimport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChannelPage_parsesDataAndSendsTokenAuth(t *testing.T) {
	var gotAuth, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"channel_id":"UC_aaa","channel_name":"Alpha","channel_active":true,"channel_subscribed":true},
			{"channel_id":"UC_bbb","channel_name":"Beta","channel_active":false,"channel_subscribed":true}
		]}`))
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "secret-token", srv.Client())

	got, more, err := testee.ChannelPage(context.Background(), 1)
	if err != nil {
		t.Fatalf("ChannelPage: %v", err)
	}
	if !more {
		t.Error("more = false, want true (page was non-empty)")
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "UC_aaa" || got[0].Name != "Alpha" || !got[0].Active || !got[0].Subscribed {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Active {
		t.Error("got[1].Active = true, want false")
	}

	// DRF token scheme, not Bearer.
	if gotAuth != "Token secret-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Token secret-token")
	}
	if gotPath != "/api/channel/" {
		t.Errorf("path = %q, want /api/channel/", gotPath)
	}
	// Only subscribed channels: TA indexes a channel for every video ever
	// downloaded, so an unfiltered import would subscribe to channels that
	// were never followed.
	if gotQuery != "filter=subscribed&page=1" {
		t.Errorf("query = %q, want filter=subscribed&page=1", gotQuery)
	}
}

func TestChannelPage_404MeansNoMorePages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"no results"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())

	got, more, err := testee.ChannelPage(context.Background(), 9)
	if err != nil {
		t.Fatalf("ChannelPage: %v", err)
	}
	if more {
		t.Error("more = true, want false on 404")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestChannelPage_emptyDataMeansNoMorePages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())

	_, more, err := testee.ChannelPage(context.Background(), 3)
	if err != nil {
		t.Fatalf("ChannelPage: %v", err)
	}
	if more {
		t.Error("more = true, want false on empty data")
	}
}

func TestChannelPage_serverErrorIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())

	if _, _, err := testee.ChannelPage(context.Background(), 1); err == nil {
		t.Fatal("err = nil, want an error on HTTP 500")
	}
}

func TestChannelPage_unauthorizedMentionsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())

	_, _, err := testee.ChannelPage(context.Background(), 1)
	if err == nil {
		t.Fatal("err = nil, want an error on HTTP 401")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("err = %q, want it to mention the token", err)
	}
}
```

Add `"strings"` to the test imports.

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/jan/localgit/vark-ta-import/backend && go test ./internal/taimport/ -run TestChannelPage -v
```

Expected: FAIL — the package does not compile, `undefined: NewClient`.

- [ ] **Step 3: Write minimal implementation**

Create `backend/internal/taimport/client.go`:

```go
// Package taimport reads an existing TubeArchivist library over TubeArchivist's
// REST API so it can be migrated into peeq. It is a one-shot migration helper,
// not a sync: it has no delta tracking and is expected to be run by hand from
// the CLI while the peeq server is stopped.
package taimport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Channel is the subset of a TubeArchivist channel document this migration
// needs. TubeArchivist stores considerably more (description, subscriber
// count, tags, thumbnail/banner/TV-art URLs, per-channel download overrides);
// none of it has a home in peeq's channels table, so none of it is decoded.
//
// Note there is no handle: TubeArchivist does not store the @handle at all.
// peeq's channels.handle is therefore left empty by this import, which is
// already a normal state (it is parsed best-effort from a pasted URL in
// httpapi/channels_handlers.go and is omitempty in the API).
type Channel struct {
	ID         string
	Name       string
	Active     bool // false once the channel is gone from YouTube
	Subscribed bool
}

// channelDoc is the wire shape of one entry in TubeArchivist's /api/channel/
// response.
type channelDoc struct {
	ID         string `json:"channel_id"`
	Name       string `json:"channel_name"`
	Active     bool   `json:"channel_active"`
	Subscribed bool   `json:"channel_subscribed"`
}

// pageEnvelope is TubeArchivist's list-response wrapper. It also carries a
// "paginate" block, deliberately not decoded: its page size comes from the
// TubeArchivist user's own config and its last-page arithmetic degrades once
// Elasticsearch's 10k result ceiling is hit. Walking until a page comes back
// empty is both simpler and more robust.
type pageEnvelope struct {
	Data []channelDoc `json:"data"`
}

// Client talks to a TubeArchivist instance's REST API.
type Client struct {
	baseURL string
	token   string
	hc      *http.Client
}

// NewClient returns a Client for the TubeArchivist instance at baseURL (e.g.
// "http://tubearchivist:8000"), authenticating with token. Pass nil for hc to
// get a client with a sane timeout.
func NewClient(baseURL, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		hc:      hc,
	}
}

// do issues an authenticated GET and decodes the JSON body into out. It
// reports (found=false) for HTTP 404, which TubeArchivist returns for an empty
// result set rather than an empty list.
func (c *Client) do(ctx context.Context, path string, query url.Values, out any) (found bool, err error) {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("taimport: build request %s: %w", path, err)
	}
	// TubeArchivist runs Django REST Framework, whose TokenAuthentication
	// expects the "Token" keyword. "Bearer" is silently unauthenticated.
	req.Header.Set("Authorization", "Token "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return false, fmt.Errorf("taimport: GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// Empty result set, not a failure.
		return false, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return false, fmt.Errorf("taimport: GET %s: %s — check the TubeArchivist API token", path, resp.Status)
	case resp.StatusCode != http.StatusOK:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, fmt.Errorf("taimport: GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return false, fmt.Errorf("taimport: decode %s: %w", path, err)
	}
	return true, nil
}

// ChannelPage fetches one page of subscribed channels. more reports whether
// the page had any entries, i.e. whether it is worth asking for the next one.
//
// The filter=subscribed query is load-bearing, not a convenience:
// TubeArchivist indexes a channel document for every video ever downloaded,
// including one-off downloads from channels that were never followed.
// Importing unfiltered would create peeq subscriptions for all of them.
func (c *Client) ChannelPage(ctx context.Context, page int) (chans []Channel, more bool, err error) {
	q := url.Values{}
	q.Set("filter", "subscribed")
	q.Set("page", strconv.Itoa(page))

	var env pageEnvelope
	found, err := c.do(ctx, "/api/channel/", q, &env)
	if err != nil {
		return nil, false, err
	}
	if !found || len(env.Data) == 0 {
		return nil, false, nil
	}

	out := make([]Channel, 0, len(env.Data))
	for _, d := range env.Data {
		out = append(out, Channel{
			ID:         d.ID,
			Name:       d.Name,
			Active:     d.Active,
			Subscribed: d.Subscribed,
		})
	}
	return out, true, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/jan/localgit/vark-ta-import/backend && go test ./internal/taimport/ -run TestChannelPage -v
```

Expected: PASS — all five tests.

- [ ] **Step 5: Commit**

```bash
cd /Users/jan/localgit/vark-ta-import
git add backend/internal/taimport/client.go backend/internal/taimport/client_test.go
git commit -m "feat(taimport): TubeArchivist API client for subscribed channels

Uses DRF's 'Token' auth scheme, not Bearer, and treats HTTP 404 as an
empty result set rather than a failure -- both are TubeArchivist quirks
that would otherwise fail silently or spuriously.

filter=subscribed is load-bearing: TubeArchivist indexes a channel for
every video ever downloaded, so an unfiltered import would subscribe to
channels that were never followed."
```

---

### Task 2: Walk all pages

**Files:**
- Modify: `backend/internal/taimport/client.go` (append `AllChannels`)
- Test: `backend/internal/taimport/client_test.go` (append)

**Interfaces:**
- Consumes: `Client.ChannelPage` from Task 1.
- Produces: `func (c *Client) AllChannels(ctx context.Context) ([]Channel, error)`

**Background:** Pagination walks until a page comes back empty. The loop needs a hard page cap so that a TubeArchivist bug (or a proxy that returns 200-with-content for every page) cannot spin forever against a live server.

- [ ] **Step 1: Write the failing test**

Append to `backend/internal/taimport/client_test.go`:

```go
func TestAllChannels_walksUntilEmptyPage(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("page")
		pages = append(pages, p)
		switch p {
		case "1":
			_, _ = w.Write([]byte(`{"data":[{"channel_id":"UC_a","channel_name":"A","channel_subscribed":true,"channel_active":true}]}`))
		case "2":
			_, _ = w.Write([]byte(`{"data":[{"channel_id":"UC_b","channel_name":"B","channel_subscribed":true,"channel_active":true}]}`))
		default:
			http.Error(w, `{"error":"none"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())

	got, err := testee.AllChannels(context.Background())
	if err != nil {
		t.Fatalf("AllChannels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "UC_a" || got[1].ID != "UC_b" {
		t.Errorf("got = %+v", got)
	}
	if len(pages) != 3 {
		t.Errorf("requested pages = %v, want three requests (1, 2, then the empty 3)", pages)
	}
}

func TestAllChannels_propagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())

	if _, err := testee.AllChannels(context.Background()); err == nil {
		t.Fatal("err = nil, want the page error propagated")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/jan/localgit/vark-ta-import/backend && go test ./internal/taimport/ -run TestAllChannels -v
```

Expected: FAIL — `testee.AllChannels undefined`.

- [ ] **Step 3: Write minimal implementation**

Append to `backend/internal/taimport/client.go`:

```go
// maxChannelPages bounds the pagination walk. TubeArchivist's default page
// size is 25, so this allows 50k subscribed channels — far beyond any real
// library, while still guaranteeing the loop terminates if a misbehaving
// instance or proxy never returns an empty page.
const maxChannelPages = 2000

// AllChannels pages through every subscribed channel, in the order
// TubeArchivist returns them.
func (c *Client) AllChannels(ctx context.Context) ([]Channel, error) {
	var all []Channel
	for page := 1; page <= maxChannelPages; page++ {
		batch, more, err := c.ChannelPage(ctx, page)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if !more {
			return all, nil
		}
	}
	return nil, fmt.Errorf("taimport: channel listing exceeded %d pages; refusing to keep paging", maxChannelPages)
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/jan/localgit/vark-ta-import/backend && go test ./internal/taimport/ -v
```

Expected: PASS — all Task 1 and Task 2 tests.

- [ ] **Step 5: Commit**

```bash
cd /Users/jan/localgit/vark-ta-import
git add backend/internal/taimport/
git commit -m "feat(taimport): page through all subscribed channels

Walks until a page comes back empty rather than trusting TubeArchivist's
paginate block, whose page size comes from the TA user's own config and
whose arithmetic degrades at Elasticsearch's 10k ceiling. Hard page cap
guarantees termination against a misbehaving instance."
```

---

### Task 3: The import runner

**Files:**
- Create: `backend/internal/taimport/channels.go`
- Test: `backend/internal/taimport/channels_test.go`

**Interfaces:**
- Consumes: `Channel` from Task 1.
- Produces:
  - `type ChannelLister interface { AllChannels(ctx context.Context) ([]Channel, error) }`
  - `type ChannelWriter interface { Upsert(c channels.Channel) error; Subscribe(channelID, nextScanAt string) error }`
  - `type ChannelResult struct { Subscribed, Active, Inactive, Skipped int; InactiveNames []string }`
  - `func ImportChannels(ctx context.Context, lister ChannelLister, w ChannelWriter, dryRun bool, now time.Time) (ChannelResult, error)`

**Background for the implementer:** `*channels.Store` (in `internal/channels/store.go`) already satisfies `ChannelWriter` — `Upsert` is at line 171 and `Subscribe` at line 279. Declaring the interface here rather than taking the concrete store is what lets the test use a spy without a database, and it follows the pattern used throughout `internal/httpapi` (narrow interfaces declared at the consumer).

Three behaviours matter:

1. **Both active and inactive channels are imported.** Inactive means the channel is gone from YouTube. This is a deliberate decision — peeq's own auto-unsubscribe will detect and retire the dead ones over the following days. The counts are reported separately so the operator can see what they inherited.
2. **A channel with `Subscribed == false` is skipped.** The client already filters server-side, but a defensive check here means a TubeArchivist version whose filter behaves differently cannot quietly create unwanted subscriptions.
3. **`Subscribe` is already idempotent** (`ON CONFLICT(channel_id) DO NOTHING`, `store.go:282`) and `Upsert` refreshes name on conflict, so re-running is safe and needs no extra guard.

Note `Subscribe` takes no autodownload argument — the column defaults to `0` in `0001_init.sql:130`, which is exactly the "autodownload off" the spec requires. Nothing extra to do.

- [ ] **Step 1: Write the failing test**

Create `backend/internal/taimport/channels_test.go`:

```go
package taimport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/channels"
)

// fakeLister returns a canned channel list.
type fakeLister struct {
	out []Channel
	err error
}

func (f *fakeLister) AllChannels(context.Context) ([]Channel, error) { return f.out, f.err }

// spyWriter records what the runner asked for.
type spyWriter struct {
	upserted   []channels.Channel
	subscribed []string
	nextScanAt []string
	failOn     string // channel id whose Upsert should fail
}

func (s *spyWriter) Upsert(c channels.Channel) error {
	if c.ID == s.failOn {
		return errors.New("boom")
	}
	s.upserted = append(s.upserted, c)
	return nil
}

func (s *spyWriter) Subscribe(channelID, nextScanAt string) error {
	s.subscribed = append(s.subscribed, channelID)
	s.nextScanAt = append(s.nextScanAt, nextScanAt)
	return nil
}

var fixedNow = time.Date(2026, 7, 21, 14, 30, 5, 0, time.UTC)

func TestImportChannels_importsActiveAndInactive(t *testing.T) {
	// Given
	lister := &fakeLister{out: []Channel{
		{ID: "UC_a", Name: "Alpha", Active: true, Subscribed: true},
		{ID: "UC_b", Name: "Beta", Active: false, Subscribed: true},
	}}
	w := &spyWriter{}

	// When
	got, err := ImportChannels(context.Background(), lister, w, false, fixedNow)

	// Then
	if err != nil {
		t.Fatalf("ImportChannels: %v", err)
	}
	if got.Subscribed != 2 || got.Active != 1 || got.Inactive != 1 || got.Skipped != 0 {
		t.Errorf("result = %+v, want Subscribed:2 Active:1 Inactive:1 Skipped:0", got)
	}
	if len(w.upserted) != 2 || len(w.subscribed) != 2 {
		t.Fatalf("upserted=%d subscribed=%d, want 2 and 2", len(w.upserted), len(w.subscribed))
	}
	if w.upserted[0].ID != "UC_a" || w.upserted[0].Name != "Alpha" {
		t.Errorf("upserted[0] = %+v", w.upserted[0])
	}
	// TubeArchivist has no @handle, so peeq's stays empty.
	if w.upserted[0].Handle != "" {
		t.Errorf("Handle = %q, want empty (TA does not store handles)", w.upserted[0].Handle)
	}
	// next_scan_at must match the format used elsewhere in peeq.
	if w.nextScanAt[0] != "2026-07-21 14:30:05" {
		t.Errorf("nextScanAt = %q, want %q", w.nextScanAt[0], "2026-07-21 14:30:05")
	}
}

func TestImportChannels_skipsUnsubscribed(t *testing.T) {
	// Given: a channel TubeArchivist knows about only from a one-off download.
	lister := &fakeLister{out: []Channel{
		{ID: "UC_a", Name: "Alpha", Active: true, Subscribed: true},
		{ID: "UC_oneoff", Name: "OneOff", Active: true, Subscribed: false},
	}}
	w := &spyWriter{}

	// When
	got, err := ImportChannels(context.Background(), lister, w, false, fixedNow)

	// Then
	if err != nil {
		t.Fatalf("ImportChannels: %v", err)
	}
	if got.Subscribed != 1 || got.Skipped != 1 {
		t.Errorf("result = %+v, want Subscribed:1 Skipped:1", got)
	}
	if len(w.subscribed) != 1 || w.subscribed[0] != "UC_a" {
		t.Errorf("subscribed = %v, want [UC_a] only", w.subscribed)
	}
	for _, c := range w.upserted {
		if c.ID == "UC_oneoff" {
			t.Error("never-subscribed channel was tracked; it must be skipped entirely")
		}
	}
}

func TestImportChannels_dryRunWritesNothing(t *testing.T) {
	// Given
	lister := &fakeLister{out: []Channel{
		{ID: "UC_a", Name: "Alpha", Active: true, Subscribed: true},
		{ID: "UC_b", Name: "Beta", Active: false, Subscribed: true},
	}}
	w := &spyWriter{}

	// When
	got, err := ImportChannels(context.Background(), lister, w, true, fixedNow)

	// Then
	if err != nil {
		t.Fatalf("ImportChannels: %v", err)
	}
	if got.Subscribed != 2 || got.Active != 1 || got.Inactive != 1 {
		t.Errorf("result = %+v, want the same counts as a real run", got)
	}
	if len(w.upserted) != 0 || len(w.subscribed) != 0 {
		t.Errorf("dry run wrote: upserted=%v subscribed=%v", w.upserted, w.subscribed)
	}
}

func TestImportChannels_skipsEmptyID(t *testing.T) {
	// Given: a malformed TubeArchivist document.
	lister := &fakeLister{out: []Channel{
		{ID: "", Name: "Nameless", Active: true, Subscribed: true},
	}}
	w := &spyWriter{}

	// When
	got, err := ImportChannels(context.Background(), lister, w, false, fixedNow)

	// Then
	if err != nil {
		t.Fatalf("ImportChannels: %v", err)
	}
	if got.Subscribed != 0 || got.Skipped != 1 {
		t.Errorf("result = %+v, want Subscribed:0 Skipped:1", got)
	}
	if len(w.upserted) != 0 {
		t.Error("a channel with no id must not be written")
	}
}

func TestImportChannels_listErrorPropagates(t *testing.T) {
	lister := &fakeLister{err: errors.New("network down")}
	w := &spyWriter{}

	if _, err := ImportChannels(context.Background(), lister, w, false, fixedNow); err == nil {
		t.Fatal("err = nil, want the lister error propagated")
	}
}

func TestImportChannels_upsertErrorPropagates(t *testing.T) {
	lister := &fakeLister{out: []Channel{
		{ID: "UC_bad", Name: "Bad", Active: true, Subscribed: true},
	}}
	w := &spyWriter{failOn: "UC_bad"}

	if _, err := ImportChannels(context.Background(), lister, w, false, fixedNow); err == nil {
		t.Fatal("err = nil, want the upsert error propagated")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/jan/localgit/vark-ta-import/backend && go test ./internal/taimport/ -run TestImportChannels -v
```

Expected: FAIL — `undefined: ImportChannels`.

- [ ] **Step 3: Write minimal implementation**

Create `backend/internal/taimport/channels.go`:

```go
package taimport

import (
	"context"
	"fmt"
	"time"

	"github.com/trick77/peeq/internal/channels"
)

// nextScanAtLayout matches the timestamp format peeq writes elsewhere when
// subscribing (see httpapi/channels_handlers.go).
const nextScanAtLayout = "2006-01-02 15:04:05"

// ChannelLister supplies TubeArchivist's subscribed channels. *Client
// satisfies it; tests use a fake so no HTTP is needed.
type ChannelLister interface {
	AllChannels(ctx context.Context) ([]Channel, error)
}

// ChannelWriter is the slice of *channels.Store this import needs.
type ChannelWriter interface {
	Upsert(c channels.Channel) error
	Subscribe(channelID, nextScanAt string) error
}

// ChannelResult summarises an import run. Active and Inactive sum to
// Subscribed; Skipped counts channels deliberately left out.
type ChannelResult struct {
	Subscribed int
	Active     int
	Inactive   int
	Skipped    int
	// Names of the inactive channels, so the operator can see which dead
	// subscriptions they inherited. peeq's auto-unsubscribe will retire these
	// on its own over the following days.
	InactiveNames []string
}

// ImportChannels creates a tracked-channel row and a subscription for every
// channel TubeArchivist has subscribed, active or not.
//
// Subscriptions are created with autodownload off — that is the schema default
// (subscriptions.autodownload DEFAULT 0), so Subscribe needs no extra
// argument. This matters: peeq's first scan of a channel baselines it, marking
// everything currently on the channel as "seen" rather than pending, so
// importing a subscription carries it over without flooding the pending queue
// or re-downloading the back catalogue.
//
// Re-running is safe. Upsert refreshes name on conflict and Subscribe is
// ON CONFLICT DO NOTHING, so a partial run can simply be repeated.
//
// When dryRun is true the counts are computed exactly as for a real run but
// nothing is written.
func ImportChannels(ctx context.Context, lister ChannelLister, w ChannelWriter, dryRun bool, now time.Time) (ChannelResult, error) {
	var res ChannelResult

	all, err := lister.AllChannels(ctx)
	if err != nil {
		return res, err
	}

	nextScanAt := now.UTC().Format(nextScanAtLayout)

	for _, c := range all {
		// Defensive: the client already asks for filter=subscribed, but a
		// TubeArchivist version whose filter behaves differently must not
		// quietly create subscriptions for channels that were never followed.
		if !c.Subscribed || c.ID == "" {
			res.Skipped++
			continue
		}

		res.Subscribed++
		if c.Active {
			res.Active++
		} else {
			res.Inactive++
			res.InactiveNames = append(res.InactiveNames, c.Name)
		}

		if dryRun {
			continue
		}

		// Handle is left empty on purpose: TubeArchivist does not store the
		// @handle, and peeq treats an empty handle as normal.
		if err := w.Upsert(channels.Channel{ID: c.ID, Name: c.Name}); err != nil {
			return res, fmt.Errorf("taimport: track channel %s: %w", c.ID, err)
		}
		if err := w.Subscribe(c.ID, nextScanAt); err != nil {
			return res, fmt.Errorf("taimport: subscribe %s: %w", c.ID, err)
		}
	}

	return res, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/jan/localgit/vark-ta-import/backend && go test ./internal/taimport/ -v
```

Expected: PASS — all tests from Tasks 1–3.

- [ ] **Step 5: Commit**

```bash
cd /Users/jan/localgit/vark-ta-import
git add backend/internal/taimport/channels.go backend/internal/taimport/channels_test.go
git commit -m "feat(taimport): import runner for subscriptions

Creates a tracked channel and a subscription per TubeArchivist
subscription, active or not. autodownload is left at the schema default
of 0 so peeq's first scan baselines the channel rather than flooding the
pending queue with its back catalogue.

Re-entrant by construction: Upsert refreshes on conflict and Subscribe is
ON CONFLICT DO NOTHING, so a partial run is simply repeated."
```

---

### Task 4: Verify against a real database

**Files:**
- Modify: `backend/internal/taimport/channels_test.go` (append)

**Interfaces:**
- Consumes: `ImportChannels` from Task 3, `channels.New` from `internal/channels`, `store.Open`/`store.Migrate` from `internal/store`.
- Produces: nothing new.

**Background:** Tasks 1–3 prove the logic against a spy. This task proves `*channels.Store` genuinely satisfies `ChannelWriter` and that the rows land correctly — the interface could match while the real store rejects the write (e.g. `subscriptions.next_scan_at` is `NOT NULL` with no default, so a wrong timestamp would fail only here).

The existing pattern for DB tests is a temp-file SQLite plus `store.Migrate`, as in `internal/videos/store_test.go:17`. Use a temp **file**, not `:memory:` — the WAL pragma in `store.Open` expects a real file.

- [ ] **Step 1: Write the failing test**

Append to `backend/internal/taimport/channels_test.go`:

```go
func TestImportChannels_againstRealStore(t *testing.T) {
	// Given a migrated database and the real channels store.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	realStore := channels.New(db)

	lister := &fakeLister{out: []Channel{
		{ID: "UC_a", Name: "Alpha", Active: true, Subscribed: true},
		{ID: "UC_b", Name: "Beta", Active: false, Subscribed: true},
	}}

	// When
	got, err := ImportChannels(context.Background(), lister, realStore, false, fixedNow)
	if err != nil {
		t.Fatalf("ImportChannels: %v", err)
	}
	if got.Subscribed != 2 {
		t.Fatalf("Subscribed = %d, want 2", got.Subscribed)
	}

	// Then both channels are tracked AND subscribed, with autodownload off.
	items, err := realStore.List("subscribed")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("subscribed channels = %d, want 2", len(items))
	}
	for _, it := range items {
		if !it.Subscribed {
			t.Errorf("%s: Subscribed = false", it.ID)
		}
		if it.Autodownload {
			t.Errorf("%s: Autodownload = true, want off so the first scan baselines instead of downloading the back catalogue", it.ID)
		}
	}

	// And re-running changes nothing (idempotent).
	if _, err := ImportChannels(context.Background(), lister, realStore, false, fixedNow); err != nil {
		t.Fatalf("second ImportChannels: %v", err)
	}
	items2, err := realStore.List("subscribed")
	if err != nil {
		t.Fatalf("List after rerun: %v", err)
	}
	if len(items2) != 2 {
		t.Errorf("after rerun subscribed = %d, want still 2", len(items2))
	}
}
```

Add these imports to the test file: `"path/filepath"`, `"github.com/trick77/peeq/internal/store"`.

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/jan/localgit/vark-ta-import/backend && go test ./internal/taimport/ -run TestImportChannels_againstRealStore -v
```

Expected: FAIL — compile error on the new imports until they are added; then PASS if the implementation is already correct. **If it fails at runtime**, the interface does not match the real store and Task 3 needs fixing — that is exactly what this test is for.

> Note for the implementer: this task is a verification test against code that already exists, so the classic red-then-green cycle does not apply. If it passes first time, that is the correct outcome — do not weaken the test to make it fail. Confirm it is genuinely exercising the real store by temporarily changing `want 2` to `want 3` and seeing it fail, then change it back.

- [ ] **Step 3: Confirm `channels.New` and `List` signatures match**

```bash
cd /Users/jan/localgit/vark-ta-import/backend && grep -n "^func New\|^func (s \*Store) List" internal/channels/store.go
```

Expected: `func New(db *sql.DB) *Store` and `func (s *Store) List(filter string) ([]ListItem, error)`. If `List` takes different arguments, adjust the test call accordingly.

- [ ] **Step 4: Run the full package test suite**

```bash
cd /Users/jan/localgit/vark-ta-import/backend && go test ./internal/taimport/ -v
```

Expected: PASS — every test.

- [ ] **Step 5: Commit**

```bash
cd /Users/jan/localgit/vark-ta-import
git add backend/internal/taimport/channels_test.go
git commit -m "test(taimport): verify import against the real channels store

Proves *channels.Store satisfies ChannelWriter and that rows actually
land -- subscriptions.next_scan_at is NOT NULL with no default, so a
wrong timestamp format would only surface here. Also asserts autodownload
lands off and that a re-run is idempotent."
```

---

### Task 5: The `import-ta-channels` subcommand

**Files:**
- Create: `backend/cmd/peeq/importcmd.go`
- Modify: `backend/cmd/peeq/main.go` (the `main` function, lines 44–58)
- Test: `backend/cmd/peeq/importcmd_test.go`

**Interfaces:**
- Consumes: `taimport.NewClient`, `taimport.ImportChannels`, `taimport.ChannelResult` from Tasks 1–3.
- Produces: `func runImportChannels(args []string) error`, `func formatChannelResult(res taimport.ChannelResult, dryRun bool) string`

**Background:** The container's `ENTRYPOINT` is already `/usr/local/bin/peeq` (`backend/Containerfile:64`), so adding a subcommand means the migration runs as `docker compose run --rm peeq import-ta-channels …` with no Containerfile change.

Dispatch must be conservative: if `os.Args[1]` is not a known subcommand, the binary behaves exactly as it does today. The server is started by `docker compose up` with no arguments, so this cannot regress.

The subcommand opens the database directly, so **peeq must be stopped when it runs**. It reuses `config.Load()` for `BACKEND_DB_PATH` rather than adding a flag, so it picks up the same DB the server uses.

- [ ] **Step 1: Write the failing test**

Create `backend/cmd/peeq/importcmd_test.go`:

```go
package main

import (
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/taimport"
)

func TestFormatChannelResult_realRun(t *testing.T) {
	res := taimport.ChannelResult{
		Subscribed: 12, Active: 10, Inactive: 2, Skipped: 37,
		InactiveNames: []string{"Dead Channel", "Gone Too"},
	}

	got := formatChannelResult(res, false)

	for _, want := range []string{"12", "10", "2", "37", "Dead Channel", "Gone Too"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "dry run") {
		t.Errorf("real run mentioned a dry run:\n%s", got)
	}
}

func TestFormatChannelResult_dryRunSaysSo(t *testing.T) {
	res := taimport.ChannelResult{Subscribed: 3, Active: 3}

	got := formatChannelResult(res, true)

	if !strings.Contains(strings.ToLower(got), "dry run") {
		t.Errorf("dry run not labelled:\n%s", got)
	}
	if !strings.Contains(strings.ToLower(got), "nothing was written") {
		t.Errorf("dry run did not say nothing was written:\n%s", got)
	}
}

func TestFormatChannelResult_noInactiveOmitsTheList(t *testing.T) {
	res := taimport.ChannelResult{Subscribed: 5, Active: 5}

	got := formatChannelResult(res, false)

	if strings.Contains(strings.ToLower(got), "inactive channel") {
		t.Errorf("listed inactive channels when there are none:\n%s", got)
	}
}

func TestRunImportChannels_requiresURLAndToken(t *testing.T) {
	if err := runImportChannels([]string{}); err == nil {
		t.Fatal("err = nil, want an error when --ta-url is missing")
	}
	if err := runImportChannels([]string{"--ta-url", "http://ta:8000"}); err == nil {
		t.Fatal("err = nil, want an error when --ta-token is missing")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/jan/localgit/vark-ta-import/backend && go test ./cmd/peeq/ -run "TestFormatChannelResult|TestRunImportChannels" -v
```

Expected: FAIL — `undefined: formatChannelResult`, `undefined: runImportChannels`.

- [ ] **Step 3: Write minimal implementation**

Create `backend/cmd/peeq/importcmd.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/config"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/taimport"
)

// runImportChannels implements `peeq import-ta-channels`: a one-shot migration
// that copies TubeArchivist's subscriptions into peeq.
//
// It opens the database directly, so the peeq server must be stopped while it
// runs. The DB path comes from config (BACKEND_DB_PATH), not a flag, so it
// always targets the same database the server uses.
func runImportChannels(args []string) error {
	fs := flag.NewFlagSet("import-ta-channels", flag.ContinueOnError)
	var (
		taURL   = fs.String("ta-url", "", "TubeArchivist base URL, e.g. http://tubearchivist:8000")
		taToken = fs.String("ta-token", "", "TubeArchivist API token (TA settings UI, or GET /api/appsettings/token/)")
		dryRun  = fs.Bool("dry-run", false, "report what would be imported without writing anything")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *taURL == "" {
		return fmt.Errorf("--ta-url is required")
	}
	if *taToken == "" {
		return fmt.Errorf("--ta-token is required")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		return err
	}

	client := taimport.NewClient(*taURL, *taToken, nil)
	store := channels.New(db)

	res, err := taimport.ImportChannels(context.Background(), client, store, *dryRun, time.Now())
	if err != nil {
		return err
	}

	fmt.Println(formatChannelResult(res, *dryRun))
	return nil
}

// formatChannelResult renders an import summary for the terminal.
func formatChannelResult(res taimport.ChannelResult, dryRun bool) string {
	var b strings.Builder

	if dryRun {
		b.WriteString("DRY RUN — nothing was written.\n\n")
	}
	fmt.Fprintf(&b, "Subscriptions:  %d\n", res.Subscribed)
	fmt.Fprintf(&b, "  active:       %d\n", res.Active)
	fmt.Fprintf(&b, "  inactive:     %d\n", res.Inactive)
	fmt.Fprintf(&b, "Skipped:        %d  (channels TubeArchivist knows but was never subscribed to)\n", res.Skipped)

	if len(res.InactiveNames) > 0 {
		b.WriteString("\nInactive channels imported (gone from YouTube).\n")
		b.WriteString("peeq's auto-unsubscribe will retire these on its own over the next few days:\n")
		for _, n := range res.InactiveNames {
			fmt.Fprintf(&b, "  - %s\n", n)
		}
	}

	if !dryRun {
		b.WriteString("\nAll subscriptions have autodownload OFF. peeq's first scan of each\n")
		b.WriteString("channel baselines it, so the pending queue will not fill with back\n")
		b.WriteString("catalogue. Turn autodownload on per channel when you want it.\n")
	}

	return b.String()
}

// importSubcommands maps a subcommand name to its entry point. Anything not
// listed here falls through to the normal server boot.
var importSubcommands = map[string]func([]string) error{
	"import-ta-channels": runImportChannels,
}

// dispatchSubcommand runs a subcommand if os.Args names one, reporting whether
// it handled the invocation.
func dispatchSubcommand() (handled bool) {
	if len(os.Args) < 2 {
		return false
	}
	fn, ok := importSubcommands[os.Args[1]]
	if !ok {
		return false
	}
	if err := fn(os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", os.Args[1], err)
		os.Exit(1)
	}
	return true
}
```

Note the local variable `store` shadows the imported `store` package after `store.Migrate` is called — that is legal here because the package is not used again, but rename it to `chStore` if the compiler or linter objects:

```go
	chStore := channels.New(db)
	res, err := taimport.ImportChannels(context.Background(), client, chStore, *dryRun, time.Now())
```

Use the `chStore` form — it is clearer and avoids the shadowing question entirely.

Now modify `backend/cmd/peeq/main.go`. The current `main` is:

```go
func main() {
	// Configure structured logging with an explicit handler so every line
	// carries an RFC3339 timestamp (the package default does not guarantee
	// one). Installed before anything else so the startup banner and any
	// config-load failure are timestamped too. Level: BACKEND_LOG_LEVEL.
	logLevel := parseLogLevel(envDefault("BACKEND_LOG_LEVEL", "info"))
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))
	slog.Info("starting peeq", "version", version.Version)
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}
```

Replace it with:

```go
func main() {
	// Configure structured logging with an explicit handler so every line
	// carries an RFC3339 timestamp (the package default does not guarantee
	// one). Installed before anything else so the startup banner and any
	// config-load failure are timestamped too. Level: BACKEND_LOG_LEVEL.
	logLevel := parseLogLevel(envDefault("BACKEND_LOG_LEVEL", "info"))
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	// One-shot CLI subcommands (e.g. the TubeArchivist migration) run instead
	// of the server. With no arguments — which is how the container starts —
	// this is a no-op and the server boots exactly as before.
	if dispatchSubcommand() {
		return
	}

	slog.Info("starting peeq", "version", version.Version)
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/jan/localgit/vark-ta-import/backend && go test ./cmd/peeq/ -v
```

Expected: PASS — the new tests plus the existing `main_test.go` and `jsruntime_boot_test.go`.

> If `TestRunImportChannels_requiresURLAndToken` produces `flag` usage output on stderr, that is expected and harmless — `flag.ContinueOnError` prints usage before returning the error.

- [ ] **Step 5: Run the whole backend suite and build**

```bash
cd /Users/jan/localgit/vark-ta-import/backend && go build ./... && go test ./...
```

Expected: build succeeds; all packages PASS.

- [ ] **Step 6: Verify the server still boots with no arguments**

```bash
cd /Users/jan/localgit/vark-ta-import/backend && go run ./cmd/peeq --help 2>&1 | head -5
```

Expected: the normal server startup path (a config error about a missing setting is fine — what matters is that it does **not** print subcommand usage, proving unknown arguments fall through).

- [ ] **Step 7: Commit**

```bash
cd /Users/jan/localgit/vark-ta-import
git add backend/cmd/peeq/importcmd.go backend/cmd/peeq/importcmd_test.go backend/cmd/peeq/main.go
git commit -m "feat(cmd): add import-ta-channels subcommand

Runs as 'docker compose run --rm peeq import-ta-channels' with no
Containerfile change, since the container ENTRYPOINT is already the peeq
binary. Unknown arguments fall through to the normal server boot, so the
argument-less container start is unaffected.

Opens the database directly, so the server must be stopped while it runs.
DB path comes from config rather than a flag so it always targets the
same database the server uses."
```

---

### Task 6: Document the migration

**Files:**
- Modify: `docs/manual-verification.md`

**Interfaces:**
- Consumes: the subcommand from Task 5.
- Produces: nothing.

**Background:** This flow cannot be covered by automated tests — it needs a live TubeArchivist instance, which `AGENTS.md` forbids tests from contacting. `docs/manual-verification.md` is where peeq records exactly these flows.

- [ ] **Step 1: Read the existing file to match its structure**

```bash
cd /Users/jan/localgit/vark-ta-import && cat docs/manual-verification.md
```

Match the heading level and tone of the existing entries.

- [ ] **Step 2: Append the section**

Append to `docs/manual-verification.md`, adjusting heading depth to match the file:

````markdown
## TubeArchivist import — Phase A (subscriptions)

Requires a live TubeArchivist instance, so it cannot be covered by automated
tests. Get an API token from TubeArchivist's settings UI, or
`GET /api/appsettings/token/`.

**1. Survey — writes nothing.**

```bash
docker compose run --rm peeq import-ta-channels \
  --ta-url http://tubearchivist:8000 \
  --ta-token "$TA_TOKEN" \
  --dry-run
```

Check:
- The subscription count matches TubeArchivist's own subscribed-channel list.
- The "Skipped" count is plausible. These are channels TubeArchivist knows only
  because a video was downloaded from them once; they are deliberately not
  imported.
- Any inactive channels listed are ones you recognise as dead.

**2. Real run, with peeq stopped.**

```bash
docker compose stop peeq
docker compose run --rm peeq import-ta-channels \
  --ta-url http://tubearchivist:8000 \
  --ta-token "$TA_TOKEN"
docker compose start peeq
```

peeq must be stopped because the subcommand writes to the same SQLite database.

**3. Verify in the UI.**

- The Channels page lists the imported channels.
- Every one shows autodownload **off**.
- Names came across. Handles are blank — expected, TubeArchivist does not store
  them.

**4. Verify the baseline, after the first scheduled scan.**

The pending queue must **not** fill with each channel's back catalogue. peeq's
first scan of a channel baselines it, marking existing videos as seen. A
flooded pending queue means baselining did not happen and should be
investigated before Phase B.

**5. Expect the list to shrink.**

Inactive channels were imported deliberately. Over the following days peeq's
auto-unsubscribe will scan them, classify them as `deleted`, and unsubscribe
them. That is working as intended, not data loss.
````

- [ ] **Step 3: Commit**

```bash
cd /Users/jan/localgit/vark-ta-import
git add docs/manual-verification.md
git commit -m "docs: manual verification for TA import Phase A

Needs a live TubeArchivist instance, so it cannot be automated. Records
the baseline check explicitly -- a flooded pending queue after the first
scan means baselining failed and must be resolved before Phase B."
```

---

### Task 7: Full verification and PR

**Files:** none modified.

- [ ] **Step 1: Run the full test suite**

```bash
cd /Users/jan/localgit/vark-ta-import && make test
```

Expected: all packages PASS.

- [ ] **Step 2: Run the frontend tests**

Nothing in this plan touches the UI, but CI runs them and the branch must be green.

```bash
cd /Users/jan/localgit/vark-ta-import && make fe-test
```

Expected: PASS.

- [ ] **Step 3: Check the merge, not just the branch**

CI tests the merge result, so a green branch can still fail CI if master moved.

```bash
cd /Users/jan/localgit/vark-ta-import && git fetch origin && git log --oneline HEAD..origin/master
```

If commits are listed, rebase onto them and re-run `make test` before pushing.

- [ ] **Step 4: Push and open the PR**

```bash
cd /Users/jan/localgit/vark-ta-import && git push -u origin feat/tubearchivist-import
```

Then open a PR against **master** of `trick77/peeq`. Do not push directly to master — it is protected and requires a PR plus green CI.

- [ ] **Step 5: Confirm CI is green**

Wait for all required checks before merging.

---

## Notes for the implementer

**What Phase A deliberately does not do.** No videos, no media files, no thumbnails, no subtitles, no summaries. Those are Phase B. If you find yourself reaching for `videos.Store` or a file copy, stop — you are building the wrong phase.

**The one decision most likely to look wrong but is not.** Inactive (dead) channels are imported as subscriptions on purpose. It looks like importing garbage. The reasoning: the operator asked for their full subscription list, and peeq's own auto-unsubscribe will clean up the dead entries within days. Reporting them at import time is how they find out what they lost.

**Why `filter=subscribed` is not optional.** TubeArchivist writes a channel document for every video in the library, including one-off downloads from channels the user never followed. Dropping the filter would create subscriptions for all of them, and peeq would then scan every one on a schedule indefinitely. There is a defensive check in `ImportChannels` as well as the server-side filter; keep both.
