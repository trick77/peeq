package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestEmbedReturnsVectorsInInputOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 1, "embedding": []float32{0.2, 0.2}},
				{"index": 0, "embedding": []float32{0.1, 0.1}},
			},
		})
	}))
	defer srv.Close()
	c := NewEmbedClient(EmbedConfig{BaseURL: srv.URL, Model: "e5"}, srv.Client())
	vecs, err := c.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 2 || vecs[0][0] != 0.1 || vecs[1][0] != 0.2 {
		t.Fatalf("vectors misaligned: %v", vecs)
	}
}

// EmbedBatched exists because chapter chunks roughly doubled how many texts one
// video contributes, and the whole set used to ride in a single request under a
// one-minute timeout.
func TestEmbedBatchedSplitsAndPreservesOrder(t *testing.T) {
	var requests int
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		requests++
		sizes = append(sizes, len(req.Input))
		// Encode each input's index into its vector so order can be checked.
		data := make([]map[string]any, 0, len(req.Input))
		for i, in := range req.Input {
			n, _ := strconv.Atoi(strings.TrimPrefix(in, "t"))
			data = append(data, map[string]any{"index": i, "embedding": []float32{float32(n)}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	c := NewEmbedClient(EmbedConfig{BaseURL: srv.URL, Model: "m"}, srv.Client())
	inputs := make([]string, 150)
	for i := range inputs {
		inputs[i] = "t" + strconv.Itoa(i)
	}
	vecs, err := c.EmbedBatched(context.Background(), inputs, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != len(inputs) {
		t.Fatalf("got %d vectors for %d inputs", len(vecs), len(inputs))
	}
	if requests != 3 {
		t.Errorf("requests = %d (sizes %v), want 3 for 150 inputs at 64 per batch", requests, sizes)
	}
	// Order across batch boundaries is what a chunk-to-vector mapping depends on.
	for i, v := range vecs {
		if int(v[0]) != i {
			t.Fatalf("vector %d carries value %v — order was not preserved", i, v[0])
		}
	}
}

func TestEmbedBatchedSingleRequestBelowThreshold(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := make([]map[string]any, 0, len(req.Input))
		for i := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float32{1}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	c := NewEmbedClient(EmbedConfig{BaseURL: srv.URL, Model: "m"}, srv.Client())
	if _, err := c.EmbedBatched(context.Background(), []string{"a", "b"}, 0); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want 1 — a small set must not pay for batching", requests)
	}
}

// A partial vector set cannot be stored (ReplaceVideoChunks needs one vector
// per row), so any failing batch must fail the whole call.
func TestEmbedBatchedFailsWholeCallOnBatchError(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		data := make([]map[string]any, 0, len(req.Input))
		for i := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float32{1}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	c := NewEmbedClient(EmbedConfig{BaseURL: srv.URL, Model: "m"}, srv.Client())
	inputs := make([]string, 130)
	for i := range inputs {
		inputs[i] = "t"
	}
	if _, err := c.EmbedBatched(context.Background(), inputs, 0); err == nil {
		t.Fatal("a failing batch must fail the whole call")
	}
}
