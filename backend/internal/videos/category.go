// Package videos: category.go defines the fixed video-category enum (the
// authority; the TS side mirrors id + order, carries its own short display
// labels and colours, and never Hint) plus reply normalization. AI is a
// first-class category, deliberately split from general technology.
package videos

import (
	"strings"
	"unicode"
)

// Category is one entry of the fixed enum. ID is the stable machine string
// stored on videos.category; Label is display-only.
//
// Hint is prompt-steering, not display copy: it is rendered into the classify
// prompt to separate categories a model would otherwise confuse (a car review
// is automotive, not tech; a workshop build is engineering, a kitchen
// renovation is home). It is deliberately NOT mirrored into the TS enum, and
// nothing in the UI may show it. Only ambiguous categories carry one.
//
// A hint is also what stops a category losing by default. An unhinted option is
// one line of prompt against a hinted one's three, so when the fit is loose the
// hinted bucket wins — which is how a video about who built the smallest nuclear
// bomb landed in politics rather than science. Any category that can be
// described in political, institutional or military vocabulary needs its own
// claim staked, or 'politics' absorbs it.
type Category struct {
	ID    string
	Label string
	Hint  string
}

// UncategorizedCategory is the fallback id: used for no-transcript videos and
// for any classifier reply that isn't an exact enum id.
const UncategorizedCategory = "uncategorized"

// Categories is the fixed, ordered enum. Order drives the Library chip order,
// so related buckets sit next to each other (politics beside news, travel
// beside nature, music beside entertainment).
//
// The lifestyle half of the list — politics, sports, food, travel, automotive,
// home, arts, music — was added after a cycling channel kept landing in
// entertainment: the classify prompt forces a choice, so a missing category
// does not produce 'uncategorized', it produces a confidently wrong one.
var Categories = []Category{
	{"ai", "Artificial Intelligence", "machine learning, LLMs, model research, AI products and their effects"},
	{"tech", "Technology & Gadgets", "consumer electronics, devices, hardware reviews, the tech industry"},
	{"software", "Software Engineering", "programming, developer tools, systems, security"},
	{"science", "Science & Research", "physics, chemistry, biology, mathematics, nuclear and weapons science, and the investigation of unexplained phenomena; the subject stays scientific even when governments or the military are the setting"},
	{"space", "Space & Astronomy", ""},
	{"engineering", "Engineering & Making", "workshop builds, machining, robotics, infrastructure"},
	{"business", "Business & Finance", ""},
	{"news", "News & Current Events", "reported events"},
	{"politics", "Politics & Society", "elections, policy, government, geopolitics and social commentary, where politics itself is the subject rather than the setting of a scientific, technical, military or historical one; analysis, as opposed to reported events"},
	{"history", "History & Culture", "past events, wars, civilizations, biography, declassified programs"},
	{"health", "Health & Medicine", ""},
	{"sports", "Sports & Fitness", "athletics, cycling, running, gym, training and race coverage"},
	{"food", "Food & Cooking", "recipes, technique, restaurants, coffee, brewing"},
	{"nature", "Nature & Environment", ""},
	{"travel", "Travel & Outdoors", "trips, hiking, camping, places"},
	{"automotive", "Automotive & Transport", "cars, EVs, motorsport, aviation, rail; road tests and car tech"},
	{"home", "Home & DIY", "renovation, woodworking, gardening, repair around the house"},
	{"education", "Education & Tutorials", "how-to and teaching where the subject fits no other category"},
	{"arts", "Arts & Design", "photography, filmmaking, drawing, architecture, graphic design"},
	{"music", "Music", "performances, music theory, instruments, gear, album analysis"},
	{"gaming", "Gaming", ""},
	{"entertainment", "Entertainment", "film, TV, comedy, celebrity, vlogs"},
	{"uncategorized", "Uncategorized", ""},
}

// CategoryIDs returns every id in Categories order.
func CategoryIDs() []string {
	ids := make([]string, len(Categories))
	for i, c := range Categories {
		ids[i] = c.ID
	}
	return ids
}

// CategoryLabel returns a category's display label, or the id itself when it is
// not a known one — so a caller rendering it for a reader never shows an empty
// string where a name should be.
func CategoryLabel(id string) string {
	for _, c := range Categories {
		if c.ID == id {
			return c.Label
		}
	}
	return id
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
