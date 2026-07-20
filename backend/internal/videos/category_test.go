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
