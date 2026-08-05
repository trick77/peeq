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
// WeightKeywordPrefix sits just below the content rung. A prefix rung means every
// content word appears in this chunk in SOME inflection — "transient" for a query
// that said "transients" — which is very nearly what the content rung means, and
// far more than the OR floor's "any one of these words".
//
// Measured on the library this was tuned against: 'transient*' matches exactly
// what '"transient" OR "transients"' matches, chunk for chunk. The width it buys
// is inflection, not noise, which is why it earns 0.7 rather than floor money.
//
// WeightSemanticTopic is the second vector lane: the same embedding model over
// the question with its framing stripped off (see httpapi/understand.go). It is
// set EQUAL to WeightSemantic, and the equality is the decision, not a placeholder.
//
// The two lanes are the same retrieval method asked two ways, so neither is
// better evidence than the other a priori: the raw lane knows the reader's exact
// words, the topic lane knows what those words were about. Weighting the topic
// lane higher would assert the rewrite is more trustworthy than the question,
// which is precisely the assumption this design refuses to make — a rewrite is
// allowed to add evidence, never to overrule.
//
// Note what equality does at fusion, because it is intended rather than
// incidental: FuseWeighted SUMS a chunk's score across lanes, so a chunk BOTH
// vector lanes return earns up to 1.2 and can outscore a strict keyword rung at
// 1.0. That is the point. Two different phrasings of one question landing on the
// same passage is real agreement, and it is the only signal in this system that
// distinguishes a passage about the topic from one that merely repeats the
// reader's words. A chunk only one lane found still earns only 0.6.
//
// This is the first constant to turn down if focused answers start drifting.
const (
	WeightKeywordStrict  = 1.0
	WeightKeywordContent = 0.9
	WeightKeywordPrefix  = 0.7
	WeightKeywordAny     = 0.4
	WeightSemantic       = 0.6
	WeightSemanticTopic  = 0.6
)

// DefaultMaxDistance is the L2 cutoff past which a semantic hit is treated as
// "not actually about this" and dropped before ranking.
//
// The embedding vectors are unit length, so L2 and cosine rank identically and
// convert exactly: L2 = sqrt(2 - 2*cos). 1.25 is cosine ~0.22. For
// text-embedding-3-small, unrelated text pairs sit around cosine 0.0-0.15
// (L2 1.31-1.41) and genuinely related passages above cosine 0.3.
//
// This started at 1.20 (cosine ~0.28), a value read off published model
// behaviour and never measured against a real library. It was too permissive to
// do its job: a question with six good chunks still came back with twenty
// moments, the last fourteen being whatever was least distant among the
// irrelevant. So #265 tightened it to 1.05 and added SemanticSpread in the same
// commit — belt and braces against the same padding.
//
// Measured on a real library, the pair over-corrected and this half is the one
// doing the damage. On a 137-video library, "transients" — a word 58 indexed
// chunks contain — got a vector lane of ONE row out of the 40 requested, and the
// nearest chunk in the entire corpus sat at 0.995 (cosine 0.505). A natural
// question fared no better: 5 rows, everything between 0.917 and 1.049, hugging
// this ceiling with 35 rows discarded just past it. The lane was contributing two
// videos while the keyword floor carried thirty.
//
// The reason is that absolute cosine means less here than it looks. A ~600-token
// chunk's vector averages a lot of content, so a short query against a long
// passage scores low BY CONSTRUCTION, and text-embedding-3-small compresses the
// range further — related passages land around 0.25-0.50, not the 0.7+ that
// older models gave. A bound at cosine 0.45 therefore sits at the TOP of this
// model's relevant band rather than below it.
//
// 1.25 is cosine ~0.22: under the ~0.3 where related passages start, so genuine
// matches survive, and well clear of unrelated text at cosine 0.0-0.15
// (L2 1.31-1.41). The padding this used to prevent is now prevented by things
// better suited to it — SemanticSpread judges a hit against the best the query
// actually found rather than against a constant, and the answer's breadth pass
// (httpapi.answerBreadthSources) spends its slots one video at a time, so
// "nearest available" cannot fill an evidence set the way it once filled twenty
// moments. Tunable via BACKEND_SEARCH_MAX_DISTANCE; negative disables both this
// and the spread.
const DefaultMaxDistance = 1.25

// SemanticSpread is how much worse than the BEST hit of this query a semantic
// hit may be and still count as evidence.
//
// An absolute bound cannot express what matters here. A library about one broad
// subject has small distances across the whole corpus, so a cutoff loose enough
// to keep recall on a narrow query is loose enough to admit half the library on
// a query nothing covers. The question worth asking is relative: is this hit
// close to the best thing the query found, or merely closer than the floor?
//
// This was 0.12, and it was covering for a mis-calibrated absolute bound. With
// DefaultMaxDistance at 1.05 the lane arrived pre-truncated, so a tight spread
// cost nothing visible; once the bound was set against the embedding model's real
// range, the spread became the binding constraint and a severe one. Measured on a
// 137-video library, "transients": the bound admitted 40 rows across 11 videos,
// spanning 0.995 to 1.213 — and a 0.12 spread cut that to 5 rows across 3, taking
// the fused result from 13 videos back to 6. Thirty-five of forty rows dropped,
// and the videos behind them were on topic.
//
// The reason is the band's shape. Related passages here run 0.995-1.213, about
// 0.218 wide, and they cluster at the FAR end — so a window measured from the
// best hit catches a twentieth of the lane rather than the top half of it. A
// ~600-token chunk against a short query scores low by construction, so the best
// hit is rarely close in absolute terms and the gradient is flat.
//
// 0.20 reaches 1.195 from a 0.995 best: it keeps such a band's body and still trims
// its extreme edge, while staying meaningful where the mechanism earns its place —
// a query whose best hit is genuinely close, say 0.70, still cuts at 0.90 rather
// than waving through everything the bound allowed. 0.22 would have kept every row
// on the library this was measured against, which is a constant doing nothing.
//
// The division of labour is now the clean one. DefaultMaxDistance rejects "not
// about anything at all" — unrelated text sits at cosine 0.0-0.15, L2 1.31-1.41,
// well outside 1.25 — which leaves the spread only its own question: is this much
// worse than the best thing this query found?
const SemanticSpread = 0.20

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
