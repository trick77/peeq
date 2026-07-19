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
