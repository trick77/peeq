package rag

import (
	"sort"
	"strconv"
)

// rrfK is the Reciprocal Rank Fusion damping constant. 60 is the widely used
// default (Cormack et al.): large enough that top ranks don't dominate, small
// enough that deep ranks still contribute.
const rrfK = 60

// Lane weights. RRF on its own is rank-blind: a hit at rank 0 of one list
// scores exactly as much as a hit at rank 0 of another, no matter how weak the
// second list is. A KNN lane ALWAYS returns its k nearest rows — on a narrow
// query most of them are simply the least distant of the irrelevant — so an
// unweighted fusion lets semantic noise outrank a literal keyword match: a
// vector hit at rank 3 scores 1/63, beating an exact term match at FTS rank 5
// on 1/65. A chunk that literally contains the user's words is the stronger
// evidence, so the keyword lane is weighted above the semantic one.
const (
	WeightKeyword  = 1.0
	WeightSemantic = 0.6
)

// DefaultMaxDistance is the L2 cutoff past which a semantic hit is treated as
// "not actually about this" and dropped before ranking.
//
// The embedding vectors are unit length, so L2 and cosine rank identically and
// convert exactly: L2 = sqrt(2 - 2*cos). 1.20 is cosine ~0.28. For
// text-embedding-3-small, unrelated text pairs sit around cosine 0.0-0.15
// (L2 1.31-1.41) and genuinely related passages above cosine 0.3, so this sits
// in the gap while staying permissive enough not to cost recall. Tunable via
// BACKEND_SEARCH_MAX_DISTANCE.
const DefaultMaxDistance = 1.20

// FilterByDistance drops hits at or beyond maxDistance, preserving order.
// A non-positive maxDistance disables the cutoff.
//
// This is what lets a search report that it found nothing. Without it a KNN
// query cannot fail: it returns k rows for any input whatsoever, so a search
// for a topic the library has never covered still renders a full page of
// confident-looking results whose text has nothing to do with the query. Hits
// carrying no distance — the keyword lane leaves it 0, since bm25 rank is
// positional — are always kept.
func FilterByDistance(hits []Hit, maxDistance float64) []Hit {
	if maxDistance <= 0 {
		return hits
	}
	out := make([]Hit, 0, len(hits))
	for _, h := range hits {
		if h.Distance > 0 && h.Distance >= maxDistance {
			continue
		}
		out = append(out, h)
	}
	return out
}

// Lane is one pre-ranked hit list plus the confidence its retrieval method
// earns. Weight scales that lane's contribution to the fused score.
type Lane struct {
	Hits   []Hit
	Weight float64
}

// FuseRRF merges pre-ranked hit lists via Reciprocal Rank Fusion: a hit's
// score is the sum over lists of 1/(rrfK + rank), where rank is its 0-based
// position in that list. Hits are identified across lists by video id +
// ordinal. Returns up to k hits, best score first; ties break by the identity
// key for determinism.
//
// Every lane counts equally here. Callers mixing retrieval methods of differing
// reliability want FuseWeighted instead.
func FuseRRF(lists [][]Hit, k int) []Hit {
	lanes := make([]Lane, 0, len(lists))
	for _, l := range lists {
		lanes = append(lanes, Lane{Hits: l, Weight: 1})
	}
	return FuseWeighted(lanes, k)
}

// FuseWeighted is FuseRRF with a per-lane confidence multiplier, so a lane that
// is merely returning its nearest rows cannot outvote a lane that found a
// literal match. Scoring is otherwise identical: the sum of weight/(rrfK+rank).
func FuseWeighted(lanes []Lane, k int) []Hit {
	type agg struct {
		hit   Hit
		score float64
	}
	byKey := make(map[string]*agg)
	order := make([]string, 0)
	for _, lane := range lanes {
		w := lane.Weight
		if w <= 0 {
			w = 1
		}
		for rank, h := range lane.Hits {
			key := h.VideoID + ":" + strconv.Itoa(h.Ordinal)
			a, ok := byKey[key]
			if !ok {
				a = &agg{hit: h}
				byKey[key] = a
				order = append(order, key)
			} else {
				// Keep whichever copy carries the richer data: the keyword lane
				// leaves Distance 0 but brings a highlighted snippet, the vector
				// lane brings a real distance. A chunk found by both should end
				// up with both.
				if a.hit.Distance == 0 && h.Distance > 0 {
					a.hit.Distance = h.Distance
				}
				if a.hit.Snippet == "" && h.Snippet != "" {
					a.hit.Snippet = h.Snippet
				}
			}
			a.score += w / float64(rrfK+rank)
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
