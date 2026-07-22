package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	// Only for FormatTokens, so embedding and chat token counts read the same
	// way in the log. llm depends on nothing in peeq, so this cannot cycle.
	"github.com/trick77/peeq/internal/llm"
)

const (
	defaultEmbedTimeout = 1 * time.Minute
	maxEmbedErrorBody   = 4 << 10
)

// EmbedConfig configures the OpenAI-compatible embedding client. Logger is
// optional and defaults to slog.Default().
type EmbedConfig struct {
	BaseURL string
	APIKey  string
	Model   string
	Logger  *slog.Logger
}

// EmbedClient generates embeddings via an OpenAI-compatible /embeddings endpoint.
type EmbedClient struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
	log     *slog.Logger
}

// NewEmbedClient builds an EmbedClient. hc is optional.
func NewEmbedClient(cfg EmbedConfig, hc *http.Client) *EmbedClient {
	if hc == nil {
		hc = &http.Client{Timeout: defaultEmbedTimeout}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &EmbedClient{baseURL: strings.TrimRight(cfg.BaseURL, "/"), apiKey: cfg.APIKey, model: cfg.Model, http: hc, log: cfg.Logger}
}

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	// Usage is optional; embedding endpoints report only input tokens, and
	// some report nothing at all.
	Usage struct {
		PromptTokens int64 `json:"prompt_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
	} `json:"usage"`
}

// Embed returns one vector per input, aligned to input order. Empty input yields
// no vectors and no request.
func (c *EmbedClient) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(embedRequest{Model: c.model, Input: inputs})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	started := time.Now()
	fail := func(err error) ([][]float32, error) {
		c.log.Warn("embed: request failed", "inputs", len(inputs), "duration_ms", time.Since(started).Milliseconds(), "err", err)
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fail(fmt.Errorf("embed request: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxEmbedErrorBody))
		return fail(fmt.Errorf("embedding failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg))))
	}
	var parsed embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fail(fmt.Errorf("decode embed response: %w", err))
	}
	if len(parsed.Data) != len(inputs) {
		return fail(fmt.Errorf("embedding count mismatch: got %d, want %d", len(parsed.Data), len(inputs)))
	}
	c.log.Debug("embed: request done", "inputs", len(inputs), "duration_ms", time.Since(started).Milliseconds(),
		"embed_tokens_in", llm.FormatTokens(parsed.Usage.PromptTokens),
		"embed_tokens_total", llm.FormatTokens(parsed.Usage.TotalTokens))
	sort.Slice(parsed.Data, func(i, j int) bool { return parsed.Data[i].Index < parsed.Data[j].Index })
	out := make([][]float32, len(parsed.Data))
	for i, d := range parsed.Data {
		out[i] = d.Embedding
	}
	return out, nil
}
