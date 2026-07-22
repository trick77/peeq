package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompleteSendsModelAndEffortAndReturnsContent(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("missing auth header")
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		io.WriteString(w, `{"choices":[{"message":{"content":"hello world"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	out, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello world" {
		t.Fatalf("content = %q", out)
	}
	if gotBody["model"] != "mimo-v2.5-pro" {
		t.Fatalf("model = %v", gotBody["model"])
	}
	if gotBody["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v", gotBody["reasoning_effort"])
	}
	if gotBody["stream"] != false {
		t.Fatalf("stream = %v", gotBody["stream"])
	}
	_ = strings.TrimSpace
}

func TestCompleteErrorsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewClient(Config{BaseURL: srv.URL}, srv.Client())
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestComplete_pacesRequestsByInterval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, RequestInterval: 100 * time.Millisecond}, srv.Client())
	start := time.Now()
	for i := 0; i < 2; i++ {
		if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
			t.Fatalf("complete %d: %v", i, err)
		}
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("two paced calls took %v, want >= the 100ms interval between them", elapsed)
	}
}

func TestComplete_zeroIntervalDoesNotPace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL}, srv.Client()) // RequestInterval 0
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("complete: %v", err)
	}
}
