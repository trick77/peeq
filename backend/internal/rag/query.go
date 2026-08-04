package rag

import (
	"strings"
	"unicode"
)

// Peeq has two search modes and they want opposite things from the query text.
//
// Find is a full-text search: the user typed words they expect to appear, and
// they may have typed FTS5 operators on purpose. ParseFTSQuery preserves that
// intent — phrases, OR, NOT, prefix — while still never handing raw user text
// to FTS5 (a syntax error there surfaces as a 500, and MATCH is an expression
// language, so the input has to be re-emitted from tokens we recognize rather
// than escaped in place).
//
// Ask is a question: the user typed a sentence whose function words ("did",
// "someone", "ever") appear in every transcript and carry no signal. The strict
// all-terms-ANDed expression BuildFTSMatch produces matches nothing at all for
// such a query, so the keyword lane silently abstains and the search degrades
// to pure vector similarity. BuildFTSQueries relaxes in steps instead.

// maxTerms caps how many tokens one query contributes to a MATCH expression.
// FTS5 has its own expression-depth limits and a pathological paste should be
// truncated rather than rejected.
const maxTerms = 32

// minPrefixRunes is the shortest content term the prefix rungs will widen. Below
// it a shared opening says nothing about a shared meaning, and the point of those
// rungs is inflection, not any word that starts the same.
//
// Five rather than four because the damaging collisions are all four-rune stems
// of ordinary words: "star" reaches start and started, "main" reaches maintain,
// "back" reaches backup and background, "form" reaches formula. None is a
// stopword, so none is filtered out before it gets here. The cost is losing the
// plural of a four-letter noun, and that is the cheaper side: the prefix rung
// weighs 0.7, ABOVE the semantic lane's 0.6, and excerpt selection now walks the
// whole fused list — so a collision does not rank low, it takes one of the twelve
// slots the model reads.
const minPrefixRunes = 5

// ParseFTSQuery turns a Find-mode query into an FTS5 MATCH expression,
// preserving the operators a full-text search is expected to support:
//
//	"exact phrase"     a quoted phrase matches those words adjacently
//	sodium OR calcium  either term
//	hydration NOT ad   exclude
//	cramp*             prefix match
//
// Bare terms are implicitly ANDed, which is FTS5's own default. Safety comes
// from re-emission, not escaping: every term is lowercased, stripped to letters
// and digits, and re-quoted, so nothing the user types can reach FTS5 as syntax.
// The only unquoted tokens in the output are the AND/OR/NOT keywords and the
// prefix '*', all of which this function emits itself.
//
// Returns "" when nothing usable remains, which callers treat as "no keyword
// lane" rather than as an error.
func ParseFTSQuery(q string) string {
	toks := tokenizeQuery(q)
	out := make([]string, 0, len(toks))
	// wantOperand tracks whether the next token must be a term. It starts true
	// (an expression cannot open with an operator) and lets a trailing or
	// doubled operator be dropped instead of producing "a OR OR" / "a OR".
	wantOperand := true
	for _, t := range toks {
		if len(out) >= maxTerms {
			break
		}
		if t.op != "" {
			// NOT is the one operator that may also read as a term the user
			// wants; treating a leading NOT as an operator would emit an
			// expression with no left operand, so drop it.
			if wantOperand {
				continue
			}
			out = append(out, t.op)
			wantOperand = true
			continue
		}
		term := `"` + t.text + `"`
		if t.prefix {
			// FTS5 spells a prefix match on a quoted string as `"abc" *`.
			term += " *"
		}
		out = append(out, term)
		wantOperand = false
	}
	// A trailing operator ("sodium OR") is not a valid expression.
	for len(out) > 0 {
		last := out[len(out)-1]
		if last == "OR" || last == "NOT" || last == "AND" {
			out = out[:len(out)-1]
			continue
		}
		break
	}
	return strings.Join(out, " ")
}

// BuildFTSMatch turns a raw user query into a safe FTS5 MATCH expression.
// Each term is lowercased, stripped of everything but letters/digits, and
// wrapped in double quotes so FTS5 treats it as a literal phrase token rather
// than an operator (AND/OR/NOT/NEAR) or a syntax character. Space-separated
// terms are implicitly ANDed by FTS5, so a multi-word query requires all its
// words to appear. Returns "" when nothing usable remains.
func BuildFTSMatch(q string) string {
	fields := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ToLower(f)
		if f == "" {
			continue
		}
		terms = append(terms, `"`+f+`"`)
	}
	return strings.Join(terms, " ")
}

// FTSTier is one rung of the Ask ladder: a MATCH expression plus the
// confidence the keyword lane earns by answering at that rung. The weight
// travels WITH the expression because the ladder skips redundant rungs — a
// query with no stopwords has no content-ANDed rung, so the OR floor is the
// second entry, not the third. A caller weighing by slice position would hand
// that floor near-full confidence.
type FTSTier struct {
	Match  string
	Weight float64
}

// BuildFTSQueries returns the Ask-mode ladder, strictest rung first. The caller
// runs the rungs in order and keeps the first that returns any row, so a
// precise query still gets a precise keyword lane and only a query that would
// otherwise match nothing pays for a second round-trip:
//
//	every term ANDed          — identical to BuildFTSMatch(q)  (WeightKeywordStrict)
//	content terms ANDed       — function words dropped         (WeightKeywordContent)
//	content prefixes ANDed    — inflection tolerated           (WeightKeywordPrefix)
//	content prefixes ORed     — recall floor                   (WeightKeywordAny)
//
// The floor is a PREFIX or, not a literal one. The index has no stemming — plain
// fts5(text), default unicode61 — so "transients" and "transient" are unrelated
// tokens, and a question written in the plural could not reach a video that says
// the singular. On the library this was tuned against that cost five of eleven
// videos on one word. A prefix floor is a strict superset of the literal floor at
// the same weight, so nothing it used to find is lost.
//
// Redundant rungs are dropped, so a query with no stopwords yields fewer
// entries and costs exactly what it costs today. Returns nil for unusable
// input.
func BuildFTSQueries(q string) []FTSTier {
	strict := BuildFTSMatch(q)
	if strict == "" {
		return nil
	}
	// Cap before the tiers are built, not after: capping only the relaxed tiers
	// would leave the STRICT one — the tier that always runs — carrying every
	// term of a pasted paragraph.
	quoted := strings.Fields(strict)
	if len(quoted) > maxTerms {
		quoted = quoted[:maxTerms]
		strict = strings.Join(quoted, " ")
	}
	content := make([]string, 0, len(quoted))
	for _, t := range quoted {
		if _, stop := stopwords[strings.Trim(t, `"`)]; stop {
			continue
		}
		content = append(content, t)
	}
	tiers := []FTSTier{{Match: strict, Weight: WeightKeywordStrict}}
	// Every term was a stopword ("what is it about") — there is no content
	// query to fall back to, so the strict tier stands alone.
	if len(content) == 0 {
		return tiers
	}
	and := strings.Join(content, " ")
	if and != strict {
		tiers = append(tiers, FTSTier{Match: and, Weight: WeightKeywordContent})
	}
	// Prefixes of the SAME content terms, still ANDed. Widens the content rung to
	// every inflection of each word without loosening the requirement that all of
	// them appear.
	//
	// A short term is left literal. "car*" reaches carbon, cardiac and career —
	// unrelated words, not inflections — and the floor these rungs feed is walked
	// in full when the answer picks its excerpts, so a rung's noise is no longer
	// something that merely ranks low. minPrefixRunes is where inflection stops
	// being the likely reason two words share an opening: "stars" and "orbit"
	// clear it, "car" and "gps" do not.
	prefixed := make([]string, len(content))
	for i, t := range content {
		if len([]rune(strings.Trim(t, `"`))) < minPrefixRunes {
			prefixed[i] = t
			continue
		}
		prefixed[i] = t + "*"
	}
	// Every content term was too short to widen, so this rung is one of the two
	// above it verbatim. The ladder only reaches a rung when the one before it
	// returned nothing, which makes a duplicate a guaranteed-empty round-trip.
	prefixedAnd := strings.Join(prefixed, " ")
	if prefixedAnd != strict && prefixedAnd != and {
		tiers = append(tiers, FTSTier{
			Match:  prefixedAnd,
			Weight: WeightKeywordPrefix,
		})
	}
	// ORing a single term reproduces the AND tier exactly; only add the recall
	// floor when it actually widens the match.
	if len(content) > 1 {
		tiers = append(tiers, FTSTier{
			Match:  strings.Join(prefixed, " OR "),
			Weight: WeightKeywordAny,
		})
	}
	return tiers
}

// queryToken is one recognized unit of a Find-mode query: either an operator
// keyword or a sanitized term (optionally a prefix match).
type queryToken struct {
	op     string // "OR", "NOT", "AND", or "" when this is a term
	text   string // sanitized term text, lowercased
	prefix bool
}

// tokenizeQuery scans a Find-mode query into operators and sanitized terms.
// A double quote opens a phrase that runs to the next quote or to end of
// input, so an unbalanced quote degrades to "everything after it is one
// phrase" rather than to a syntax error.
func tokenizeQuery(q string) []queryToken {
	var toks []queryToken
	runes := []rune(q)
	for i := 0; i < len(runes); {
		r := runes[i]
		switch {
		case r == '"':
			i++
			start := i
			for i < len(runes) && runes[i] != '"' {
				i++
			}
			if phrase := sanitizePhrase(string(runes[start:i])); phrase != "" {
				toks = append(toks, queryToken{text: phrase})
			}
			if i < len(runes) {
				i++ // consume the closing quote
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			start := i
			for i < len(runes) && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i])) {
				i++
			}
			word := string(runes[start:i])
			// A '*' immediately after the word is a prefix marker; anywhere
			// else it is punctuation and gets skipped by the default branch.
			prefix := i < len(runes) && runes[i] == '*'
			if prefix {
				i++
			}
			if op := operatorFor(word); op != "" && !prefix {
				toks = append(toks, queryToken{op: op})
				continue
			}
			toks = append(toks, queryToken{text: strings.ToLower(word), prefix: prefix})
		default:
			i++
		}
	}
	return toks
}

// operatorFor recognizes FTS5's boolean keywords, which must be uppercase to
// count as operators. A lowercase "or" is a word the user wants to find —
// matching FTS5's own rule and keeping "rock or roll" searchable.
func operatorFor(word string) string {
	switch word {
	case "OR", "AND", "NOT":
		return word
	}
	return ""
}

// sanitizePhrase reduces quoted phrase content to lowercased, space-separated
// letter/digit words, so the phrase can be re-quoted without any character
// that FTS5 would read as syntax.
func sanitizePhrase(s string) string {
	words := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) > maxTerms {
		words = words[:maxTerms]
	}
	for i, w := range words {
		words[i] = strings.ToLower(w)
	}
	return strings.Join(words, " ")
}
