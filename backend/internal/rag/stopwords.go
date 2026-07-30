package rag

// stopwords are the function words an Ask-mode question is built from. They
// appear in essentially every transcript, so ANDing them into the keyword lane
// guarantees zero rows for any natural question ("did someone ever talk about
// electrolytes" needs every one of did/someone/ever/talk/about to co-occur in a
// single ~600-token chunk).
//
// This list is deliberately conservative: only words that carry no topical
// signal at all. Anything a user might plausibly be searching FOR — "how" in
// "how to", domain nouns, verbs like "talk" or "say" — stays out of it, since
// dropping a term the user meant is worse than keeping one they didn't. The
// strict tier runs first anyway (see BuildFTSQueries), so this list only ever
// affects a query that would otherwise have matched nothing.
//
// English only, matching the fact that FTS5 here uses the default tokenizer.
var stopwords = map[string]struct{}{}

func init() {
	words := []string{
		// articles, conjunctions, particles
		"a", "an", "the", "and", "or", "but", "nor", "so", "yet", "if", "then",
		"than", "as", "that", "this", "these", "those", "there", "here",
		// prepositions
		"about", "above", "across", "after", "against", "along", "among",
		"around", "at", "before", "behind", "below", "beneath", "beside",
		"between", "beyond", "by", "down", "during", "for", "from", "in",
		"inside", "into", "near", "of", "off", "on", "onto", "out", "outside",
		"over", "through", "to", "toward", "under", "until", "up", "upon",
		"with", "within", "without",
		// pronouns and determiners
		"i", "me", "my", "we", "us", "our", "you", "your", "he", "him", "his",
		"she", "her", "it", "its", "they", "them", "their", "who", "whom",
		"whose", "which", "what", "some", "someone", "somebody", "something",
		"anyone", "anybody", "anything", "everyone", "everything", "no",
		"none", "any", "all", "both", "each", "other", "another", "such",
		// auxiliaries and copulas
		"am", "is", "are", "was", "were", "be", "been", "being", "do", "does",
		"did", "done", "have", "has", "had", "having", "can", "could", "will",
		"would", "shall", "should", "may", "might", "must",
		// common adverbs with no topical weight
		"ever", "never", "always", "also", "just", "only", "very", "really",
		"too", "again", "still", "even", "much", "many", "more", "most",
		"not", "now", "ok", "okay", "please",
	}
	for _, w := range words {
		stopwords[w] = struct{}{}
	}
}
