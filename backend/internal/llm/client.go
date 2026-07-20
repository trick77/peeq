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
	"time"
)

const (
	model           = "mimo-v2.5-pro"
	reasoningEffort = "high"
	defaultTimeout  = 5 * time.Minute
	maxErrorBody    = 4 << 10
)

// Config configures the chat client. BaseURL is the OpenAI-compatible root
// (the client appends /chat/completions). APIKey is optional.
type Config struct {
	BaseURL string
	APIKey  string
}

// Message is one chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client calls an OpenAI-compatible /chat/completions endpoint.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient builds a Client. hc is optional (a 5-minute-timeout client is used
// when nil).
func NewClient(cfg Config, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{baseURL: strings.TrimRight(cfg.BaseURL, "/"), apiKey: cfg.APIKey, http: hc}
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
