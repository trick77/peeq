package videos

import (
	"encoding/json"
	"slices"
	"strings"
)

// Subcategory is one entry of the fixed subcategory vocabulary: a finer label
// under a category. A video carries up to MaxSubcategories of its own
// category's; a channel up to the same number from any category, since a
// channel spans what its videos span.
//
// ID is unique within its category only ('other', 'industry', 'film' recur);
// Key() — "category/id" — is what is stored and what callers filter by. Hint
// plays the same role as Category.Hint: prompt steering for the pairs a model
// would blur, never display copy.
type Subcategory struct {
	Category string
	ID       string
	Label    string
	Hint     string
}

// Key is the stored, filterable form: "sports/cycling".
func (s Subcategory) Key() string { return s.Category + "/" + s.ID }

const (
	// OtherSubcategory closes every category's list. The classify prompts
	// force a choice, and a forced choice without an escape lands a misfit on
	// a confidently wrong neighbour — the category enum's own lesson.
	OtherSubcategory = "other"
	// MaxSubcategories caps a video's or channel's labels. Without a cap the
	// model files everything under everything; the first label is the primary.
	MaxSubcategories = 3
)

// Subcategories is the fixed vocabulary, grouped by category in Categories
// order. Broad on purpose: it has to fit videos the library does not hold yet,
// and a fine split makes a multi-label answer unstable between runs.
var Subcategories = []Subcategory{
	{"ai", "building", "Building with AI", "agents, RAG, evals, LLM tooling, AI-assisted coding"},
	{"ai", "models", "Models & research", "model releases, benchmarks, comparisons, research results"},
	{"ai", "infra", "Infrastructure", "training, inference, GPUs, chips, compute"},
	{"ai", "business", "AI business", "AI companies, products, pricing, deals, go-to-market"},
	{"ai", "society", "AI & society", "safety, alignment, ethics, consciousness, jobs, policy"},
	{"ai", "other", "Other", ""},

	{"tech", "phones", "Phones & tablets", ""},
	{"tech", "computers", "Computers & hardware", "PCs, laptops, servers, components, datacenters"},
	{"tech", "wearables", "Wearables", "watches, fitness trackers, bike computers"},
	{"tech", "smarthome", "Smart home", ""},
	{"tech", "industry", "Tech industry", ""},
	{"tech", "other", "Other", ""},

	{"software", "development", "Development", "languages, frameworks, architecture"},
	{"software", "web", "Web", ""},
	{"software", "systems", "Systems & ops", "operating systems, Linux, networking, cloud, devops"},
	{"software", "security", "Security", ""},
	{"software", "tools", "Tools & workflow", "editors, terminals, developer workflow"},
	{"software", "other", "Other", ""},

	{"science", "physics", "Physics", "including theoretical physics and quantum mechanics"},
	{"science", "chemistry", "Chemistry", ""},
	{"science", "biology", "Biology", "life, evolution, genetics, medicine research"},
	{"science", "earth", "Earth & climate", "climate science, geology, weather"},
	{"science", "mind", "Mind & brain", "neuroscience, psychology, consciousness research"},
	{"science", "math", "Mathematics", ""},
	{"science", "energy", "Energy & nuclear", "nuclear physics and weapons, fusion, power generation"},
	{"science", "uap", "UAP & unexplained", "UFOs, UAP, disclosure, paranormal and fringe investigation"},
	{"science", "other", "Other", ""},

	{"space", "spaceflight", "Spaceflight", "launches, rockets, SpaceX, vehicles"},
	{"space", "exploration", "Exploration", "Moon, Mars, probes, missions"},
	{"space", "astronomy", "Astronomy", "stars, planets, telescopes, observation"},
	{"space", "cosmology", "Cosmology", "the universe, dark matter, dark energy, the big bang"},
	{"space", "other", "Other", ""},

	{"engineering", "making", "Making", "workshop builds, machining, 3D printing"},
	{"engineering", "electronics", "Electronics", "DIY electronics, radio, embedded"},
	{"engineering", "robotics", "Robotics", ""},
	{"engineering", "infrastructure", "Infrastructure", "civil engineering, megaprojects, power grids"},
	{"engineering", "other", "Other", ""},

	{"business", "markets", "Markets & investing", ""},
	{"business", "personal-finance", "Personal finance", ""},
	{"business", "crypto", "Crypto", ""},
	{"business", "companies", "Companies & startups", ""},
	{"business", "economy", "Economy", ""},
	{"business", "work", "Work & careers", "careers, productivity, management"},
	{"business", "other", "Other", ""},

	{"news", "world", "World", ""},
	{"news", "regional", "Regional", ""},
	{"news", "tech", "Tech news", ""},
	{"news", "science", "Science news", ""},
	{"news", "business", "Business news", ""},
	{"news", "other", "Other", ""},

	{"politics", "elections", "Elections", ""},
	{"politics", "policy", "Policy", ""},
	{"politics", "geopolitics", "Geopolitics", ""},
	{"politics", "government", "Government", "institutions, law, secrecy, oversight"},
	{"politics", "society", "Society", "social commentary, culture"},
	{"politics", "other", "Other", ""},

	{"history", "ancient", "Ancient", ""},
	{"history", "military", "Military", "wars, weapons programs, military history"},
	{"history", "modern", "Modern", "the 20th century onward"},
	{"history", "biography", "Biography", ""},
	{"history", "archaeology", "Archaeology", ""},
	{"history", "other", "Other", ""},

	{"health", "medicine", "Medicine", ""},
	{"health", "nutrition", "Nutrition", ""},
	{"health", "mental", "Mental health", ""},
	{"health", "longevity", "Longevity", ""},
	{"health", "other", "Other", ""},

	{"sports", "cycling", "Cycling", "racing, training, bike fitting, bikes and gear"},
	{"sports", "running", "Running", ""},
	{"sports", "fitness", "Fitness", "gym, strength, general training"},
	{"sports", "team", "Team sports", "football, basketball and the like"},
	{"sports", "combat", "Combat sports", ""},
	{"sports", "winter", "Winter sports", ""},
	{"sports", "other", "Other", ""},

	{"food", "recipes", "Recipes & cooking", ""},
	{"food", "restaurants", "Restaurants", ""},
	{"food", "drinks", "Drinks", "coffee, beer, wine"},
	{"food", "other", "Other", ""},

	{"nature", "wildlife", "Wildlife", ""},
	{"nature", "environment", "Environment", "conservation, ecosystems"},
	{"nature", "other", "Other", ""},

	{"travel", "destinations", "Destinations", ""},
	{"travel", "outdoors", "Outdoors", "hiking, camping, bikepacking"},
	{"travel", "exploration", "Exploration", "remote places, restricted sites, urban exploring"},
	{"travel", "other", "Other", ""},

	{"automotive", "cars", "Cars", ""},
	{"automotive", "ev", "Electric vehicles", ""},
	{"automotive", "motorsport", "Motorsport", ""},
	{"automotive", "aviation", "Aviation", ""},
	{"automotive", "rail", "Rail & transit", ""},
	{"automotive", "other", "Other", ""},

	{"home", "renovation", "Renovation", ""},
	{"home", "woodworking", "Woodworking", ""},
	{"home", "gardening", "Gardening", ""},
	{"home", "repair", "Repair", ""},
	{"home", "other", "Other", ""},

	{"education", "languages", "Languages", ""},
	{"education", "study", "Study & learning", ""},
	{"education", "howto", "How-to", ""},
	{"education", "other", "Other", ""},

	{"arts", "photography", "Photography", ""},
	{"arts", "film", "Filmmaking", ""},
	{"arts", "drawing", "Drawing & painting", ""},
	{"arts", "design", "Design & architecture", ""},
	{"arts", "other", "Other", ""},

	{"music", "performance", "Performance", ""},
	{"music", "theory", "Theory", ""},
	{"music", "gear", "Gear", ""},
	{"music", "analysis", "Analysis", ""},
	{"music", "other", "Other", ""},

	{"gaming", "reviews", "Reviews", ""},
	{"gaming", "playthroughs", "Playthroughs", ""},
	{"gaming", "esports", "Esports", ""},
	{"gaming", "retro", "Retro", ""},
	{"gaming", "industry", "Games industry", ""},
	{"gaming", "other", "Other", ""},

	{"entertainment", "film", "Film", ""},
	{"entertainment", "tv", "TV", ""},
	{"entertainment", "comedy", "Comedy", ""},
	{"entertainment", "celebrity", "Celebrity", ""},
	{"entertainment", "vlogs", "Vlogs", ""},
	{"entertainment", "other", "Other", ""},
}

// SubcategoriesFor returns a category's subcategories in order, 'other' last.
func SubcategoriesFor(category string) []Subcategory {
	var out []Subcategory
	for _, s := range Subcategories {
		if s.Category == category {
			out = append(out, s)
		}
	}
	return out
}

// SubcategoryKeys returns every key in vocabulary order.
func SubcategoryKeys() []string {
	out := make([]string, len(Subcategories))
	for i, s := range Subcategories {
		out[i] = s.Key()
	}
	return out
}

// ValidSubcategory reports whether key is an exact "category/id" key.
func ValidSubcategory(key string) bool {
	_, ok := LookupSubcategory(key)
	return ok
}

// SubcategoryLabel is a key's display label, or the key itself when unknown,
// so a reader never sees an empty name.
func SubcategoryLabel(key string) string {
	if s, ok := LookupSubcategory(key); ok {
		return s.Label
	}
	return key
}

// LookupSubcategory returns the vocabulary entry for a "category/id" key.
func LookupSubcategory(key string) (Subcategory, bool) {
	for _, s := range Subcategories {
		if s.Key() == key {
			return s, true
		}
	}
	return Subcategory{}, false
}

// NormalizeSubcategories turns a classify reply into at most MaxSubcategories
// keys from allowed, in the reply's order, deduplicated. The reply is the JSON
// object {"subcategories": [...]} the prompts ask for, each entry a full key
// or an id. The calls run on the gate model without JSON mode (it may not
// support it), so the object is cut out of whatever fence or prose wraps it.
// An id counts only when it is unique within allowed: 'other' offered across
// categories names no category at all. Unknown entries are dropped, never
// guessed.
//
// ok reports whether the reply is an ANSWER: a parseable object whose list is
// empty ("nothing fits") or names at least one usable key. A reply that does
// not parse, lacks the list, or names only junk is not one — and an empty or
// cut reply (a call that out-thinks its cap ends "length" with no content and
// no error) must not be persisted as "nothing fits", or the sweep never asks
// again. The caller parks those instead.
func NormalizeSubcategories(reply string, allowed []Subcategory) (keys []string, ok bool) {
	start, end := strings.IndexByte(reply, '{'), strings.LastIndexByte(reply, '}')
	if start < 0 || end < start {
		return nil, false
	}
	var parsed struct {
		Subcategories *[]string `json:"subcategories"`
	}
	if json.Unmarshal([]byte(reply[start:end+1]), &parsed) != nil || parsed.Subcategories == nil {
		return nil, false
	}
	if len(*parsed.Subcategories) == 0 {
		return nil, true
	}
	byKey := map[string]string{}
	idCount := map[string]int{}
	for _, a := range allowed {
		byKey[a.Key()] = a.Key()
		idCount[a.ID]++
	}
	for _, a := range allowed {
		if idCount[a.ID] == 1 {
			byKey[a.ID] = a.Key()
		}
	}
	for _, raw := range *parsed.Subcategories {
		key, found := byKey[strings.ToLower(strings.TrimSpace(raw))]
		if !found || slices.Contains(keys, key) {
			continue
		}
		keys = append(keys, key)
		if len(keys) == MaxSubcategories {
			break
		}
	}
	return keys, len(keys) > 0
}
