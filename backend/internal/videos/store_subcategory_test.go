package videos

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// classifiedVideo seeds a downloaded video with a summary and a category: what
// the subcategory sweep looks for.
func classifiedVideo(t *testing.T, s *Store, id, channelID, category string) {
	t.Helper()
	seedVideo(t, s, Video{ID: id, URL: "u-" + id, Title: "T " + id, ChannelID: channelID, DurationSeconds: 100, Status: StatusDownloaded})
	if err := s.SetSummary(id, "summary of "+id, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCategory(id, category); err != nil {
		t.Fatal(err)
	}
}

func TestSetSubcategoriesIfCategory_writesInOrderAndGuardsTheCategory(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	classifiedVideo(t, s, "v1", "UCa", "ai")

	ok, err := s.SetSubcategoriesIfCategory(ctx, "v1", "ai", []string{"ai/models", "ai/building"})
	if err != nil || !ok {
		t.Fatalf("write = %v, %v", ok, err)
	}
	got, err := s.SubcategoriesCtx(ctx, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"ai/models", "ai/building"}) {
		t.Fatalf("got %v: primary must stay first", got)
	}

	// A classify call takes seconds; the user may have re-picked the category
	// meanwhile. Labels chosen under the old one must not land.
	ok, err = s.SetSubcategoriesIfCategory(ctx, "v1", "science", []string{"science/physics"})
	if err != nil || ok {
		t.Fatalf("stale write = %v, %v; want refused", ok, err)
	}
	got, _ = s.SubcategoriesCtx(ctx, "v1")
	if !slices.Equal(got, []string{"ai/models", "ai/building"}) {
		t.Fatalf("stale write changed labels: %v", got)
	}
}

func TestSetCategory_dropsSubcategoriesAndHandsTheVideoBack(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	classifiedVideo(t, s, "v1", "UCa", "ai")
	if _, err := s.SetSubcategoriesIfCategory(ctx, "v1", "ai", []string{"ai/models"}); err != nil {
		t.Fatal(err)
	}
	if v, err := s.NextUnsubcategorized(ctx, nil); err != nil || v != nil {
		t.Fatalf("classified video offered again: %v, %v", v, err)
	}

	if err := s.SetCategory("v1", "science"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.SubcategoriesCtx(ctx, "v1")
	if len(got) != 0 {
		t.Fatalf("labels of the old category survived: %v", got)
	}
	v, err := s.NextUnsubcategorized(ctx, nil)
	if err != nil || v == nil || v.ID != "v1" || v.Category != "science" {
		t.Fatalf("re-categorized video not offered: %+v, %v", v, err)
	}
}

func TestResetForReprocess_dropsSubcategories(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	classifiedVideo(t, s, "v1", "UCa", "ai")
	if _, err := s.SetSubcategoriesIfCategory(ctx, "v1", "ai", []string{"ai/models"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetForReprocess("v1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SubcategoriesCtx(ctx, "v1"); len(got) != 0 {
		t.Fatalf("labels survived a reprocess: %v", got)
	}
}

func TestNextUnsubcategorized_skipsWhatTheSweepMustNotTouch(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	classifiedVideo(t, s, "done", "UCa", "ai")
	if _, err := s.SetSubcategoriesIfCategory(ctx, "done", "ai", nil); err != nil {
		t.Fatal(err) // an empty answer still counts as answered
	}
	seedVideo(t, s, Video{ID: "uncat", URL: "u-uncat", DurationSeconds: 100, Status: StatusDownloaded})
	// Inbox reads are never categorized (summarize/worker.go), so never subcategorized.
	seedVideo(t, s, Video{ID: "inbox", URL: "u-inbox", DurationSeconds: 100})
	if err := s.SetCategory("inbox", "ai"); err != nil {
		t.Fatal(err)
	}
	classifiedVideo(t, s, "parked", "UCa", "sports")

	if v, err := s.NextUnsubcategorized(ctx, []string{"parked"}); err != nil || v != nil {
		t.Fatalf("offered %+v, %v; want nothing", v, err)
	}
	v, err := s.NextUnsubcategorized(ctx, nil)
	if err != nil || v == nil || v.ID != "parked" {
		t.Fatalf("got %+v, %v; want parked", v, err)
	}
}

func TestSubcategoriesMany(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	classifiedVideo(t, s, "a", "UCa", "ai")
	classifiedVideo(t, s, "b", "UCa", "sports")
	_, _ = s.SetSubcategoriesIfCategory(ctx, "a", "ai", []string{"ai/infra", "ai/models"})
	_, _ = s.SetSubcategoriesIfCategory(ctx, "b", "sports", []string{"sports/cycling"})

	got, err := s.SubcategoriesMany(ctx, []string{"a", "b", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got["a"], []string{"ai/infra", "ai/models"}) || !slices.Equal(got["b"], []string{"sports/cycling"}) || len(got["missing"]) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestChannelTitles_newestFirst(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	for i, d := range []string{"2026-01-01", "2026-03-01", "2026-02-01"} {
		id := string(rune('a' + i))
		seedVideo(t, s, Video{ID: id, URL: "u-" + id, Title: "T" + d, ChannelID: "UCa", PublishedAt: d, DurationSeconds: 1})
	}
	seedVideo(t, s, Video{ID: "x", URL: "u-x", Title: "other channel", ChannelID: "UCb", DurationSeconds: 1})
	got, err := s.ChannelTitles(ctx, "UCa", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"T2026-03-01", "T2026-02-01"}) {
		t.Fatalf("got %v", got)
	}
}

// The filter seeks by subcategory: it must stay on the index, never scan.
func TestSubcategoryFilterQuery_usesTheIndex(t *testing.T) {
	db := openTestDB(t)
	plan := queryPlan(t, db, `SELECT video_id FROM video_subcategories WHERE subcategory = ?`, "ai/models")
	if !strings.Contains(plan, "idx_video_subcategories_sub") {
		t.Fatalf("plan = %s", plan)
	}
}
