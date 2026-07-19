package rag

import (
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// rrfK is the Reciprocal Rank Fusion damping constant. 60 is the widely used
// default (Cormack et al.): large enough that top ranks don't dominate, small
// enough that deep ranks still contribute.
const rrfK = 60

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

// FuseRRF merges pre-ranked hit lists via Reciprocal Rank Fusion: a hit's
// score is the sum over lists of 1/(rrfK + rank), where rank is its 0-based
// position in that list. Hits are identified across lists by video id +
// ordinal. Returns up to k hits, best score first; ties break by the identity
// key for determinism.
func FuseRRF(lists [][]Hit, k int) []Hit {
	type agg struct {
		hit   Hit
		score float64
	}
	byKey := make(map[string]*agg)
	order := make([]string, 0)
	for _, list := range lists {
		for rank, h := range list {
			key := h.VideoID + ":" + strconv.Itoa(h.Ordinal)
			a, ok := byKey[key]
			if !ok {
				a = &agg{hit: h}
				byKey[key] = a
				order = append(order, key)
			}
			a.score += 1.0 / float64(rrfK+rank)
		}
	}
	keys := append([]string(nil), order...)
	sort.SliceStable(keys, func(i, j int) bool {
		ai, aj := byKey[keys[i]], byKey[keys[j]]
		if ai.score != aj.score {
			return ai.score > aj.score
		}
		return keys[i] < keys[j]
	})
	if k <= 0 {
		k = 10
	}
	out := make([]Hit, 0, len(keys))
	for _, key := range keys {
		out = append(out, byKey[key].hit)
		if len(out) >= k {
			break
		}
	}
	return out
}
