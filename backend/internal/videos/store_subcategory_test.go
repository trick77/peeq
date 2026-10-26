package videos

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// classifiedVideo seeds a downloaded video with a summary ("summary of <id>")
// and a category: what the subcategory sweep looks for.
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

func label(t *testing.T, s *Store, id, category string, keys ...string) {
	t.Helper()
	if ok, err := s.SetSubcategoriesIfCurrent(context.Background(), id, category, "summary of "+id, keys); err != nil || !ok {
		t.Fatalf("label %s: %v, %v", id, ok, err)
	}
}

func TestSetSubcategoriesIfCurrent_writesInOrderAndGuardsTheRow(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	classifiedVideo(t, s, "v1", "UCa", "ai")

	label(t, s, "v1", "ai", "ai/models", "ai/building")
	got, err := s.SubcategoriesCtx(ctx, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"ai/models", "ai/building"}) {
		t.Fatalf("got %v: primary must stay first", got)
	}

	// A classify call takes seconds. A category re-picked meanwhile, or a
	// reprocess that wrote a new summary under the same category, must not
	// receive labels chosen from the old row.
	for name, c := range map[string]struct{ category, summary string }{
		"other category": {"science", "summary of v1"},
		"other summary":  {"ai", "an older summary"},
	} {
		ok, err := s.SetSubcategoriesIfCurrent(ctx, "v1", c.category, c.summary, []string{"ai/infra"})
		if err != nil || ok {
			t.Fatalf("%s: stale write = %v, %v; want refused", name, ok, err)
		}
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
	label(t, s, "v1", "ai", "ai/models")
	if v, err := s.NextUnsubcategorized(ctx, nil); err != nil || v != nil {
		t.Fatalf("classified video offered again: %v, %v", v, err)
	}

	// Re-picking the same category is no change and keeps the labels.
	if err := s.SetCategory("v1", "ai"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SubcategoriesCtx(ctx, "v1"); len(got) != 1 {
		t.Fatalf("an unchanged category dropped the labels: %v", got)
	}

	if err := s.SetCategory("v1", "science"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.SubcategoriesCtx(ctx, "v1"); len(got) != 0 {
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
	label(t, s, "v1", "ai", "ai/models")
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
	label(t, s, "done", "ai") // an empty answer still counts as answered
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
	label(t, s, "a", "ai", "ai/infra", "ai/models")
	label(t, s, "b", "sports", "sports/cycling")

	got, err := s.SubcategoriesMany(ctx, []string{"a", "b", "missing"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got["a"], []string{"ai/infra", "ai/models"}) || !slices.Equal(got["b"], []string{"sports/cycling"}) || len(got["missing"]) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestChannelTitles_mostRecentlyRecordedFirst(t *testing.T) {
	s := New(openTestDB(t))
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		seedVideo(t, s, Video{ID: id, URL: "u-" + id, Title: "T" + id, ChannelID: "UCa", DurationSeconds: 1})
	}
	seedVideo(t, s, Video{ID: "x", URL: "u-x", Title: "other channel", ChannelID: "UCb", DurationSeconds: 1})
	got, err := s.ChannelTitles(ctx, "UCa", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"Tc", "Tb"}) {
		t.Fatalf("got %v", got)
	}
}

// Every query here runs on the cold production disk, where a videos row is
// expensive (its small columns sit behind the text ones). Pin the plans.
func TestSubcategoryQueries_stayOnIndexes(t *testing.T) {
	db := openTestDB(t)
	for name, c := range map[string]struct {
		query string
		args  []any
		want  string
	}{
		"filter": {`SELECT video_id FROM video_subcategories WHERE subcategory = ?`, []any{"ai/models"}, "idx_video_subcategories_sub"},
		"sweep":  {nextUnsubcategorizedQuery, []any{UncategorizedCategory, StatusNew}, "USING COVERING INDEX idx_videos_cards"},
		// The rowid pick sorts index entries only; the outer read seeks by rowid.
		"titles": {channelTitlesQuery, []any{"UCa", 25}, "USING COVERING INDEX idx_videos_channel_status"},
	} {
		if plan := queryPlan(t, db, c.query, c.args...); !strings.Contains(plan, c.want) || strings.Contains(plan, "SCAN videos") {
			t.Errorf("%s: plan = %s; want %s and no table scan", name, plan, c.want)
		}
	}
	if plan := queryPlan(t, db, channelTitlesQuery, "UCa", 25); !strings.Contains(plan, "USING INTEGER PRIMARY KEY (rowid=?)") {
		t.Errorf("titles: rows are not read by rowid: %s", plan)
	}
}
