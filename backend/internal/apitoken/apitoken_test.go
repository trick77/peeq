package apitoken

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenerate_hasThePrefixAndExpectedLength(t *testing.T) {
	// Given: a deterministic reader with 32 bytes available.
	r := bytes.NewReader(bytes.Repeat([]byte{0xAB}, 32))

	// When
	got, err := Generate(r)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Then: peeq_ + 43 base64url chars (32 bytes, unpadded) = 48.
	if !strings.HasPrefix(got, Prefix) {
		t.Fatalf("token %q lacks prefix %q", got, Prefix)
	}
	if len(got) != 48 {
		t.Fatalf("len(token) = %d, want 48 (token=%q)", len(got), got)
	}
	if strings.Contains(got, "=") {
		t.Fatalf("token %q contains base64 padding, want unpadded", got)
	}
}

func TestGenerate_isDeterministicForAGivenReader(t *testing.T) {
	a, err := Generate(bytes.NewReader(bytes.Repeat([]byte{0x01}, 32)))
	if err != nil {
		t.Fatalf("Generate a: %v", err)
	}
	b, err := Generate(bytes.NewReader(bytes.Repeat([]byte{0x01}, 32)))
	if err != nil {
		t.Fatalf("Generate b: %v", err)
	}
	if a != b {
		t.Fatalf("same reader bytes gave different tokens: %q vs %q", a, b)
	}

	c, err := Generate(bytes.NewReader(bytes.Repeat([]byte{0x02}, 32)))
	if err != nil {
		t.Fatalf("Generate c: %v", err)
	}
	if a == c {
		t.Fatalf("different reader bytes gave the same token %q", a)
	}
}

func TestGenerate_errorsWhenTheReaderIsShort(t *testing.T) {
	// Given: only 8 bytes available where 32 are required.
	_, err := Generate(bytes.NewReader([]byte("12345678")))

	// Then: it must fail rather than silently produce a weak token.
	if err == nil {
		t.Fatalf("Generate with a short reader returned nil error")
	}
}

func TestVerify_acceptsOnlyTheMatchingToken(t *testing.T) {
	token, err := Generate(bytes.NewReader(bytes.Repeat([]byte{0x07}, 32)))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	hash := Hash(token)

	if !Verify(token, hash) {
		t.Fatalf("Verify rejected the token that produced the hash")
	}
	if Verify(token+"x", hash) {
		t.Fatalf("Verify accepted a token with a trailing character")
	}
	if Verify("peeq_totally-different-value-that-is-wrong", hash) {
		t.Fatalf("Verify accepted an unrelated token")
	}
}

func TestVerify_neverAcceptsAnEmptyStoredHash(t *testing.T) {
	// An unconfigured peeq must not authenticate anything, including a
	// request that presents the empty string.
	if Verify("", "") {
		t.Fatalf("Verify accepted empty token against empty stored hash")
	}
	if Verify("peeq_anything", "") {
		t.Fatalf("Verify accepted a token against an empty stored hash")
	}
}

func TestHash_isStableAndNotThePlaintext(t *testing.T) {
	const token = "peeq_abc"
	h1 := Hash(token)
	h2 := Hash(token)
	if h1 != h2 {
		t.Fatalf("Hash is not stable: %q vs %q", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("len(Hash) = %d, want 64 hex chars", len(h1))
	}
	if strings.Contains(h1, token) {
		t.Fatalf("hash %q contains the plaintext token", h1)
	}
}
