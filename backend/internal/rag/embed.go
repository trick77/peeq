package rag

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/trick77/llmwire"
	// For the shared logging vocabulary: the call identity the worker puts on
	// the context, the heartbeat, and the token formatting, so an embed line
	// reads like a chat line. llm depends on nothing in peeq, so no cycle.
	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/sched"
)

// EmbedModel is the deployment every vector in this database was produced by.
//
// A constant, not configuration, for the same reason the chat model is: the
// width of every vector column, the similarity index and every stored chunk are
// all built to THIS model's output, so it is a property of the build. It used
// to be BACKEND_EMBED_MODEL, with the width in a second variable that had to
// agree with it — and when the two disagreed the only signal was a warning at
// boot while the vector table was already stale. Pinning the model lets the
// width come from its profile instead, so there is one fact and nothing to
// keep in step with it.
//
// Changing this is a corpus rebuild, and NOT by recreating the database: the
// vec_chunks DDL in store/migrations/0001_init.sql carries the width as a
// literal, as a migration that has run must, so a fresh database would come back
// at the old width. A model change needs a new migration that rebuilds the table
// at EmbedDim(). store's TestVecChunksWidthMatchesTheEmbeddingModel is what
// keeps the literal and the profile equal, and the dim-guard in cmd/peeq says
// so at boot when a stored table predates the change.
const EmbedModel = "text-embedding-3-small"

// embedProfile is the registry's description of EmbedModel. Resolved once, at
// init, and a failure is a panic on purpose: the id above is compiled in, so if
// llmwire has no profile for it that is a build error in everything but name,
// and the first test to import this package says so.
var embedProfile = mustEmbedProfile()

func mustEmbedProfile() *llmwire.Profile {
	p, err := llmwire.Default().Lookup(EmbedModel)
	if err != nil {
		panic("rag: EmbedModel has no llmwire profile: " + err.Error())
	}
	if p.Endpoint != llmwire.EndpointEmbeddings {
		panic("rag: EmbedModel is not an embeddings model: " + EmbedModel)
	}
	return p
}

// EmbedDim is the width of every vector EmbedModel returns, and therefore what
// the vec_chunks table must be built to. Read from the model's profile; the one
// other place it appears is the migration DDL, and a test holds the two equal.
func EmbedDim() int { return embedProfile.Embedding.DefaultDimensions }

// defaultEmbedTimeout bounds one embeddings call end to end. Embeddings are
// not streamed, so unlike the chat client there is no answer to cut off
// mid-way, and one cap on the whole call is the honest bound.
const defaultEmbedTimeout = 1 * time.Minute

// EmbedConfig configures the embedding client. BaseURL and APIKey override
// EmbedModel's profile: left empty, llmwire uses the host its profile ships
// and reads LLMWIRE_OPENAI_API_KEY itself, and a test points BaseURL at its
// fake. Logger is optional and defaults to
// slog.Default(). HeartbeatInterval is how often an in-flight request logs
// that it is still waiting (0 uses llm.DefaultHeartbeat; negative disables it).
type EmbedConfig struct {
	BaseURL           string
	APIKey            string
	Logger            *slog.Logger
	HeartbeatInterval time.Duration
}

// EmbedClient generates embeddings via an OpenAI-compatible /embeddings endpoint.
type EmbedClient struct {
	wire      *llmwire.Client
	log       *slog.Logger
	heartbeat time.Duration
}

// NewEmbedClient builds an EmbedClient. hc is optional. The error is a missing
// LLMWIRE_OPENAI_API_KEY, named.
func NewEmbedClient(cfg EmbedConfig, hc *http.Client) (*EmbedClient, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = llm.DefaultHeartbeat
	}
	wire, err := llmwire.FromEnv(EmbedModel, llmwire.Config{
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
		log:       cfg.Logger,
		heartbeat: cfg.HeartbeatInterval,
	}, nil
}

// Model names the deployment this client embeds against. The answer trace has
// to say which model turned the question into a vector.
func (c *EmbedClient) Model() string { return EmbedModel }

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

	resp, warnings, err := c.wire.Embed(ctx, llmwire.EmbedRequest{Model: EmbedModel, Inputs: inputs})
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
		if start > 0 && gap > 0 {
			// Context-aware: a shutdown mid-backfill should stop here rather
			// than sleep out the remaining batches.
			if !sched.Sleep(ctx, gap) {
				return nil, ctx.Err()
			}
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
