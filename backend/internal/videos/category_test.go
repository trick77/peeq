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

func TestClassifiableCategoriesExcludeTheFallback(t *testing.T) {
	cs := ClassifiableCategories()
	if len(cs) != len(Categories)-1 {
		t.Fatalf("want %d classifiable categories, got %d", len(Categories)-1, len(cs))
	}
	for _, c := range cs {
		if c.ID == UncategorizedCategory {
			t.Fatal("uncategorized must not be offered to the classifier")
		}
		if c.Label == "" {
			t.Fatalf("category %q has no label; the prompt needs it", c.ID)
		}
	}
}

func TestNormalizeCategory(t *testing.T) {
	cases := map[string]string{
		// exact ids, the pre-existing behavior
		"ai":           "ai",
		"AI":           "ai",
		"  software  ": "software",
		"\"news\".":    "news",
		"`gaming`":     "gaming",

		// decoration models add around an otherwise correct answer
		"**ai**":            "ai",
		"```\nscience\n```": "science",
		"Category: ai":      "ai",
		"category: TECH":    "tech",

		// display labels
		"Science & Research":   "science",
		"engineering & making": "engineering",

		// the id buried in prose, including a hedged answer that names a real
		// category alongside the fallback
		"The best fit is history.":      "history",
		"uncategorized, though ai fits": "ai",

		// the last id wins: models pad the verdict to the end and echo the
		// option list at the start, so taking the first token would pick the
		// negated or merely-listed category in both of these
		"This is not tech; it is best classified as history.": "history",
		"Choosing from ai, tech, software, science: history":  "history",

		// genuinely unusable replies
		"":              "uncategorized",
		"not-a-real-id": "uncategorized",
		"uncategorized": "uncategorized",
	}
	for in, want := range cases {
		if got := NormalizeCategory(in); got != want {
			t.Fatalf("NormalizeCategory(%q) = %q, want %q", in, got, want)
		}
	}
}
