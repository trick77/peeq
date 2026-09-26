package rag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/trick77/llmwire"
	"github.com/trick77/llmwire/llmwiretest"
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
	c := mustEmbedClient(t, EmbedConfig{BaseURL: srv.URL}, srv.Client())
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

	c := mustEmbedClient(t, EmbedConfig{BaseURL: srv.URL}, srv.Client())
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

	c := mustEmbedClient(t, EmbedConfig{BaseURL: srv.URL}, srv.Client())
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

	c := mustEmbedClient(t, EmbedConfig{BaseURL: srv.URL}, srv.Client())
	inputs := make([]string, 130)
	for i := range inputs {
		inputs[i] = "t"
	}
	if _, err := c.EmbedBatched(context.Background(), inputs, 0); err == nil {
		t.Fatal("a failing batch must fail the whole call")
	}
}

// The model is configuration; its width is its llmwire profile's. Both are
// what the worker's IndexMeta and the boot width check read.
func TestEmbedClient_modelAndWidthComeFromConfigAndProfile(t *testing.T) {
	c := mustEmbedClient(t, EmbedConfig{BaseURL: "http://example.invalid"}, nil)
	if c.Model() != llmwiretest.EmbedModel {
		t.Errorf("Model() = %q, want the configured %q", c.Model(), llmwiretest.EmbedModel)
	}
	if c.Dim() != llmwiretest.EmbedDimensions {
		t.Errorf("Dim() = %d, want the profile's %d", c.Dim(), llmwiretest.EmbedDimensions)
	}
}

// A missing, unknown or non-embeddings model is refused at construction, named
// by its env var, with the choices that would work.
func TestNewEmbedClient_refusesAModelThatIsNotAnEmbeddingModel(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		want        []string
	}{
		{"missing", "", []string{"BACKEND_EMBED_MODEL", "required", llmwiretest.EmbedModel}},
		{"unknown", "no-such-model", []string{"BACKEND_EMBED_MODEL", "no-such-model"}},
		{"chat model", llmwiretest.ChatModel, []string{"BACKEND_EMBED_MODEL", "not embeddings", llmwiretest.EmbedModel}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewEmbedClient(EmbedConfig{Model: tc.model, Registry: llmwiretest.Registry(),
				BaseURL: "http://example.invalid"}, nil)
			if err == nil {
				t.Fatal("NewEmbedClient accepted it")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("err = %q, want it to mention %q", err, w)
				}
			}
		})
	}
}

// The request goes out under the configured model id.
func TestEmbed_sendsTheConfiguredModel(t *testing.T) {
	srv := llmwiretest.NewServer(t)
	c := mustEmbedClient(t, EmbedConfig{BaseURL: srv.URL}, srv.Server.Client())
	if _, err := c.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if got := srv.Last().Model(); got != llmwiretest.EmbedModel {
		t.Errorf("model on the wire = %q, want %q", got, llmwiretest.EmbedModel)
	}
}

func TestCheckVecWidth(t *testing.T) {
	if err := CheckVecWidth(8, "m", 8); err != nil {
		t.Fatalf("matching widths refused: %v", err)
	}
	err := CheckVecWidth(1536, "m", 8)
	if err == nil {
		t.Fatal("a width mismatch must refuse boot")
	}
	for _, w := range []string{"1536", "8", "m", "BACKEND_EMBED_MODEL", "re-index"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("err = %q, want it to mention %q", err, w)
		}
	}
}

// mustEmbedClient is NewEmbedClient for a test whose EmbedConfig names its
// fake server, so the only way it can fail is a bug in the constructor.
func mustEmbedClient(t testing.TB, cfg EmbedConfig, hc *http.Client) *EmbedClient {
	t.Helper()
	if cfg.Model == "" {
		cfg.Model = llmwiretest.EmbedModel
	}
	if cfg.Registry == nil {
		cfg.Registry = llmwiretest.Registry()
	}
	c, err := NewEmbedClient(cfg, hc)
	if err != nil {
		t.Fatalf("NewEmbedClient: %v", err)
	}
	return c
}

// With no BaseURL the constructor takes the host from the profile and asks
// llmwire for the key variable; a missing one comes back named rather than as
// a client that dials "".
func TestNewEmbedClient_withoutBaseURLNamesTheMissingVariable(t *testing.T) {
	t.Setenv("LLMWIRE_LLMWIRETEST_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("LLMWIRE_LLMWIRETEST_API_KEY", "")
	_, err := NewEmbedClient(EmbedConfig{Model: llmwiretest.EmbedModel, Registry: llmwiretest.Registry()}, nil)
	var me *llmwire.MissingEnvError
	if !errors.As(err, &me) || !strings.Contains(err.Error(), "LLMWIRE_LLMWIRETEST_API_KEY") {
		t.Fatalf("got %v", err)
	}
}

// Same contract as the chat client: the phrasing is this package's, the chain
// is llmwire's, so a caller can still classify the failure by errors.Is.
func TestEmbed_statusErrorsKeepLlmwiresChain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"slow down"}}`))
	}))
	defer srv.Close()
	c := mustEmbedClient(t, EmbedConfig{BaseURL: srv.URL}, srv.Client())
	_, err := c.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); got != "embedding failed with status 429: slow down" {
		t.Errorf("err = %q, the phrasing changed", got)
	}
	if !errors.Is(err, llmwire.ErrRateLimited) {
		t.Errorf("errors.Is(err, ErrRateLimited) = false; the chain to llmwire is cut")
	}
}

// TestEmbedBatchedStopsBetweenBatchesOnCancel: a shutdown during a backfill
// stops at the next gap instead of sending the remaining batches.
func TestEmbedBatchedStopsBetweenBatchesOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		requests++
		cancel() // the process is shutting down while this batch is answered
		data := make([]map[string]any, 0, len(req.Input))
		for i := range req.Input {
			data = append(data, map[string]any{"index": i, "embedding": []float32{0}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()

	c := mustEmbedClient(t, EmbedConfig{BaseURL: srv.URL}, srv.Client())
	inputs := make([]string, 150)
	for i := range inputs {
		inputs[i] = "t" + strconv.Itoa(i)
	}
	_, err := c.EmbedBatched(ctx, inputs, time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1: no batch may be sent after the cancel", requests)
	}
}
