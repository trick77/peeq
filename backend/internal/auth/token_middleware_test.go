package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/peeq/internal/apitoken"
)

// fakeTokens is a TokenHashLookup returning a fixed hash.
type fakeTokens struct{ hash string }

func (f fakeTokens) APITokenHash(context.Context) string { return f.hash }

// okHandler records that the protected handler was reached.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequireToken_acceptsAValidBearerToken(t *testing.T) {
	// Given
	const token = "peeq_valid-token-value"
	reached := false
	mw := NewTokenMiddleware(fakeTokens{hash: apitoken.Hash(token)})
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	// When
	mw.RequireToken(okHandler(&reached)).ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !reached {
		t.Fatalf("protected handler was not reached")
	}
}

func TestRequireToken_rejectsBadCredentials(t *testing.T) {
	const token = "peeq_valid-token-value"
	cases := []struct {
		name   string
		header string
		stored string
	}{
		{"wrong token", "Bearer peeq_wrong-token-value", apitoken.Hash(token)},
		{"missing header", "", apitoken.Hash(token)},
		{"empty bearer value", "Bearer ", apitoken.Hash(token)},
		{"wrong scheme", "Basic " + token, apitoken.Hash(token)},
		{"scheme only", "Bearer", apitoken.Hash(token)},
		{"no token configured", "Bearer " + token, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			reached := false
			mw := NewTokenMiddleware(fakeTokens{hash: tc.stored})
			req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()

			// When
			mw.RequireToken(okHandler(&reached)).ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if reached {
				t.Fatalf("protected handler was reached despite bad credentials")
			}
		})
	}
}

func TestRequireToken_doesNotPutAUserInTheContext(t *testing.T) {
	// A token request is not a session. Handlers behind RequireToken must
	// not be able to assume UserFromContext works.
	const token = "peeq_valid-token-value"
	var sawUser bool
	mw := NewTokenMiddleware(fakeTokens{hash: apitoken.Hash(token)})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawUser = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	mw.RequireToken(h).ServeHTTP(httptest.NewRecorder(), req)

	if sawUser {
		t.Fatalf("a user was present in the context of a token request")
	}
}
