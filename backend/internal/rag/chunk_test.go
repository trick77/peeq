package rag

import "testing"

func TestChunkProducesOrderedOverlappingChunks(t *testing.T) {
	words := make([]byte, 0)
	for i := 0; i < 4000; i++ {
		words = append(words, 'a', ' ')
	}
	chunks := Chunk(string(words), DefaultChunkOptions())
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if c.Ordinal != i {
			t.Fatalf("ordinal %d != %d", c.Ordinal, i)
		}
	}
	if len(Chunk("   ", DefaultChunkOptions())) != 0 {
		t.Fatal("whitespace-only should yield no chunks")
	}
}

func TestChunkSetsIncreasingWordOffset(t *testing.T) {
	words := make([]byte, 0)
	for i := 0; i < 4000; i++ {
		words = append(words, 'a', ' ')
	}
	chunks := Chunk(string(words), DefaultChunkOptions())
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	if chunks[0].WordOffset != 0 {
		t.Fatalf("expected first chunk's WordOffset == 0, got %d", chunks[0].WordOffset)
	}
	for i := 1; i < len(chunks); i++ {
		if chunks[i].WordOffset <= chunks[i-1].WordOffset {
			t.Fatalf("expected strictly increasing WordOffset, got %d then %d at index %d",
				chunks[i-1].WordOffset, chunks[i].WordOffset, i)
		}
	}
}
