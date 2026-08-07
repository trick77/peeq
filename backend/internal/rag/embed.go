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

	// For the shared logging vocabulary: the call identity the worker puts on
	// the context, the heartbeat, and the token formatting, so an embed line
	// reads like a chat line. llm depends on nothing in peeq, so no cycle.
	"github.com/trick77/peeq/internal/llm"
)

const (
	defaultEmbedTimeout = 1 * time.Minute
	maxEmbedErrorBody   = 4 << 10
)

// EmbedConfig configures the OpenAI-compatible embedding client. Logger is
// optional and defaults to slog.Default(). HeartbeatInterval is how often an
// in-flight request logs that it is still waiting (0 uses
// llm.DefaultHeartbeat; negative disables it).
type EmbedConfig struct {
	BaseURL           string
	APIKey            string
	Model             string
	Logger            *slog.Logger
	HeartbeatInterval time.Duration
}

// EmbedClient generates embeddings via an OpenAI-compatible /embeddings endpoint.
type EmbedClient struct {
	baseURL   string
	apiKey    string
	model     string
	http      *http.Client
	log       *slog.Logger
	heartbeat time.Duration
}

// NewEmbedClient builds an EmbedClient. hc is optional.
func NewEmbedClient(cfg EmbedConfig, hc *http.Client) *EmbedClient {
	if hc == nil {
		hc = &http.Client{Timeout: defaultEmbedTimeout}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = llm.DefaultHeartbeat
	}
	return &EmbedClient{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"), apiKey: cfg.APIKey, model: cfg.Model,
		http: hc, log: cfg.Logger, heartbeat: cfg.HeartbeatInterval,
	}
}

// Model names the deployment this client embeds against. It is configuration,
// not a secret — the same string already rides on every request body — and the
// answer trace has to say which model turned the question into a vector.
func (c *EmbedClient) Model() string { return c.model }

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

	// The worker embeds inside a step context carrying the video's identity, so
	// every embed line names the video the same way the chat lines do.
	ident := append(llm.CallFrom(ctx).LogAttrs(), "inputs", len(inputs))
	started := time.Now()
	fail := func(err error) ([][]float32, error) {
		c.log.Warn("embed: request failed", append(ident, "duration_ms", time.Since(started).Milliseconds(), "err", err)...)
		return nil, err
	}

	// A stalled embedding endpoint would otherwise be silent for the whole
	// minute-long timeout.
	stop := llm.StartHeartbeat(ctx, c.log, c.heartbeat, "embed: still waiting for response", ident...)
	defer stop()

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
	c.log.Debug("embed: request done", append(ident, "duration_ms", time.Since(started).Milliseconds(),
		"embed_tokens_in", llm.FormatTokens(parsed.Usage.PromptTokens),
		"embed_tokens_total", llm.FormatTokens(parsed.Usage.TotalTokens))...)
	sort.Slice(parsed.Data, func(i, j int) bool { return parsed.Data[i].Index < parsed.Data[j].Index })
	out := make([][]float32, len(parsed.Data))
	for i, d := range parsed.Data {
		out[i] = d.Embedding
	}
	return out, nil
}

// maxEmbedInputs caps how many texts ride in one /embeddings request.
//
// A whole video went in a single call before chapter chunks existed, under a
// one-minute HTTP timeout — already the tightest thing in this package for a
// long video. Chapter chunks roughly double the count, and the backfill sends
// every video in the library through here, so the request has to be bounded.
const maxEmbedInputs = 64

// EmbedBatched is Embed for input sets large enough that one request would be
// unwise: it splits into requests of at most maxEmbedInputs, concatenates the
// vectors in input order, and waits `gap` between requests so a library-wide
// backfill trickles rather than bursting at the endpoint.
//
// A non-positive gap disables the wait. Any batch failing fails the whole call:
// a partial vector set cannot be stored, since ReplaceVideoChunks requires one
// vector per row.
func (c *EmbedClient) EmbedBatched(ctx context.Context, inputs []string, gap time.Duration) ([][]float32, error) {
	if len(inputs) <= maxEmbedInputs {
		return c.Embed(ctx, inputs)
	}
	out := make([][]float32, 0, len(inputs))
	for start := 0; start < len(inputs); start += maxEmbedInputs {
		if start > 0 && gap > 0 {
			// Context-aware: a shutdown mid-backfill should stop here rather
			// than sleep out the remaining batches.
			t := time.NewTimer(gap)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}
		end := min(start+maxEmbedInputs, len(inputs))
		vecs, err := c.Embed(ctx, inputs[start:end])
		if err != nil {
			return nil, fmt.Errorf("embed batch %d-%d: %w", start, end, err)
		}
		if len(vecs) != end-start {
			return nil, fmt.Errorf("embed batch %d-%d: got %d vectors for %d inputs", start, end, len(vecs), end-start)
		}
		out = append(out, vecs...)
	}
	return out, nil
}
