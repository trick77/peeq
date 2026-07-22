// Package llm is peeq's lean OpenAI-compatible chat client, configured via
// BACKEND_CHAT_BASE_URL and BACKEND_CHAT_API_KEY. The model below is a real
// upstream model identifier sent on the wire, not a config name — it is
// deliberately NOT renamed alongside those env vars. It targets
// reasoning_effort=high on every call (an offline summarization job, so latency
// is free and quality is the priority). Modeled on loom's llm/client.go, minus
// loom's tool/vision/streaming machinery.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	model             = "mimo-v2.5-pro"
	reasoningEffort   = "high"
	defaultTimeout    = 5 * time.Minute
	maxErrorBody      = 4 << 10
	pacedLogThreshold = time.Second
	maxRawUsage       = 1 << 10
)

// Config configures the chat client. BaseURL is the OpenAI-compatible root
// (the client appends /chat/completions). APIKey is optional. RequestInterval
// is the minimum gap between requests — breathing room for a slow or
// rate-limited endpoint; 0 disables it. Logger defaults to slog.Default().
// HeartbeatInterval is how often an in-flight request logs that it is still
// waiting (0 uses the default; negative disables the heartbeat).
type Config struct {
	BaseURL           string
	APIKey            string
	RequestInterval   time.Duration
	Logger            *slog.Logger
	HeartbeatInterval time.Duration
}

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client calls an OpenAI-compatible /chat/completions endpoint.
type Client struct {
	baseURL   string
	apiKey    string
	http      *http.Client
	interval  time.Duration
	log       *slog.Logger
	heartbeat time.Duration

	mu     sync.Mutex
	nextAt time.Time // earliest time the next request may start
}

// NewClient builds a Client. hc is optional (a 5-minute-timeout client is used
// when nil).
func NewClient(cfg Config, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = DefaultHeartbeat
	}
	return &Client{
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:    cfg.APIKey,
		http:      hc,
		interval:  cfg.RequestInterval,
		log:       cfg.Logger,
		heartbeat: cfg.HeartbeatInterval,
	}
}

// pace blocks until at least RequestInterval has elapsed since the previous
// request began, spacing calls out for a slow/rate-limited endpoint. It
// reserves the slot under the mutex so concurrent callers still serialize with
// the gap. Returns how long it actually blocked (for logging) and the context
// error if cancelled while waiting.
func (c *Client) pace(ctx context.Context) (time.Duration, error) {
	if c.interval <= 0 {
		return 0, nil
	}
	c.mu.Lock()
	start := time.Now()
	if !c.nextAt.IsZero() && c.nextAt.After(start) {
		start = c.nextAt
	}
	c.nextAt = start.Add(c.interval)
	c.mu.Unlock()

	wait := time.Until(start)
	if wait <= 0 {
		return 0, nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-t.C:
		return wait, nil
	}
}

type chatRequest struct {
	Model           string    `json:"model"`
	Messages        []Message `json:"messages"`
	ReasoningEffort string    `json:"reasoning_effort"`
	Stream          bool      `json:"stream"`
}

// chatUsage mirrors the OpenAI-compatible `usage` object. Everything in it is
// optional — endpoints vary in how much of the breakdown they report, and a
// missing field must read as "not reported", not as an error.
type chatUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u chatUsage) toUsage() Usage {
	return Usage{
		Requests:         1,
		Reported:         u.reported(),
		PromptTokens:     u.PromptTokens,
		CachedTokens:     u.PromptTokensDetails.CachedTokens,
		CompletionTokens: u.CompletionTokens,
		ReasoningTokens:  u.CompletionTokensDetails.ReasoningTokens,
		TotalTokens:      u.TotalTokens,
	}
}

// reported says the endpoint sent a usage object with something in it, so its
// zeros are answers rather than silence. Mirrors loom's TokenUsage.Present.
func (u chatUsage) reported() bool {
	return u.PromptTokens != 0 || u.CompletionTokens != 0 || u.TotalTokens != 0 ||
		u.PromptTokensDetails.CachedTokens != 0 || u.CompletionTokensDetails.ReasoningTokens != 0
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	// Kept verbatim rather than decoded in place: the same bytes then feed both
	// chatUsage and the debug line that shows what the endpoint really sent,
	// which is what settles whether a zero is the endpoint's answer or a field
	// name we do not know about. (Two struct fields cannot share one json tag.)
	Usage json.RawMessage `json:"usage"`
}

// usage decodes the raw usage object; an absent or malformed one yields the
// zero chatUsage, i.e. "not reported", never an error.
func (r chatResponse) usage() chatUsage {
	var u chatUsage
	if len(r.Usage) > 0 {
		_ = json.Unmarshal(r.Usage, &u)
	}
	return u
}

// Complete runs a single non-streaming chat completion and returns the first
// choice's content. It logs the call against whatever CallInfo the caller
// attached to ctx (see callinfo.go): a debug line on start and finish, an info
// heartbeat while the endpoint is still thinking, and a warning on failure.
func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
	info := CallFrom(ctx)
	pacedFor, err := c.pace(ctx)
	if err != nil {
		return "", err
	}
	if pacedFor >= pacedLogThreshold {
		// Distinguish our own deliberate spacing from a slow endpoint: without
		// this, RequestInterval looks like latency.
		c.log.Debug("llm: paced", append(info.LogAttrs(), "waited_ms", pacedFor.Milliseconds())...)
	}
	body, err := json.Marshal(chatRequest{Model: model, Messages: messages, ReasoningEffort: reasoningEffort, Stream: false})
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	started := time.Now()
	c.log.Debug("llm: request start", append(info.LogAttrs(), "messages", len(messages), "request_bytes", len(body))...)
	stop := StartHeartbeat(ctx, c.log, c.heartbeat, "llm: still waiting for response", info.LogAttrs()...)
	defer stop()

	fail := func(err error) (string, error) {
		c.log.Warn("llm: request failed", append(info.LogAttrs(), "duration_ms", time.Since(started).Milliseconds(), "err", err)...)
		return "", err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fail(fmt.Errorf("chat request: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return fail(fmt.Errorf("chat failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg))))
	}
	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return fail(fmt.Errorf("decode chat response: %w", err))
	}
	if len(parsed.Choices) == 0 {
		return fail(fmt.Errorf("chat response had no choices"))
	}

	// Inference is measured from `started`, which is taken after pace()
	// returns, so the deliberate gap between calls is accounted separately
	// instead of inflating the model's apparent latency.
	inference := time.Since(started)
	usage := parsed.usage().toUsage()
	usage.InferenceNanos = int64(inference)
	usage.PacedNanos = int64(pacedFor)
	info.Totals.Add(usage)

	if len(parsed.Usage) > 0 {
		c.log.Debug("llm: usage raw", append(info.LogAttrs(), "usage", truncate(string(parsed.Usage), maxRawUsage))...)
	} else {
		c.log.Debug("llm: no usage reported", info.LogAttrs()...)
	}
	attrs := append(info.LogAttrs(), "duration_ms", inference.Milliseconds(), "status", resp.StatusCode)
	c.log.Debug("llm: request done", append(attrs, usage.LogAttrs()...)...)
	return parsed.Choices[0].Message.Content, nil
}

// truncate caps a log value, marking it so a cut is never mistaken for the
// endpoint's own output.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}
