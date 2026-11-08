package rag

// Hit is one retrieved chunk with its cosine/L2 distance (smaller == closer).
//
// Snippet is set only by the keyword lane, which knows which part of the chunk
// actually matched; the vector lane matches a chunk as a whole and leaves it
// empty for the caller to fall back to Text.
//
// Table: transcript_chunks
type Hit struct {
	VideoID      string
	Ordinal      int
	Text         string
	Kind         string
	StartSeconds int
	Distance     float64
	Snippet      string
}

// KindCount is how many chunks of one kind a video contributed to the index,
// and how much text they carry.
//
// Table: -
type KindCount struct {
	Kind   string
	Count  int
	Tokens int
}

// LibraryCount is what the library holds under a filter, for a question asking
// how much of something there is rather than what it says.
//
// Table: -
type LibraryCount struct {
	Videos          int `json:"videos"`
	DurationSeconds int `json:"duration_seconds"`
	Channels        int `json:"channels"`
}

// VecCompaction is what one CompactVectors call found and did. Chunks are
// vec0's storage chunks (vec_chunks_chunks rows), not transcript chunks.
//
// Table: -
type VecCompaction struct {
	Compacted    bool
	Rows         int64
	ChunksBefore int64
	ChunksAfter  int64
}
