package tokenauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStaticToken_acceptsTheConfiguredSecret(t *testing.T) {
	// Given
	reached := false
	mw := NewStaticTokenMiddleware("s3cret-value")
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer s3cret-value")
	rec := httptest.NewRecorder()

	// When
	mw.RequireToken(okHandler(&reached)).ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK || !reached {
		t.Fatalf("status = %d reached = %v, want 200 and the handler reached", rec.Code, reached)
	}
}

func TestStaticToken_rejectsWithBearerChallenge(t *testing.T) {
	cases := map[string]string{
		"missing":      "",
		"wrong secret": "Bearer nope",
		"prefix only":  "Bearer s3cret",
		"wrong scheme": "Token s3cret-value",
		"empty bearer": "Bearer ",
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			// Given
			reached := false
			mw := NewStaticTokenMiddleware("s3cret-value")
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()

			// When
			mw.RequireToken(okHandler(&reached)).ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if reached {
				t.Fatal("protected handler reached without a valid token")
			}
			if got := rec.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Errorf("WWW-Authenticate = %q, want Bearer", got)
			}
		})
	}
}

// An empty configured secret must never match an empty or absent credential.
func TestStaticToken_emptySecretFailsClosed(t *testing.T) {
	reached := false
	mw := NewStaticTokenMiddleware("")
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer x")
	rec := httptest.NewRecorder()

	mw.RequireToken(okHandler(&reached)).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized || reached {
		t.Fatalf("status = %d reached = %v, want 401 and not reached", rec.Code, reached)
	}
}
