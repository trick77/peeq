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

// BuildFTSQueries returns Ask-mode MATCH expressions ordered strictest first.
// The caller runs them in order and keeps the first that returns any row, so a
// precise query still gets a precise keyword lane and only a query that would
// otherwise match nothing pays for a second round-trip:
//
//	[0] every term ANDed          — identical to BuildFTSMatch(q)
//	[1] content terms ANDed       — function words dropped
//	[2] content terms ORed        — recall floor
//
// Consecutive duplicates are dropped, so a query with no stopwords yields one
// entry and costs exactly what it costs today. Returns nil for unusable input.
func BuildFTSQueries(q string) []string {
	strict := BuildFTSMatch(q)
	if strict == "" {
		return nil
	}
	quoted := strings.Fields(strict)
	content := make([]string, 0, len(quoted))
	for _, t := range quoted {
		if _, stop := stopwords[strings.Trim(t, `"`)]; stop {
			continue
		}
		content = append(content, t)
	}
	if len(content) > maxTerms {
		content = content[:maxTerms]
	}
	tiers := []string{strict}
	// Every term was a stopword ("what is it about") — there is no content
	// query to fall back to, so the strict tier stands alone.
	if len(content) == 0 {
		return tiers
	}
	if and := strings.Join(content, " "); and != strict {
		tiers = append(tiers, and)
	}
	// ORing a single term reproduces the AND tier exactly; only add the recall
	// floor when it actually widens the match.
	if len(content) > 1 {
		tiers = append(tiers, strings.Join(content, " OR "))
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
