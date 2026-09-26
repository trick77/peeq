package rag

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/trick77/llmwire"
	// For the shared logging vocabulary: the call identity the worker puts on
	// the context, the heartbeat, and the token formatting, so an embed line
	// reads like a chat line. llm depends on nothing in peeq, so no cycle.
	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/sched"
)

// The embedding model is configuration (BACKEND_EMBED_MODEL); its vector width
// is its llmwire profile's. Every stored vector and the vec_chunks table are
// built to one width, and the migration DDL carries it as a literal, so a model
// of a different width is refused at boot (CheckVecWidth) rather than mixed
// into a table built for another. Switching to one is a re-index: a migration
// that rebuilds vec_chunks at the new width, then every video re-embedded.

// embeddingModels lists the registry's embeddings models, for the "valid
// choices" of a configuration error.
func embeddingModels(reg *llmwire.Registry) []string {
	var out []string
	for _, id := range reg.Models() {
		if _, err := reg.LookupEmbedding(id); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// CheckVecWidth refuses a vector table built at a width other than the one the
// configured embedding model returns. Mixing widths is not a degraded search,
// it is every insert failing, so boot stops here with the remedy.
func CheckVecWidth(built int, model string, dim int) error {
	if built == dim {
		return nil
	}
	return fmt.Errorf("vec_chunks is built for %d-wide vectors, but BACKEND_EMBED_MODEL=%s returns %d; "+
		"either set BACKEND_EMBED_MODEL back to a %d-wide model, or re-index: ship a migration that "+
		"rebuilds vec_chunks at %d and re-embed every video", built, model, dim, built, dim)
}

// defaultEmbedTimeout bounds one embeddings call end to end. Embeddings are
// not streamed, so unlike the chat client there is no answer to cut off
// mid-way, and one cap on the whole call is the honest bound.
const defaultEmbedTimeout = 1 * time.Minute

// EmbedConfig configures the embedding client. Model is the embedding model
// id (BACKEND_EMBED_MODEL), looked up in Registry (llmwire's default when nil).
// BaseURL and APIKey override the model's profile: left empty, llmwire uses the
// host its profile ships and reads the provider's key variable itself, and a
// test points BaseURL at its fake. Logger is optional and defaults to
// slog.Default(). HeartbeatInterval is how often an in-flight request logs
// that it is still waiting (0 uses llm.DefaultHeartbeat; negative disables it).
type EmbedConfig struct {
	Model             string
	Registry          *llmwire.Registry
	BaseURL           string
	APIKey            string
	Logger            *slog.Logger
	HeartbeatInterval time.Duration
}

// EmbedClient generates embeddings via an OpenAI-compatible /embeddings endpoint.
type EmbedClient struct {
	wire      *llmwire.Client
	model     string
	dim       int
	log       *slog.Logger
	heartbeat time.Duration
}

// NewEmbedClient builds an EmbedClient. hc is optional. The error names what
// is wrong: a model missing, unknown or not an embeddings model (with the valid
// choices), or the provider key variable llmwire could not read.
func NewEmbedClient(cfg EmbedConfig, hc *http.Client) (*EmbedClient, error) {
	if cfg.Registry == nil {
		cfg.Registry = llmwire.Default()
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("BACKEND_EMBED_MODEL is required; valid choices are %s",
			strings.Join(embeddingModels(cfg.Registry), ", "))
	}
	profile, err := cfg.Registry.LookupEmbedding(cfg.Model)
	if err != nil {
		return nil, fmt.Errorf("BACKEND_EMBED_MODEL: %w; valid choices are %s",
			err, strings.Join(embeddingModels(cfg.Registry), ", "))
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = llm.DefaultHeartbeat
	}
	wire, err := llmwire.FromEnv(cfg.Model, llmwire.Config{
		Registry:   cfg.Registry,
		BaseURL:    cfg.BaseURL,
		APIKey:     cfg.APIKey,
		HTTPClient: hc,
		// The whole-call cap is the bound that matters on a non-streamed
		// route; the header and idle bounds sit underneath it and only name
		// which phase went quiet when it does.
		CallTimeout: defaultEmbedTimeout,
	})
	if err != nil {
		return nil, err
	}
	return &EmbedClient{
		wire:      wire,
		model:     cfg.Model,
		dim:       profile.Embedding.DefaultDimensions,
		log:       cfg.Logger,
		heartbeat: cfg.HeartbeatInterval,
	}, nil
}

// Model names the model this client embeds against. The answer trace has to
// say which model turned the question into a vector.
func (c *EmbedClient) Model() string { return c.model }

// Dim is the width of every vector Model returns, from its profile, and
// therefore what vec_chunks must be built to.
func (c *EmbedClient) Dim() int { return c.dim }

// Embed returns one vector per input, aligned to input order. Empty input yields
// no vectors and no request.
//
// ONE log record per call, success or failure — a hard invariant the logging
// tests pin. llmwire's own warnings are folded into that record rather than
// logged separately, because a second line per call would break the count and
// bury the failures that matter.
func (c *EmbedClient) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
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

	resp, warnings, err := c.wire.Embed(ctx, llmwire.EmbedRequest{Model: c.model, Inputs: inputs})
	if err != nil {
		return fail(embedError(err))
	}
	// llmwire places each vector by the response's own index and refuses a
	// count mismatch, a gap or a repeat, so what comes back is already aligned
	// to the inputs; nothing is re-checked here.

	// One token figure, not two. An embeddings call has no completion side, so
	// the endpoint's total_tokens equals its prompt_tokens — and llmwire's merged
	// usage carries only the input lane, so there is nothing else to read anyway.
	// The old embed_tokens_total was the same number under a second name.
	attrs := append(ident, "duration_ms", time.Since(started).Milliseconds(),
		"embed_tokens_in", llm.FormatTokens(llmwire.Tokens(resp.Usage.Input.Total)))
	if len(warnings) > 0 {
		attrs = append(attrs, "warnings", llmwire.Warnings(warnings))
	}
	c.log.Debug("embed: request done", attrs...)
	return resp.Vectors, nil
}

// embedError keeps this package's error phrasing over llmwire's, for the same
// reason the chat client does: the strings are what the summarize worker's
// activity log and the operator runbook quote.
func embedError(err error) error {
	var apiErr *llmwire.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode != 0 {
		return llm.Rephrase(err, fmt.Sprintf("embedding failed with status %d: %s", apiErr.StatusCode, apiErr.Message))
	}
	switch {
	case errors.Is(err, llmwire.ErrMalformedResponse):
		return fmt.Errorf("decode embed response: %w", err)
	case errors.Is(err, llmwire.ErrResponseShape):
		return fmt.Errorf("embedding count mismatch: %w", err)
	}
	return fmt.Errorf("embed request: %w", err)
}

// maxEmbedInputs caps how many texts ride in one /embeddings request.
//
// A whole video went in a single call before chapter chunks existed, under a
// one-minute HTTP timeout — already the tightest thing in this package for a
// long video. Chapter chunks roughly double the count, and the backfill sends
// every video in the library through here, so the request has to be bounded.
//
// llmwire batches at the same size internally, so a call at or under this
// count is exactly one request; the split here exists for the gap between
// batches, which llmwire deliberately does not model.
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
		// Context-aware: a shutdown mid-backfill stops here rather than sleep
		// out the remaining batches. A non-positive gap is no wait at all.
		if start > 0 && !sched.Sleep(ctx, gap) {
			return nil, ctx.Err()
		}
		end := min(start+maxEmbedInputs, len(inputs))
		vecs, err := c.Embed(ctx, inputs[start:end])
		if err != nil {
			return nil, fmt.Errorf("embed batch %d-%d: %w", start, end, err)
		}
		out = append(out, vecs...)
	}
	return out, nil
}
