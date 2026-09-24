package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/videos"
)

func TestDecodeJSON(t *testing.T) {
	type body struct {
		Name *string `json:"name"`
	}
	t.Run("well-formed body decodes", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x"}`))
		rec := httptest.NewRecorder()
		var b body
		if !decodeJSON(rec, req, &b, 1<<10, "bad") {
			t.Fatalf("decode refused: %d %s", rec.Code, rec.Body.String())
		}
		if b.Name == nil || *b.Name != "x" {
			t.Fatalf("decoded %+v", b)
		}
	})
	t.Run("malformed body is a 400 with the caller's message", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{`))
		rec := httptest.NewRecorder()
		var b body
		if decodeJSON(rec, req, &b, 1<<10, "name is required") {
			t.Fatal("malformed body accepted")
		}
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "name is required") {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("oversized body is a 413", func(t *testing.T) {
		big := `{"name":"` + strings.Repeat("x", 2<<10) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
		rec := httptest.NewRecorder()
		var b body
		if decodeJSON(rec, req, &b, 1<<10, "bad") {
			t.Fatal("oversized body accepted")
		}
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", rec.Code)
		}
	})
}

func TestDecodeJSONLenient(t *testing.T) {
	type body struct {
		On *bool `json:"on"`
	}
	t.Run("empty and malformed bodies are no preference", func(t *testing.T) {
		for _, raw := range []string{"", "{", "null"} {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(raw))
			rec := httptest.NewRecorder()
			var b body
			if !decodeJSONLenient(rec, req, &b, 1<<10) {
				t.Fatalf("%q refused: %d", raw, rec.Code)
			}
			if b.On != nil {
				t.Fatalf("%q decoded a value: %+v", raw, b)
			}
		}
	})
	t.Run("oversized body is still a 413", func(t *testing.T) {
		big := `{"on":true,"pad":"` + strings.Repeat("x", 2<<10) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(big))
		rec := httptest.NewRecorder()
		var b body
		if decodeJSONLenient(rec, req, &b, 1<<10) {
			t.Fatal("oversized body accepted")
		}
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want 413", rec.Code)
		}
	})
}

// TestRequestBodyLimit_endToEnd pins the bound through real routes: a strict
// one, a lenient one, and the two cookie routes, which take a bigger body
// than the rest (a Netscape jar) but not an unbounded one.
func TestRequestBodyLimit_endToEnd(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	fw := &fakeWorker{}
	deps.Worker = fw
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	send := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	// Well-formed JSON that is simply too long: the limit, not the parser,
	// is what refuses it.
	big := `{"category":"` + strings.Repeat("x", maxJSONBody) + `"}`
	for _, path := range []string{"/api/videos/v1/category", "/api/videos/v1/favorite"} {
		if rec := send(http.MethodPost, path, big); rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s: status = %d, want 413, body = %s", path, rec.Code, rec.Body.String())
		}
	}
	// A real jar can be far bigger than a JSON toggle: one over the general
	// limit is still accepted on the cookie route...
	jar := validYouTubeCookieBody
	for len(jar) < maxJSONBody+1024 {
		jar += ".youtube.com\tTRUE\t/\tTRUE\t1789000000\tPREF\t" + strings.Repeat("y", 100) + "\n"
	}
	body, _ := json.Marshal(map[string]string{"cookie": jar})
	if rec := send(http.MethodPut, "/api/settings/cookie", string(body)); rec.Code != http.StatusOK {
		t.Fatalf("large jar: status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	// ...and one over the cookie limit is not, through the middleware ceiling
	// as much as the route's own bound.
	huge, _ := json.Marshal(map[string]string{"cookie": validYouTubeCookieBody + strings.Repeat("z", maxCookieBody)})
	if rec := send(http.MethodPut, "/api/settings/cookie", string(huge)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("huge jar: status = %d, want 413, body = %s", rec.Code, rec.Body.String())
	}
}

func TestDecodeJSONOptional(t *testing.T) {
	type body struct {
		At string `json:"at"`
	}
	cases := []struct {
		raw  string
		ok   bool
		code int
	}{
		{"", true, 0},
		{`{"at":"x"}`, true, 0},
		{"{", false, http.StatusBadRequest},
		{`{"at":"` + strings.Repeat("x", 2<<10) + `"}`, false, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.raw))
		rec := httptest.NewRecorder()
		var b body
		if got := decodeJSONOptional(rec, req, &b, 1<<10, "bad"); got != tc.ok {
			t.Fatalf("%q: ok = %v, want %v (status %d)", tc.raw[:min(len(tc.raw), 12)], got, tc.ok, rec.Code)
		}
		if !tc.ok && rec.Code != tc.code {
			t.Fatalf("%q: status = %d, want %d", tc.raw[:min(len(tc.raw), 12)], rec.Code, tc.code)
		}
	}
}
