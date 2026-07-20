// Package apitoken generates and verifies peeq's single machine API token.
//
// The token is write-only: only its SHA-256 hash is ever persisted (see
// settings.SetAPITokenHash), and the plaintext exists solely in the HTTP
// response that creates it. There is deliberately no way to recover a token
// after that response — losing it means generating a new one.
//
// SHA-256 rather than bcrypt/argon2 is correct here: those exist to slow
// brute-force attacks against low-entropy human passwords, whereas this
// token is 32 bytes of crypto/rand. A fast hash costs nothing in security
// and keeps per-request verification cheap.
package apitoken

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
)

// Prefix marks a peeq API token, making it greppable in logs and
// recognizable when pasted into the wrong field.
const Prefix = "peeq_"

// tokenBytes is the raw entropy per token, before encoding.
const tokenBytes = 32

// Generate reads tokenBytes of entropy from r and returns a prefixed,
// base64url-encoded token. Callers pass crypto/rand.Reader in production and
// a deterministic reader in tests. A short read is an error: a truncated
// token would be a weak credential.
func Generate(r io.Reader) (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("read token entropy: %w", err)
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// Hash returns the lowercase hex SHA-256 of token. This is the only form of
// the token that is ever persisted.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Verify reports whether presented is the token behind storedHash, using a
// constant-time compare so a caller cannot recover the hash byte by byte
// from response timing.
//
// An empty storedHash always fails: peeq without a generated token must
// never authenticate a request, including one presenting the empty string.
func Verify(presented, storedHash string) bool {
	if storedHash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(Hash(presented)), []byte(storedHash)) == 1
}
