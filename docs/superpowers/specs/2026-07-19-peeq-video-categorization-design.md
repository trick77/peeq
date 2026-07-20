# peeq — LLM Video Categorization + Library Filter — Design

**Date:** 2026-07-19
**Status:** Approved (design), pre-plan
**Branch (to be):** `feat/video-categorization`
**Depends on:** Phase 3 summarize/MiMo infra + Phase 3.1 (all merged to `origin/master`)

## 1. Goal

During the existing summarize job, classify each video into **exactly one** category
from a **fixed enum**, store it on the video, and let the user **filter the Library by
category**. Self-contained: it rides the summarize worker + MiMo client already in use,
adds no new external surface, and holds every existing invariant.

Folds in one deferred cleanup: the scan-classify path gains the missing `ErrNoCookie`
case, mirroring the download worker's classify (the 3.2 minor noted in the Phase 3.1b
status).

## 2. Scope

**In:** category enum (Go + TS), a `category` column on `videos`, a constrained MiMo
classify step in the summarize worker, a `category` filter in the videos store + list
API, a Library category-chip row, a per-card category badge, and the `ErrNoCookie`
scan-classify mirror.

**Out (own later slices):** API token / Chrome extension, auto-unsubscribe stale
channels, conversational RAG-QA, any re-categorization UI or backfill job (see §7).

## 3. Taxonomy (fixed enum)

14 categories + `uncategorized` fallback. AI is a **first-class** category, split out
from general technology. IDs are stable machine strings; labels are display-only.

| id | label |
|----|-------|
| `ai` | AI |
| `tech` | Technology & Gadgets |
| `software` | Software & Programming |
| `science` | Science & Research |
| `space` | Space & Astronomy |
| `engineering` | Engineering & Making |
| `business` | Business & Finance |
| `news` | News & Current Events |
| `history` | History & Culture |
| `health` | Health & Medicine |
| `nature` | Nature & Environment |
| `education` | Education & Tutorials |
| `gaming` | Gaming |
| `entertainment` | Entertainment & Music |
| `uncategorized` | Uncategorized (fallback) |

The enum is defined once in Go (the authority) and mirrored in one TS module
(`ui/src/categories.ts`) carrying `{ id, label, color }`. `ai` uses the accent
(`#d97757`); the rest use muted dot colors (scanning aid only — chips/badges otherwise
stay in the warm palette). `uncategorized` uses `--color-faint`.

## 4. Data model

Add one column, squashed **in place** into `0001_init.sql` (DB is recreated on upgrade;
there is no prod DB and no existing rows, so **no backfill**):

```sql
category TEXT NOT NULL DEFAULT 'uncategorized'
```

Rationale for in-place vs append-only: reconfirmed with the operator — same as 3.1a/3.1b,
DB-recreate on upgrade, no data to preserve. Append-only resumes when a prod DB first
exists.

The `videos.Video` struct gains a `Category string` field; the row scanner and every
`SELECT ... FROM videos` column list are updated to include it.

## 5. Backend — classify step

**Where.** In `internal/summarize`, add a `Classify(ctx, title, summary) (string, error)`
method on `Summarizer` (it already owns the `Completer`/MiMo client). The worker calls it
in `processOne` **after** `SetSummary` succeeds — the summary is the classifier's input,
so it must exist first.

**Prompt.** A single constrained completion: system prompt lists the exact allowed ids
and instructs the model to reply with **one id and nothing else**; user content is
`title` + the generated `summary` (short, cheap input — not the full transcript).

**Validation.** Trim/lowercase the reply and match it against the enum set. Anything not
an exact enum id → `uncategorized`. A classify **error never fails the job**: the summary
is already stored; log it and leave the category at its `uncategorized` default. (Contrast
with summarize errors, which do fail the job.)

**Storage.** A new `videos.Store.SetCategory(id, category string) error`, called by the
worker after a successful classify. Videos that reach `no_transcript` / tombstoned /
`error` are never classified and keep `uncategorized`.

**Invariants.** The classify call goes through the same MiMo client and the same
throttle/pause choke point as summarize — no new external path, no YouTube/subtitle call.
Tests drive it through the existing httptest MiMo fake; never a real LLM.

**SSE / phase.** No new phase event required; category is a stored field the Library
reads on its normal list refresh. (Optional: the worker may `emit` a "categorizing" phase
for parity — decide in the plan; not required.)

## 6. Backend — filter + API

- `videos.Store.List` gains an **optional category** filter, **orthogonal** to the
  existing status filter — both may apply (e.g. status=`unwatched` AND category=`ai`).
  Empty/absent category = no category constraint.
- The list endpoint accepts a `category=<id>` query param alongside the existing status
  filter param. Unknown/empty category id = ignored (returns all).
- Chip counts: the Library computes category counts client-side from the unfiltered
  `all` list (mirroring how status-chip counts already work), so the backend needs no
  per-category count endpoint.

## 7. Frontend

**Taxonomy module.** `ui/src/categories.ts` — the ordered `{ id, label, color }` list
mirroring the Go enum, plus a lookup map. `api/types` `Video` gains `category: string`.

**Library — category chip row (mockup variant A).** Below the existing status chips, a
second wrapping row of category chips: an "All categories" chip plus one chip per
category **that has ≥1 video** (empty categories hidden), each showing a muted color dot +
label + count. Selecting a category refetches the list with `category=<id>`; it is
independent of the active status chip (both dimensions apply). "All categories" clears it.

**VideoCard — pill in the meta line (mockup variant 1).** A category pill in the card's
`.by` meta line (channel · date · category), shown only when `category !== 'uncategorized'`:
a muted dot + label on a subtle bordered chip. Lives in the meta line rather than on the
thumbnail, so it never competes with the `NEW` tag, duration, or hover actions.

**Colors.** Keep the muted per-category dot colors (operator choice). Chips/badges
otherwise use the standard warm treatment; only the dot carries category color.

## 8. Cleanup — scan-classify `ErrNoCookie`

The channel-scan classify path omits an `ErrNoCookie` case that the download worker's
classify handles (race-only, self-limiting). Mirror the worker's handling so the two
classify paths agree. Small, same area, no behavior change in the common path.

## 9. Testing

- **Enum/validation:** Go unit tests for the enum set + the classify-reply validator
  (valid id passes through; unknown/empty/whitespace/case → `uncategorized`).
- **Worker:** using the httptest MiMo fake — a fake classify reply yields the stored
  category; a classify error leaves `uncategorized` **and does not fail the job** (summary
  still stored); `no_transcript` path never classifies.
- **Store:** `SetCategory` round-trips; `List` with a category filter (and status+category
  together) returns the right rows.
- **API:** list endpoint honors `category`; unknown id returns all.
- **Scan-classify:** `ErrNoCookie` case covered, mirroring the worker test.
- **UI:** Library renders the category chip row, filters on selection, and independence
  from the status chip; VideoCard renders the badge only when categorized and places it
  bottom-left. Existing fakes only.

## 10. Invariants held

No new YouTube/subtitle path; classify is MiMo-only through the existing throttle/pause
gate; all tests use fakes (httptest MiMo, no real LLM/embeddings/yt-dlp); single in-place
`0001` migration; `CGO_ENABLED=0`; `BACKEND_` env prefix; English comments; conventional
commits; work lands via a `feat/video-categorization` branch → PR to `master`.

## 11. Process

Brainstorm (this doc) → writing-plans (TDD, bite-sized) → subagent-driven-development
(fresh sonnet implementer per task; **every** returning result validated by a Fable
reviewer; fix Critical/Important before closing, record minors in the SDD ledger) →
whole-branch Fable review → finishing-a-development-branch (PR to `master`, CI green:
backend `-race` + UI).
