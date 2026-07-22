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
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	model           = "mimo-v2.5-pro"
	reasoningEffort = "high"
	defaultTimeout  = 5 * time.Minute
	maxErrorBody    = 4 << 10
)

// Config configures the chat client. BaseURL is the OpenAI-compatible root
// (the client appends /chat/completions). APIKey is optional. RequestInterval
// is the minimum gap between requests — breathing room for a slow or
// rate-limited endpoint; 0 disables it.
type Config struct {
	BaseURL         string
	APIKey          string
	RequestInterval time.Duration
}

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client calls an OpenAI-compatible /chat/completions endpoint.
type Client struct {
	baseURL  string
	apiKey   string
	http     *http.Client
	interval time.Duration

	mu     sync.Mutex
	nextAt time.Time // earliest time the next request may start
}

// NewClient builds a Client. hc is optional (a 5-minute-timeout client is used
// when nil).
func NewClient(cfg Config, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{
		baseURL:  strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:   cfg.APIKey,
		http:     hc,
		interval: cfg.RequestInterval,
	}
}

// pace blocks until at least RequestInterval has elapsed since the previous
// request began, spacing calls out for a slow/rate-limited endpoint. It
// reserves the slot under the mutex so concurrent callers still serialize with
// the gap. Returns the context error if cancelled while waiting.
func (c *Client) pace(ctx context.Context) error {
	if c.interval <= 0 {
		return nil
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
		return nil
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type chatRequest struct {
	Model           string    `json:"model"`
	Messages        []Message `json:"messages"`
	ReasoningEffort string    `json:"reasoning_effort"`
	Stream          bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
}

// Complete runs a single non-streaming chat completion and returns the first
// choice's content.
func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
	if err := c.pace(ctx); err != nil {
		return "", err
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
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("chat request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return "", fmt.Errorf("chat failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode chat response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("chat response had no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}
