package videos

import (
	"context"
	"sort"
	"testing"
)

// Tests for store_summary.go: the summarize/classify pipeline's writes and
// the classifier's unclassified sweep.

func TestAddChatUsage_accumulatesAcrossRuns(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v1", URL: "u-v1", DurationSeconds: 100}); err != nil {
		t.Fatal(err)
	}

	// A fresh row is unaccounted, not zero-cost. The distinction is what stops
	// the panel claiming an old video was free.
	got, err := s.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.ChatUsage.Empty() {
		t.Fatalf("new row already has usage: %+v", got.ChatUsage)
	}

	// Two runs, standing in for an attempt that failed and the retry that
	// succeeded. Both spent real tokens, so both must be counted.
	first := ChatUsage{PromptTokens: 1000, CachedTokens: 200, CompletionTokens: 300, CostNanoUSD: 138_000}
	second := ChatUsage{PromptTokens: 500, CachedTokens: 100, CompletionTokens: 50, CostNanoUSD: 44_000}
	for _, u := range []ChatUsage{first, second} {
		if err := s.AddChatUsage("v1", u); err != nil {
			t.Fatal(err)
		}
	}

	got, err = s.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	want := ChatUsage{PromptTokens: 1500, CachedTokens: 300, CompletionTokens: 350, CostNanoUSD: 182_000}
	if got.ChatUsage != want {
		t.Fatalf("usage = %+v, want %+v (the second run overwrote rather than added)", got.ChatUsage, want)
	}
	if got.ChatUsage.Empty() {
		t.Fatal("Empty() true on an accounted row")
	}
}

func TestSetCategoryAndListByCategory(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v-ai", URL: "u-v-ai", DurationSeconds: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDownloaded("v-ai", DownloadedResult{MediaPath: "/m/v-ai.mp4"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(Video{ID: "v-news", URL: "u-v-news", DurationSeconds: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDownloaded("v-news", DownloadedResult{MediaPath: "/m/v-news.mp4"}); err != nil {
		t.Fatal(err)
	}

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
	ai, err := s.List(ListOptions{Filter: "all", Category: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ai) != 1 || ai[0].ID != "v-ai" {
		t.Fatalf("List all/ai = %v, want [v-ai]", ai)
	}

	// Empty / "all" category => no constraint.
	all, err := s.List(ListOptions{Filter: "all", Category: ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("List all/'' returned %d, want 2", len(all))
	}
}

// TestNextUnclassified_picksAnySummarizedUncategorized covers the two
// conditions the idle classify sweep depends on: a video is a candidate when it
// is still uncategorized and actually has a summary to classify from (the
// no-transcript case must stay out of the sweep).
//
// Status is deliberately NOT a condition. Classification reads a title and a
// summary, never the media file, so a tombstoned video — media reclaimed, row
// and summary kept, still listed and still filtered by category — is as
// classifiable as any other and must not be stranded on whatever enum existed
// when it was archived.
func TestNextUnclassified_picksAnySummarizedUncategorized(t *testing.T) {
	s := newTestStore(t)

	// Given: two candidates that differ only in status, plus one disqualified
	// row per real condition.
	seed := []struct {
		id, status, summary, category, createdAt string
	}{
		{"v-tombstoned", "tombstoned", "A summary.", "uncategorized", "2026-07-01"},
		{"v-candidate", "downloaded", "A summary.", "uncategorized", "2026-07-02"},
		{"v-classified", "downloaded", "A summary.", "ai", "2026-07-03"},
		{"v-no-summary", "downloaded", "", "uncategorized", "2026-07-04"},
	}
	for _, v := range seed {
		seedVideo(t, s, Video{ID: v.id, URL: "https://youtu.be/" + v.id, Status: v.status, CreatedAt: v.createdAt})
		if v.summary != "" {
			if err := s.SetSummaryText(v.id, v.summary); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.SetCategory(v.id, v.category); err != nil {
			t.Fatal(err)
		}
	}

	// When/Then: only the candidate is returned.
	got, err := s.NextUnclassified(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "v-candidate" {
		t.Fatalf("NextUnclassified = %v, want v-candidate", got)
	}
	if got.Summary != "A summary." {
		t.Fatalf("candidate summary = %q, want it loaded for the classify call", got.Summary)
	}

	// And: skipping it falls through to the tombstoned row — status is not a
	// filter — and only then does the backlog empty, rather than a
	// disqualified row being offered.
	got, err = s.NextUnclassified([]string{"v-candidate"})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "v-tombstoned" {
		t.Fatalf("NextUnclassified(skip candidate) = %v, want v-tombstoned", got)
	}
	got, err = s.NextUnclassified([]string{"v-candidate", "v-tombstoned"})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("NextUnclassified(skip both) = %v, want nil", got)
	}
}

// TestNextUnclassified_newestFirstAndSkipsMany asserts ordering and that the
// skip list works with more than one entry (the IN-clause placeholder build).
func TestNextUnclassified_newestFirstAndSkipsMany(t *testing.T) {
	s := newTestStore(t)

	for _, id := range []struct{ id, createdAt string }{
		{"v-old", "2026-07-01"}, {"v-mid", "2026-07-02"}, {"v-new", "2026-07-03"},
	} {
		seedVideo(t, s, Video{ID: id.id, URL: "https://youtu.be/" + id.id, Status: "downloaded", CreatedAt: id.createdAt})
		if err := s.SetSummaryText(id.id, "A summary."); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.NextUnclassified(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "v-new" {
		t.Fatalf("NextUnclassified = %v, want the newest (v-new)", got)
	}

	got, err = s.NextUnclassified([]string{"v-new", "v-mid"})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "v-old" {
		t.Fatalf("NextUnclassified(skip 2) = %v, want v-old", got)
	}
}

// TestNextUnclassified_errorsOnClosedDB asserts a query failure is reported to
// the caller rather than a nil video masquerading as "backlog empty" — which
// would silently retire the classify sweep for the rest of the process.
func TestNextUnclassified_errorsOnClosedDB(t *testing.T) {
	s := newTestStore(t)
	if err := s.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if _, err := s.NextUnclassified(nil); err == nil {
		t.Fatal("expected an error querying against a closed db")
	}
}

// TestSetCategoryIfUnset_guardsAManualPick is the whole reason the guarded
// write exists: both classifier paths decide to classify from a row read
// before a slow LLM call, so the write must re-check rather than trust that
// decision.
func TestSetCategoryIfUnset_guardsAManualPick(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v1", URL: "https://youtu.be/v1", Status: "downloaded"})

	// Unset: the classifier's write lands.
	applied, err := s.SetCategoryIfUnset("v1", "ai")
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("applied = false, want the write to land on an uncategorized row")
	}

	// Already set — the state a manual pick leaves behind: the write is a
	// no-op and says so, rather than silently overwriting the human.
	applied, err = s.SetCategoryIfUnset("v1", "gaming")
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("applied = true, want the guard to refuse an already-set row")
	}
	got, err := s.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != "ai" {
		t.Fatalf("category = %q, want the existing value kept", got.Category)
	}

	// SetCategory itself stays unconditional: the user is allowed to overwrite
	// the model, never the other way round.
	if err := s.SetCategory("v1", "gaming"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get("v1")
	if got.Category != "gaming" {
		t.Fatalf("category = %q, want gaming — a manual write must not be guarded", got.Category)
	}
}

// TestSetCategory_maintainsTheManualFlag pins the rule migration 0004 depends
// on: a real category is the human speaking and survives a bulk reset, while a
// reset to 'uncategorized' (Re-summarize) hands the video back to the
// classifier and must therefore clear the flag too.
func TestSetCategory_maintainsTheManualFlag(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v1", URL: "https://youtu.be/v1", Status: "downloaded"})
	if err := s.SetSummaryText("v1", "A cycling video."); err != nil {
		t.Fatal(err)
	}

	if got := categoryManual(t, s, "v1"); got != 0 {
		t.Fatalf("category_manual = %d on a fresh row, want 0", got)
	}
	if err := s.SetCategory("v1", "sports"); err != nil {
		t.Fatal(err)
	}
	if got := categoryManual(t, s, "v1"); got != 1 {
		t.Fatalf("category_manual = %d after a manual pick, want 1", got)
	}

	// Flagged and uncategorized at once cannot happen through the UI (the
	// picker has no "clear" entry), but the guard is what makes that a
	// guarantee rather than a convention, so exercise it directly.
	if _, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET category = ? WHERE id = 'v1'`, UncategorizedCategory); err != nil {
		t.Fatal(err)
	}
	applied, err := s.SetCategoryIfUnset("v1", "gaming")
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("applied = true, want the classifier refused on a flagged row")
	}

	// Re-summarize: back to the classifier, flag cleared, and the idle sweep
	// can see it again.
	if err := s.SetCategory("v1", UncategorizedCategory); err != nil {
		t.Fatal(err)
	}
	if got := categoryManual(t, s, "v1"); got != 0 {
		t.Fatalf("category_manual = %d after a reset, want 0", got)
	}
	next, err := s.NextUnclassified(nil)
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.ID != "v1" {
		t.Fatalf("NextUnclassified = %v, want v1 back in the backlog", next)
	}
	applied, err = s.SetCategoryIfUnset("v1", "sports")
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("applied = false, want the classifier's write to land once the flag is clear")
	}
}

// TestClearSummary_wipesTheAnalysisButNotTheStatus asserts ClearSummary is the
// exact counterpart of SetSummary: it removes the three artifacts and the error
// text, and deliberately leaves summary_status for the caller to set, since the
// resulting state differs (pending for a re-summarize, no_transcript for a
// track that turned out to carry no speech).
func TestClearSummary_wipesTheAnalysisButNotTheStatus(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Video{ID: "v1", URL: "u1"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetSummary("v1", "prose", `[{"ts":0}]`, `[{"ts":1}]`); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	if err := s.SetSummaryStatus("v1", "error", "boom"); err != nil {
		t.Fatalf("set status: %v", err)
	}

	if err := s.ClearSummary("v1"); err != nil {
		t.Fatalf("clear summary: %v", err)
	}

	got, err := s.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if got.Summary != "" || got.Chapters != "" || got.KeyPoints != "" {
		t.Fatalf("expected the artifacts wiped, got summary=%q chapters=%q key_points=%q",
			got.Summary, got.Chapters, got.KeyPoints)
	}
	if got.SummaryError != "" {
		t.Fatalf("expected the stale error cleared, got %q", got.SummaryError)
	}
	if got.SummaryStatus != "error" {
		t.Fatalf("summary_status = %q, want it left for the caller to set", got.SummaryStatus)
	}
}

// TestClearSummary_errorsOnClosedDB asserts a failed wipe is reported rather
// than swallowed — a caller that thinks it cleared the summary but did not
// would leave the resumable worker skipping the summary step forever.
func TestClearSummary_errorsOnClosedDB(t *testing.T) {
	s := newTestStore(t)
	if err := s.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	if err := s.ClearSummary("v1"); err == nil {
		t.Fatal("expected an error clearing against a closed db")
	}
}

// TestCategoryResetSetTellsResetsFromRemaps pins what categoryResets counts as
// a bulk reclassification, because both ways of getting it wrong are silent.
//
// Too greedy and a targeted remap — which names 'uncategorized' in its WHERE
// but clears nothing — gets dragged into TestResetSetMatchesTheSweep and fails
// a correct migration, which teaches whoever hits it to weaken the scan. Too
// strict and a real reset written with different spacing drops out of the
// assertion entirely, and the reset that erases data with no path back is the
// one nobody checked.
func TestCategoryResetSetTellsResetsFromRemaps(t *testing.T) {
	for _, tc := range []struct {
		name string
		stmt string
		want bool
	}{
		{"the real 0004/0015 statement", `UPDATE videos SET category = 'uncategorized' WHERE summary <> '' AND category_manual = 0`, true},
		{"no spaces around the equals", `UPDATE videos SET category='uncategorized' WHERE summary <> ''`, true},
		{"lowercased keywords", `update videos set category = 'uncategorized'`, true},
		{"remap OUT of uncategorized", `UPDATE videos SET category = 'science' WHERE category = 'uncategorized'`, false},
		{"another column entirely", `UPDATE videos SET summary = '' WHERE category = 'uncategorized'`, false},
		{"unrelated update", `UPDATE videos SET category_manual = 0`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := categoryResetSet.MatchString(tc.stmt); got != tc.want {
				t.Fatalf("matched=%v want %v for:\n%s", got, tc.want, tc.stmt)
			}
		})
	}
}

// TestResetSetMatchesTheSweep pins every bulk-reclassification migration to the
// query that is supposed to undo it. Such a migration clears categories in bulk
// on the promise that the summarize worker's idle sweep re-classifies whatever
// it cleared; if the two predicates ever drift, the difference is not a stale
// category, it is data erased with no path back — which is exactly the bug this
// pairing was introduced to prevent.
//
// So rather than restate the rule, this reads the real UPDATE out of the real
// migration files, runs each over a table seeded with every row shape peeq can
// produce, and asserts the rows it cleared are exactly the rows
// NextUnclassified will offer. Same trick as ui/src/enumsync.test.ts, which
// reads category.go instead of mirroring it.
//
// It covers every migration carrying a reset, not just 0004, because a second
// one exists (0015, for the category hints) and a third will follow the next
// time the enum or the classify prompt changes enough to invalidate past
// answers. Copying a reset into a new file must not silently opt it out of the
// only check that says the reset is reversible.
func TestResetSetMatchesTheSweep(t *testing.T) {
	resets := categoryResets(t)
	names := make([]string, 0, len(resets))
	for name := range resets {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			s := newTestStore(t)

			// Given: one row per shape. 'category' is the row's category BEFORE
			// the reset, and 'uncategorized' here is not filler — a
			// no-transcript video really does sit at the column default in
			// production, and it is the shape that catches a "cleared" set
			// computed as "uncategorized afterwards".
			seeds := []struct {
				id, status, summary, summaryStatus, category string
				manual                                       bool
			}{
				{"downloaded", "downloaded", "a summary", "done", "entertainment", false},
				{"tombstoned", "tombstoned", "a summary", "done", "history", false},         // media reclaimed, summary kept
				{"notranscript", "downloaded", "", "no_transcript", "uncategorized", false}, // nothing to classify from
				{"handpicked", "downloaded", "", "no_transcript", "gaming", true},           // the picker's whole reason to exist
				{"queued", "queued", "", "pending", "uncategorized", false},
				{"errored", "error", "a summary", "error", "news", false},
				{"handpicked-summarized", "downloaded", "a summary", "done", "ai", true},
			}
			for _, sd := range seeds {
				seedVideo(t, s, Video{ID: sd.id, URL: "https://youtu.be/" + sd.id, Status: sd.status})
				if _, err := s.db.ExecContext(context.Background(),
					`UPDATE videos SET summary = ?, summary_status = ?, category = ?, category_manual = ? WHERE id = ?`,
					sd.summary, sd.summaryStatus, sd.category, boolToInt(sd.manual), sd.id); err != nil {
					t.Fatal(err)
				}
			}
			before := idsWithCategory(t, s, UncategorizedCategory)

			// When: the migration's own UPDATE runs. The test DB is already
			// migrated, so replaying just this statement is what an upgrade
			// does to the data.
			if _, err := s.db.ExecContext(context.Background(), resets[name]); err != nil {
				t.Fatalf("replay %s reset: %v", name, err)
			}

			// Then: the set the reset CHANGED — not the set that reads
			// 'uncategorized' now, which would also count rows that were
			// already there and could never be reclassified — equals the set
			// the sweep offers.
			cleared := minusSet(idsWithCategory(t, s, UncategorizedCategory), before)
			reachable := []string{}
			for i := 0; i <= len(seeds); i++ {
				v, err := s.NextUnclassified(reachable)
				if err != nil {
					t.Fatal(err)
				}
				if v == nil {
					break
				}
				reachable = append(reachable, v.ID)
				if i == len(seeds) {
					// Bounded on purpose: an unbounded drain turns a broken skip
					// clause into a hung suite instead of a failed assertion.
					t.Fatalf("NextUnclassified still returning rows after %d turns: %v", i+1, reachable)
				}
			}
			if !sameSet(cleared, reachable) {
				t.Fatalf("reset cleared %v but the sweep can reach %v — a row in the difference is either\n"+
					"erased with no way back, or left on the pre-expansion enum forever", cleared, reachable)
			}
			// And the rule both sides are meant to encode, stated once so a
			// mutual drift (both sides wrong the same way) still fails.
			if !sameSet(cleared, []string{"downloaded", "tombstoned", "errored"}) {
				t.Fatalf("cleared %v, want every row that has a summary and is not a hand pick, and only those", cleared)
			}
			// And the hand picks are untouched — the column's entire purpose.
			for _, id := range []string{"handpicked", "handpicked-summarized"} {
				var got string
				if err := s.db.QueryRowContext(context.Background(),
					`SELECT category FROM videos WHERE id = ?`, id).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got == UncategorizedCategory {
					t.Fatalf("%s was cleared; a flagged row must survive a bulk reset", id)
				}
			}
		})
	}
}
