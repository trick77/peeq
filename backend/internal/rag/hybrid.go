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
// The keyword lane's weight depends on WHICH rung of the BuildFTSQueries ladder
// answered, because the rungs are not equal evidence. The strict rung means
// every word the user typed is in this chunk. The OR rung — the recall floor an
// unanswerable question falls through to — means ANY ONE content word is, which
// for a question like "did someone talk about electrolytes in endurance sport"
// is satisfied by "talk" or "sport" alone. Each rung carries its own weight, on
// FTSTier: the ladder drops redundant rungs, so a rung's slice position does
// not identify it.
//
// Flat-weighting them let up to searchCandidates chunks sharing one common word
// enter at full confidence and outrank passages that are genuinely about the
// topic. That is the same failure the lane weights exist to prevent, one level
// down: weak evidence beating strong because the fusion could not tell them
// apart.
//
// WeightKeywordAny sits BELOW WeightSemantic deliberately. A chunk that happens
// to share a word is worse evidence than one the embedding places near the
// question.
const (
	WeightKeywordStrict  = 1.0
	WeightKeywordContent = 0.9
	WeightKeywordAny     = 0.4
	WeightSemantic       = 0.6
)

// DefaultMaxDistance is the L2 cutoff past which a semantic hit is treated as
// "not actually about this" and dropped before ranking.
//
// The embedding vectors are unit length, so L2 and cosine rank identically and
// convert exactly: L2 = sqrt(2 - 2*cos). 1.05 is cosine ~0.45. For
// text-embedding-3-small, unrelated text pairs sit around cosine 0.0-0.15
// (L2 1.31-1.41) and genuinely related passages above cosine 0.3.
//
// This started at 1.20 (cosine ~0.28), a value read off published model
// behaviour and never measured against a real library. It was too permissive to
// do its job: a question with six good chunks still came back with twenty
// moments, the last fourteen being whatever was least distant among the
// irrelevant. Tunable via BACKEND_SEARCH_MAX_DISTANCE.
const DefaultMaxDistance = 1.05

// SemanticSpread is how much worse than the BEST hit of this query a semantic
// hit may be and still count as evidence.
//
// An absolute bound cannot express what matters here. A library about one broad
// subject has small distances across the whole corpus, so a cutoff loose enough
// to keep recall on a narrow query is loose enough to admit half the library on
// a query nothing covers. The question worth asking is relative: is this hit
// close to the best thing the query found, or merely closer than the floor?
//
// 0.12 in L2 is roughly a tenth of a cosine point at this end of the range —
// wide enough to keep a paraphrase that the top hit states outright, narrow
// enough that "nearest available" cannot ride in behind a genuine match.
const SemanticSpread = 0.12

// WithinSpread drops semantic hits more than SemanticSpread past the closest
// one, and is what lets the vector lane return FEWER rows than it was asked
// for. hits must be distance-ascending, which is what Store.RetrieveWithin
// returns; an empty list stays empty.
//
// This belongs on the fused input rather than in SQL, unlike the absolute
// bound: the reference point is the best row of this particular query, which is
// not known until the rows come back.
func WithinSpread(hits []Hit, spread float64) []Hit {
	if len(hits) == 0 || spread <= 0 {
		return hits
	}
	limit := hits[0].Distance + spread
	for i, h := range hits {
		if h.Distance > limit {
			return hits[:i]
		}
	}
	return hits
}

// The cutoff itself lives in SQL, in Store.RetrieveWithin: a Go-side filter over
// the fused list would have to guess which hits carry a real distance and which
// are keyword hits that simply leave it 0, and the two spellings would drift.
// See RetrieveWithin for why this is what lets a search report that it found
// nothing at all — without a bound a KNN query cannot fail, it returns k rows
// for any input whatsoever.

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
//
// A lane whose weight is zero or negative contributes nothing at all.
func FuseWeighted(lanes []Lane, k int) []Hit {
	type agg struct {
		hit   Hit
		score float64
	}
	byKey := make(map[string]*agg)
	order := make([]string, 0)
	for _, lane := range lanes {
		// A non-positive weight MUTES the lane. It used to be promoted to 1 —
		// full confidence — which made the one value a caller would reach for to
		// silence a lane the value that made it shout loudest.
		if lane.Weight <= 0 {
			continue
		}
		w := lane.Weight
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
