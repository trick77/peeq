package videos

import (
	"strings"
	"testing"
)

func TestCategoryIDsCoverAllAndIncludeAI(t *testing.T) {
	ids := CategoryIDs()
	if len(ids) != 23 {
		t.Fatalf("want 23 categories, got %d", len(ids))
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	// The lifestyle half exists because the classify prompt forces a choice: a
	// missing category does not yield 'uncategorized', it yields a confidently
	// wrong neighbour (a cycling channel filed under entertainment).
	for _, want := range []string{
		"ai", "uncategorized",
		"politics", "sports", "food", "travel", "automotive", "home", "arts", "music",
	} {
		if !seen[want] {
			t.Fatalf("missing id %q", want)
		}
	}
}

func TestCategoryLabelsAndHints(t *testing.T) {
	for _, c := range Categories {
		if c.Label == "" {
			t.Fatalf("category %q has no label", c.ID)
		}
		// Hints are rendered one category per line in the classify prompt, so a
		// newline inside one would split a category into two bogus entries.
		if strings.ContainsAny(c.Hint, "\n\r") {
			t.Fatalf("category %q hint contains a newline", c.ID)
		}
	}
	if hintOf(t, UncategorizedCategory) != "" {
		t.Fatal("the fallback is never offered to the classifier; a hint on it is dead weight")
	}
	// The pairs the enum split apart are exactly the ones the model needs told
	// apart, so they must carry their disambiguation.
	for _, id := range []string{"music", "entertainment", "politics", "news", "automotive", "home"} {
		if hintOf(t, id) == "" {
			t.Fatalf("category %q needs a hint: it blurs into its neighbour without one", id)
		}
	}
}

func hintOf(t *testing.T, id string) string {
	t.Helper()
	for _, c := range Categories {
		if c.ID == id {
			return c.Hint
		}
	}
	t.Fatalf("no such category %q", id)
	return ""
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
		"Science & Research":     "science",
		"engineering & making":   "engineering",
		"Sports & Fitness":       "sports",
		"Automotive & Transport": "automotive",

		// the added ids, including the two that used to share a bucket
		"sports":        "sports",
		"food":          "food",
		"travel":        "travel",
		"automotive":    "automotive",
		"home":          "home",
		"arts":          "arts",
		"music":         "music",
		"politics":      "politics",
		"entertainment": "entertainment",

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
