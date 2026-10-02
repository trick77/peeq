package rag

// FuseRRF is FuseWeighted with every lane at weight 1: plain Reciprocal Rank
// Fusion. Nothing in production fuses unweighted any more; the tests keep it as
// the baseline the weighted ranking is compared against.
func FuseRRF(lists [][]Hit, k int) []Hit {
	lanes := make([]Lane, 0, len(lists))
	for _, l := range lists {
		lanes = append(lanes, Lane{Hits: l, Weight: 1})
	}
	return FuseWeighted(lanes, k)
}
