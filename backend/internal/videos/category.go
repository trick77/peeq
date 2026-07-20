// Package videos: category.go defines the fixed video-category enum (the
// authority; the TS side mirrors it) plus reply normalization. AI is a
// first-class category, deliberately split from general technology.
package videos

import "strings"

// Category is one entry of the fixed enum. ID is the stable machine string
// stored on videos.category; Label is display-only.
type Category struct {
	ID    string
	Label string
}

// UncategorizedCategory is the fallback id: used for no-transcript videos and
// for any classifier reply that isn't an exact enum id.
const UncategorizedCategory = "uncategorized"

// Categories is the fixed, ordered enum. Order drives the Library chip order.
var Categories = []Category{
	{"ai", "AI"},
	{"tech", "Technology & Gadgets"},
	{"software", "Software & Programming"},
	{"science", "Science & Research"},
	{"space", "Space & Astronomy"},
	{"engineering", "Engineering & Making"},
	{"business", "Business & Finance"},
	{"news", "News & Current Events"},
	{"history", "History & Culture"},
	{"health", "Health & Medicine"},
	{"nature", "Nature & Environment"},
	{"education", "Education & Tutorials"},
	{"gaming", "Gaming"},
	{"entertainment", "Entertainment & Music"},
	{"uncategorized", "Uncategorized"},
}

// CategoryIDs returns every id in Categories order.
func CategoryIDs() []string {
	ids := make([]string, len(Categories))
	for i, c := range Categories {
		ids[i] = c.ID
	}
	return ids
}

// ValidCategory reports whether id is an exact enum id.
func ValidCategory(id string) bool {
	for _, c := range Categories {
		if c.ID == id {
			return true
		}
	}
	return false
}

// NormalizeCategory maps a raw model reply to a valid id: it trims
// surrounding whitespace, quotes, backticks, and trailing punctuation, then
// lowercases. Returns the id when valid, else UncategorizedCategory.
func NormalizeCategory(reply string) string {
	s := strings.ToLower(strings.TrimSpace(reply))
	s = strings.Trim(s, " \t\n\r\"'`.,:;!")
	if ValidCategory(s) {
		return s
	}
	return UncategorizedCategory
}
