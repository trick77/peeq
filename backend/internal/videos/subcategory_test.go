package videos

import (
	"slices"
	"strings"
	"testing"
)

func TestSubcategoriesCoverEveryClassifiableCategory(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Subcategories {
		if seen[s.Key()] {
			t.Fatalf("duplicate subcategory %q", s.Key())
		}
		seen[s.Key()] = true
		if !ValidCategory(s.Category) || s.Category == UncategorizedCategory {
			t.Fatalf("subcategory %q hangs off %q, which is not a classifiable category", s.Key(), s.Category)
		}
		if s.Label == "" {
			t.Fatalf("subcategory %q has no label", s.Key())
		}
		// Rendered one per line in the classify prompts, like category hints.
		if strings.ContainsAny(s.Hint, "\n\r") || strings.ContainsAny(s.ID, "/ ") {
			t.Fatalf("subcategory %q: newline in hint or separator in id", s.Key())
		}
	}
	// The forced-choice lesson from the category enum: without an escape the
	// model files a misfit under a confidently wrong neighbour.
	for _, c := range ClassifiableCategories() {
		subs := SubcategoriesFor(c.ID)
		if len(subs) < 3 {
			t.Fatalf("category %q has %d subcategories, want a real split", c.ID, len(subs))
		}
		if subs[len(subs)-1].ID != OtherSubcategory {
			t.Fatalf("category %q must end in %q", c.ID, OtherSubcategory)
		}
	}
	if len(SubcategoriesFor(UncategorizedCategory)) != 0 {
		t.Fatal("the fallback category carries no subcategories")
	}
}

func TestSubcategoryLookups(t *testing.T) {
	if !ValidSubcategory("sports/cycling") || !ValidSubcategory("ai/building") {
		t.Fatal("known keys must be valid")
	}
	for _, bad := range []string{"", "cycling", "sports/", "sports/knitting", "uncategorized/other"} {
		if ValidSubcategory(bad) {
			t.Fatalf("%q must not be valid", bad)
		}
	}
	if got := SubcategoryLabel("sports/cycling"); got != "Cycling" {
		t.Fatalf("label = %q", got)
	}
	if got := SubcategoryLabel("nope/nope"); got != "nope/nope" {
		t.Fatalf("unknown key label = %q, want the key back", got)
	}
	keys := SubcategoryKeys()
	if len(keys) != len(Subcategories) || keys[0] != Subcategories[0].Key() {
		t.Fatal("SubcategoryKeys must list every key in order")
	}
}

func TestNormalizeSubcategories(t *testing.T) {
	ai := SubcategoriesFor("ai")
	all := Subcategories
	cases := []struct {
		name    string
		reply   string
		allowed []Subcategory
		want    []string
	}{
		{"json short ids", `{"subcategories":["building","models"]}`, ai, []string{"ai/building", "ai/models"}},
		{"json full keys", `{"subcategories":["ai/infra"]}`, ai, []string{"ai/infra"}},
		{"cap and dedupe", `{"subcategories":["building","models","building","infra","society"]}`, ai,
			[]string{"ai/building", "ai/models", "ai/infra"}},
		{"drops unknown and foreign", `{"subcategories":["knitting","sports/cycling","business"]}`, ai,
			[]string{"ai/business"}},
		{"case and whitespace", `{"subcategories":[" Building "]}`, ai, []string{"ai/building"}},
		{"fenced json", "```json\n{\"subcategories\":[\"society\"]}\n```", ai, []string{"ai/society"}},
		{"prose around json", "Here you go: {\"subcategories\":[\"infra\"]} — done.", ai, []string{"ai/infra"}},
		{"cross-category keys", `{"subcategories":["sports/cycling","tech/wearables"]}`, all,
			[]string{"sports/cycling", "tech/wearables"}},
		// A short id shared by several categories (other, industry, film) is
		// ambiguous without the category, so it only counts as a full key.
		{"ambiguous short id across categories", `{"subcategories":["other","cycling"]}`, all,
			[]string{"sports/cycling"}},
		{"empty list is an answer", `{"subcategories":[]}`, ai, nil},
	}
	for _, c := range cases {
		got, ok := NormalizeSubcategories(c.reply, c.allowed)
		if !ok || !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v ok=%v, want %v", c.name, got, ok, c.want)
		}
	}

	// Not answers: persisting any of these as "nothing fits" would stop the
	// sweep asking again. An empty reply is what a call that out-thought its
	// cap returns, with no error.
	for name, reply := range map[string]string{
		"empty":      "",
		"prose":      "I cannot decide",
		"cut":        `{"subcategories":["build`,
		"no list":    `{"category":"ai"}`,
		"only junk":  `{"subcategories":["knitting","sports/cycling"]}`,
		"wrong type": `{"subcategories":"building"}`,
	} {
		if got, ok := NormalizeSubcategories(reply, ai); ok || got != nil {
			t.Errorf("%s: got %v ok=%v, want not an answer", name, got, ok)
		}
	}
}

func TestLookupSubcategory(t *testing.T) {
	s, ok := LookupSubcategory("sports/cycling")
	if !ok || s.Category != "sports" || s.Label != "Cycling" {
		t.Fatalf("got %+v %v", s, ok)
	}
	if _, ok := LookupSubcategory("sports"); ok {
		t.Fatal("a category is not a subcategory key")
	}
}
