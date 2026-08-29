// Package llm is peeq's lean OpenAI-compatible chat client, configured via
// BACKEND_CHAT_BASE_URL and BACKEND_CHAT_API_KEY. The model below is a real
// upstream model identifier sent on the wire, not a config name — it is
// deliberately NOT renamed alongside those env vars.
//
// The upstream is Z.ai (api.z.ai/api/paas/v4), which cannot be asked to skip
// reasoning: GLM-5.3-Flash rejects thinking:{"type":"disabled"} outright with
// code 1210, "This model always engages in thinking and cannot be disabled;
// please use low, high, or max". So thinking is sent enabled on every call and
// reasoning_effort is the only depth control — see calloptions.go for the tiers
// and thinking.go for the one step that asks for the shallowest of them.
// Modeled on loom's llm/client.go, minus loom's tool/vision/streaming machinery.
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
	"unicode/utf8"
)

// Three bounds replace the single whole-request timeout this client used while
// it was non-streaming. That one timeout had to cover both "the endpoint never
// answered" and "the model is thinking hard", so it could only ever be wrong
// for one of them — and when it fired, five minutes in, the log could not say
// which had happened. Streaming separates them: headers arrive in seconds, and
// silence afterwards is measurable per event.
//
// A whole-request http.Client.Timeout is deliberately NOT set: it caps body
// reads too, so it would cut a legitimately long stream mid-answer.
const (
	model = "glm-5.3-flash"
	// shortGateModel is the deployment the short gates reach (see ShortGate) —
	// the steps whose answer is a lookup rather than a deduction. Today Z.ai
	// serves those from the same model as everything else, so this is the same
	// id twice and ShortGate changes nothing on the wire.
	//
	// It stays a separate const anyway, named for the use rather than for a model
	// class and written out rather than derived from model, so that pointing the
	// gates at a different deployment later moves one use without silently moving
	// the other. Under MiMo these differed: mimo-v2.5 was picked over -pro
	// because it queued less, not because it was weaker.
	shortGateModel = "glm-5.3-flash"
	// The three reasoning depths GLM-5.3-Flash accepts. They are the ONLY values
	// it takes — none, minimal, medium and xhigh are rejected — and thinking
	// cannot be switched off at all (see the package doc).
	//
	// Measured against api.z.ai on peeq's own call shapes, reasoning tokens and
	// wall clock:
	//
	//	                     low          high          max
	//	classify             0 / 1.0s     15 / 1.3s     107 / 3.5s
	//	understand           53 / 2.5s    54 / 2.5s     345 / 7.4s
	//	summary (6.7k tok)   —            18 / 8.5s     144 / 11.0s
	//	keypoints (12.5k)    18 / 7.9s    192 / 12.8s   3183 / 69.9s
	//	ask answer           —            80 / 4.4s     355 / 9.6s
	//
	// The model scales its reasoning to the task, so even max stays far inside
	// every max_tokens cap in the codebase. Time is the cost that varies, not
	// tokens — note keypoints at 70s, and understand at 7.4s against a 10s
	// timeout, which is the one place a deep call cannot go.
	lowReasoningEffort  = "low"
	highReasoningEffort = "high"
	maxReasoningEffort  = "max"
	// reasoningEffort is what a call gets when it asks for nothing. max is Z.ai's
	// own default and their documented recommendation for this model, so peeq
	// sends it rather than quietly running the model shallower than it was tuned
	// for. Only Shallow opts out, and only because of a hard timeout.
	//
	// highReasoningEffort has no caller today. It is named because
	// WithReasoningEffort takes a raw string and this is the one place that knows
	// which strings the endpoint accepts.
	reasoningEffort = maxReasoningEffort

	// Z.ai's recommended sampling settings for GLM-5.3-Flash. Sent explicitly
	// because the endpoint's own fallbacks are lower (around 0.5 and 0.7), so
	// omitting them does not mean "the model's defaults" — it means running it
	// off its recommended operating point.
	chatTemperature = 1.0
	chatTopP        = 0.95
	// defaultHeaderTimeout is how long the endpoint may take to send response
	// headers. Generous next to the ~2.5s observed, because it competes with
	// nothing — a stall costs a minute now instead of five.
	defaultHeaderTimeout = 60 * time.Second
	// defaultIdleTimeout is how long a started stream may go completely silent.
	// Any event re-arms it, including reasoning deltas and keepalives, so this
	// bounds a dead socket rather than a slow model.
	defaultIdleTimeout = 90 * time.Second
	// defaultCallTimeout is the backstop for a stream that stays alive forever
	// without finishing — dribbling keepalives past any sane summary length. The
	// summarize worker sets no deadline of its own, so without this there would
	// be no cap at all.
	defaultCallTimeout = 15 * time.Minute
	maxErrorBody       = 4 << 10
	pacedLogThreshold  = time.Second
	maxRawUsage        = 1 << 10
)

// Config configures the chat client. BaseURL is the OpenAI-compatible root
// (the client appends /chat/completions). APIKey is optional. RequestInterval
// is the minimum gap between requests — breathing room for a slow or
// rate-limited endpoint; 0 disables it. Logger defaults to slog.Default().
// HeartbeatInterval is how often an in-flight request logs that it is still
// waiting (0 uses the default; negative disables the heartbeat).
// StreamIdleTimeout is how long a started stream may go silent before the call
// is abandoned, HeaderTimeout how long the endpoint may take to answer at all,
// and CallTimeout the cap on the whole call; each uses its default above when
// left at 0.
//
// All three are settable, but only StreamIdleTimeout is wired to an
// environment variable — the other two exist as fields so a test can drive them
// without mutating package state, which is the difference between a test that
// proves the header bound fires and a test that waits sixty real seconds.
type Config struct {
	BaseURL           string
	APIKey            string
	RequestInterval   time.Duration
	Logger            *slog.Logger
	HeartbeatInterval time.Duration
	StreamIdleTimeout time.Duration
	HeaderTimeout     time.Duration
	CallTimeout       time.Duration
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
	idle      time.Duration
	header    time.Duration
	cap       time.Duration

	mu     sync.Mutex
	nextAt time.Time // earliest time the next request may start
}

// NewClient builds a Client. hc is optional; the default has NO whole-request
// timeout (see the consts above — it would truncate a stream) and instead
// carries ResponseHeaderTimeout as a backstop under the stallGuard.
func NewClient(cfg Config, hc *http.Client) *Client {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = DefaultHeartbeat
	}
	if cfg.StreamIdleTimeout <= 0 {
		cfg.StreamIdleTimeout = defaultIdleTimeout
	}
	if cfg.HeaderTimeout <= 0 {
		cfg.HeaderTimeout = defaultHeaderTimeout
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = defaultCallTimeout
	}
	if hc == nil {
		// Built after the defaults are resolved, so the backstop matches the
		// bound the caller actually configured rather than silently reverting to
		// the package default.
		//
		// Clone the stdlib default rather than build a bare Transport, so proxy
		// support, dial timeouts and connection pooling stay at their tuned
		// values instead of being dropped.
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.ResponseHeaderTimeout = cfg.HeaderTimeout
		hc = &http.Client{Transport: tr}
	}
	return &Client{
		baseURL:   strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:    cfg.APIKey,
		http:      hc,
		interval:  cfg.RequestInterval,
		log:       cfg.Logger,
		heartbeat: cfg.HeartbeatInterval,
		idle:      cfg.StreamIdleTimeout,
		header:    cfg.HeaderTimeout,
		cap:       cfg.CallTimeout,
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

// No tool_stream here, which Z.ai also recommends for streaming: it streams tool
// CALL arguments as they are generated, and peeq sends no tools at all. There is
// nothing for it to stream.
type chatRequest struct {
	Model           string         `json:"model"`
	Messages        []Message      `json:"messages"`
	ReasoningEffort string         `json:"reasoning_effort"`
	Thinking        thinkingOption `json:"thinking"`
	Temperature     float64        `json:"temperature"`
	TopP            float64        `json:"top_p"`
	MaxTokens       int            `json:"max_tokens,omitempty"`
	Stream          bool           `json:"stream"`
}

// No stream_options here, deliberately. MiMo needed stream_options.include_usage
// to send the trailing usage chunk at all, without which every chat_tokens_*
// field went dark the moment streaming was switched on. Z.ai does not take the
// parameter — it is absent from their SDK's request schema — and does not need
// it: the final SSE frame carries usage unconditionally, before [DONE].
// Measured against api.z.ai, same prompt, streamed:
//
//	{"choices":[{"finish_reason":"stop",...}],
//	 "usage":{"prompt_tokens":15,"completion_tokens":16,"total_tokens":31,
//	          "prompt_tokens_details":{"cached_tokens":0},
//	          "completion_tokens_details":{"reasoning_tokens":12}}}
//
// Sending it anyway is accepted and ignored rather than rejected, but it is an
// undocumented field on this endpoint, so it is not sent.

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
		Accounted:        boolToCount(u.reported()),
		PromptTokens:     u.PromptTokens,
		CachedTokens:     u.PromptTokensDetails.CachedTokens,
		CompletionTokens: u.CompletionTokens,
		ReasoningTokens:  u.CompletionTokensDetails.ReasoningTokens,
		TotalTokens:      u.TotalTokens,
	}
}

// boolToCount turns "this call reported usage" into the counter Usage keeps,
// so a total can say how many of its calls were actually accounted for.
func boolToCount(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// reported says the endpoint sent a usage object with something in it, so its
// zeros are answers rather than silence. Mirrors loom's TokenUsage.Present.
func (u chatUsage) reported() bool {
	return u.PromptTokens != 0 || u.CompletionTokens != 0 || u.TotalTokens != 0 ||
		u.PromptTokensDetails.CachedTokens != 0 || u.CompletionTokensDetails.ReasoningTokens != 0
}

// usageFrom decodes a raw usage object; an absent, null or malformed one yields
// the zero chatUsage, i.e. "not reported", never an error.
//
// The bytes are carried around raw rather than decoded where they are found, so
// the same bytes feed both chatUsage and the debug line that shows what the
// endpoint really sent — which is what settles whether a zero is the endpoint's
// answer or a field name we do not know about.
func usageFrom(raw json.RawMessage) chatUsage {
	var u chatUsage
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &u)
	}
	return u
}

// Complete runs a single streamed chat completion and returns the concatenated
// content deltas. It logs the call against whatever CallInfo the caller
// attached to ctx (see callinfo.go): a debug line on start and finish, an info
// heartbeat carrying how much has arrived while the endpoint is still thinking,
// and a warning on failure naming which bound gave up.
//
// It streams internally but returns whole, so every Completer implementation
// and every caller in summarize is unaffected.
func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
	return c.CompleteStream(ctx, messages, nil)
}

// CompleteStream is Complete with a callback invoked for every content
// fragment as it arrives, for callers relaying the answer to a browser rather
// than waiting for it. onDelta runs on the reader goroutine, so it must not
// block; the returned string is still the whole answer, so a caller that
// streams and a caller that buffers see exactly the same text.
//
// Every bound, counter and log line is shared with Complete — there is one
// request path, not two.
func (c *Client) CompleteStream(ctx context.Context, messages []Message, onDelta func(string)) (string, error) {
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
	body, err := json.Marshal(chatRequest{
		Model: modelFrom(ctx), Messages: messages, ReasoningEffort: reasoningEffortFrom(ctx),
		Thinking: thinkingOptionFor(ctx), Temperature: chatTemperature, TopP: chatTopP,
		MaxTokens: maxTokensFrom(ctx), Stream: true,
	})
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}

	// Two nested contexts, because their failures mean different things and the
	// error has to say which: callCtx is the overall cap, reqCtx is what the
	// stallGuard cancels. Cancelling reqCtx leaves callCtx's deadline intact to
	// be distinguished from it afterwards.
	callCtx, cancelCall := context.WithTimeout(ctx, c.cap)
	defer cancelCall()
	reqCtx, cancelReq := context.WithCancel(callCtx)
	defer cancelReq()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", chatUserAgent)
	// Session headers pin the many calls one video costs to a single upstream
	// node. Both names carry the same value; the upstream sends the pair too.
	// Accept-Encoding is left unset on purpose so net/http keeps negotiating and
	// decompressing gzip transparently — setting it by hand would hand us a
	// compressed body to decode ourselves, mid-stream.
	sessionID := chatSessionID(info.VideoID)
	req.Header.Set("X-Session-Id", sessionID)
	req.Header.Set("X-Session-Affinity", sessionID)
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	started := time.Now()
	c.log.Debug("llm: request start", append(info.LogAttrs(),
		"model", modelFrom(ctx), "messages", len(messages), "request_bytes", len(body),
		"reasoning_effort", reasoningEffortFrom(ctx))...)

	var counters streamCounters
	stop := StartHeartbeatFunc(ctx, c.log, c.heartbeat, "llm: still waiting for response",
		counters.attrs, info.LogAttrs()...)
	defer stop()

	guard := newStallGuard(cancelReq, c.header, stallHeaders)
	defer guard.stop()

	// The failure line carries this call's OWN counts. Before streaming there
	// was nothing to report but a duration, so a reader reaching for numbers
	// found only the chat_* totals — which cover the calls that SUCCEEDED and
	// omit the failed one entirely (Totals is only added to on the success path
	// below). That mismatch is what made a 5-minute stall read as a 6-second
	// request; these attributes are the fix.
	fail := func(err error) (string, error) {
		c.log.Warn("llm: request failed", append(info.LogAttrs(),
			"duration_ms", time.Since(started).Milliseconds(),
			"chunks", counters.events.Load(), "chars", counters.chars.Load(),
			"err", err)...)
		return "", err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fail(fmt.Errorf("chat request: %w", c.explain(ctx, callCtx, guard, err)))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return fail(fmt.Errorf("chat failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg))))
	}
	// Headers are in, so the bound that matters from here is silence, not
	// arrival. Every event re-arms this inside readStream.
	guard.arm(c.idle, stallIdle)

	res, err := readStream(resp.Body, guard, &counters, c.idle, onDelta)
	if err != nil {
		return fail(fmt.Errorf("chat stream: %w", c.explain(ctx, callCtx, guard, err)))
	}

	// A finish_reason other than "stop" means the endpoint ended the answer on
	// its own terms — "length" being a completion cut off at a token limit,
	// i.e. a genuinely partial summary. It is not treated as an error, because
	// retrying an answer the model chose to truncate would just truncate again,
	// and the non-streaming client accepted it silently too. But it stops being
	// silent: a half summary that nobody can explain later is worse than a
	// warning nobody reads.
	if res.finishReason != "" && res.finishReason != "stop" {
		c.log.Warn("llm: answer ended early", append(info.LogAttrs(),
			"finish_reason", res.finishReason, "chars", res.chars)...)
	}

	// Inference is measured from `started`, which is taken after pace()
	// returns, so the deliberate gap between calls is accounted separately
	// instead of inflating the model's apparent latency.
	inference := time.Since(started)
	usage := usageFrom(res.rawUsage).toUsage()
	usage.InferenceNanos = int64(inference)
	usage.PacedNanos = int64(pacedFor)
	info.Totals.Add(usage)

	if len(res.rawUsage) > 0 {
		c.log.Debug("llm: usage raw", append(info.LogAttrs(), "usage", truncate(string(res.rawUsage), maxRawUsage))...)
	} else {
		c.log.Debug("llm: no usage reported", info.LogAttrs()...)
	}
	// chat_inference_ms comes from usage.LogAttrs below and is this call's
	// duration, so printing duration_ms here too would be the same number
	// twice. status and the stream counts are what this line adds on top of
	// the accounting.
	attrs := append(info.LogAttrs(), "status", resp.StatusCode,
		"chunks", res.events, "finish_reason", res.finishReason)
	c.log.Debug("llm: request done", append(attrs, usage.LogAttrs()...)...)
	// Opt-in: a caller that must not persist a truncated answer (the single-pass
	// summary) turns a refusal/filter early-end into an error so the job retries,
	// rather than accepting the partial content. Accounting above still ran, so
	// the tokens this call spent are recorded either way. "length" is tolerated
	// (that cut is our own max_tokens; retrying would just re-truncate).
	if failOnEarlyFinishFrom(ctx) && res.finishReason != "" &&
		res.finishReason != "stop" && res.finishReason != "length" {
		return "", fmt.Errorf("chat ended early: finish_reason=%s", res.finishReason)
	}
	return res.content, nil
}

// explain replaces the bare "context canceled" a cancelled request returns with
// the bound that actually gave up. Without it every one of these three failures
// looks identical in the log, which is the exact problem streaming was adopted
// to solve — so the classification, not the streaming, is the deliverable.
//
// Order matters: the guard is checked first because it cancels reqCtx directly,
// and a parent that is also done would otherwise mask it.
func (c *Client) explain(parent, call context.Context, guard *stallGuard, err error) error {
	if reason := guard.firedReason(); reason != "" {
		switch reason {
		case stallHeaders:
			return fmt.Errorf("%s within %s", reason, c.header)
		default:
			return fmt.Errorf("%s for %s", reason, c.idle)
		}
	}
	if call.Err() != nil && parent.Err() == nil {
		return fmt.Errorf("exceeded the %s call cap", c.cap)
	}
	return err
}

// truncate caps a log value, marking it so a cut is never mistaken for the
// endpoint's own output. It cuts on a rune boundary: a byte-offset cut can
// split a multi-byte character and put invalid UTF-8 in the log, which is not
// hypothetical against an endpoint that returns non-ASCII field values.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…(truncated)"
}
