// Package videos: category.go defines the fixed video-category enum (the
// authority; the TS side mirrors it) plus reply normalization. AI is a
// first-class category, deliberately split from general technology.
package videos

import (
	"strings"
	"unicode"
)

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
	{"software", "Software Engineering"},
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

// ClassifiableCategories is Categories minus the uncategorized fallback: the
// set a classifier is allowed to choose from. 'uncategorized' is a state the
// app assigns (not yet classified, or the call failed), never an answer the
// model may give — offering it in the prompt just invites the punt.
func ClassifiableCategories() []Category {
	out := make([]Category, 0, len(Categories)-1)
	for _, c := range Categories {
		if c.ID != UncategorizedCategory {
			out = append(out, c)
		}
	}
	return out
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

// NormalizeCategory maps a raw model reply to a valid id, repairing the
// wrappers models habitually add rather than discarding an answer that is
// actually correct: "**ai**", "Category: ai", a fenced reply, or the display
// label "Science & Research" all used to fall through to uncategorized.
//
// The chain is, in order: exact id after cleaning; exact label after cleaning;
// last valid id among the reply's word tokens. UncategorizedCategory is
// returned only when none of those match, i.e. the reply really is junk.
func NormalizeCategory(reply string) string {
	s := cleanReply(reply)
	if ValidCategory(s) {
		return s
	}
	for _, c := range Categories {
		if cleanReply(c.Label) == s {
			return c.ID
		}
	}
	// Token scan, for a reply that buries the id in prose. The LAST valid id
	// wins, not the first: models that pad an answer put the verdict at the end
	// ("this is not tech, it is history") and echo the option list at the start
	// ("choosing from ai, tech, science: history"), so taking the first token
	// would reliably pick the wrong one in both shapes.
	//
	// 'uncategorized' is deliberately not a candidate, so a hedging answer
	// ("ai, though it could be uncategorized") still lands on the real
	// category; a reply naming no real id at all falls through to the fallback.
	found := UncategorizedCategory
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if tok != UncategorizedCategory && ValidCategory(tok) {
			found = tok
		}
	}
	return found
}

// cleanReply lowercases and strips the decoration a model wraps an answer in:
// code fences, backticks, markdown emphasis, quotes, a leading "category:"
// prefix, and surrounding punctuation.
func cleanReply(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "```", "")
	s = strings.Trim(s, " \t\n\r\"'`*_.,:;!")
	s = strings.TrimSpace(strings.TrimPrefix(s, "category:"))
	return strings.Trim(s, " \t\n\r\"'`*_.,:;!")
}
