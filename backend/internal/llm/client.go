// Package llm is peeq's lean OpenAI-compatible chat client. The host comes
// from the model's llmwire profile and the key from the env var that profile
// names (LLMWIRE_ZAI_API_KEY), read by llmwire.FromEnv in NewClient. The
// opencode identity, where a host needs it, is the provider's in llmwire. The model below is a
// real upstream model identifier sent on the wire, not a config name — it is
// deliberately NOT renamed alongside those env vars.
//
// The upstream is Z.ai (api.z.ai/api/paas/v4), which cannot be asked to skip
// reasoning: GLM-5.3-Flash rejects thinking:{"type":"disabled"} outright with
// code 1210, "This model always engages in thinking and cannot be disabled;
// please use low, high, or max". reasoning_effort is the only depth control —
// see calloptions.go for the tiers and for Shallow, the one step that asks for
// the shallowest of them. llmwire renders it; nothing about thinking is sent
// by hand.
// Modeled on loom's llm/client.go, minus loom's tool/vision/streaming machinery.
package llm

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/trick77/llmwire"
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
	// highReasoningEffort has exactly one caller, the streamed Ask answer (see
	// httpapi/answer_handlers.go), and is exported as HighReasoningEffort for it.
	// It is a named const because WithReasoningEffort takes a raw string and this
	// is the one place that knows which strings the endpoint accepts.
	reasoningEffort = maxReasoningEffort

	// Z.ai's recommended sampling settings for GLM-5.3-Flash live in llmwire's
	// profile now, as its recommended values, and it sends them whenever a caller
	// expresses no preference — which peeq never does. They are sent rather than
	// omitted because this endpoint's own fallbacks are lower (around 0.5 and
	// 0.7), so leaving them out does not mean "the model's defaults", it means
	// running the model off its recommended operating point. The values are
	// asserted on the wire in client_test.go.
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
	pacedLogThreshold  = time.Second
	maxRawUsage        = 1 << 10
)

// chatProfile is llmwire's description of model. Resolved once, at init, and a
// failure is a panic on purpose, for the same reason rag does it for the
// embedding model: the id is compiled in, so a missing profile is a build error
// in everything but name, and the first test to import this package says so.
var chatProfile = mustChatProfile()

func mustChatProfile() *llmwire.Profile {
	p, err := llmwire.Default().Lookup(model)
	if err != nil {
		panic("llm: model has no llmwire profile: " + err.Error())
	}
	return p
}

// MaxOutputTokens is the completion cap the model itself enforces, from its
// profile. A WithMaxTokens value above it is not an error on the wire — the
// endpoint just ignores the excess — so the callers that pin a cap assert
// against this in their tests instead.
func MaxOutputTokens() int { return int(chatProfile.Limits.MaxOutput) }

// wantsJSONObject reports whether this call asked to be constrained to JSON. The
// wire shape is llmwire's to render; what stays here is the decision.
func wantsJSONObject(ctx context.Context) bool { return jsonObjectFrom(ctx) }

// Config configures the chat client. BaseURL and APIKey override the env vars
// the model's profile names; left empty (the production case) llmwire reads
// those vars itself, and a test points BaseURL at its fake. RequestInterval
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
	// wire is the one llmwire client every call goes through. One, not one per
	// call: on a provider that presents as opencode, that identity carries a
	// session id llmwire mints and rotates itself — building a client per call
	// would mint a session per call, which is not what a session is.
	wire      *llmwire.Client
	interval  time.Duration
	log       *slog.Logger
	heartbeat time.Duration

	mu     sync.Mutex
	nextAt time.Time // earliest time the next request may start
}

// NewClient builds a Client. hc is optional: left nil, llmwire builds one with
// NO whole-request timeout (see the consts above — it would cut a stream
// mid-answer) and a ResponseHeaderTimeout sitting 30s later than the header
// bound, so the named failure wins the race against the transport's generic
// one.
//
// The error is a missing LLMWIRE_ZAI_API_KEY, named.
func NewClient(cfg Config, hc *http.Client) (*Client, error) {
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
	wire, err := llmwire.FromEnv(model, llmwire.Config{
		BaseURL:       cfg.BaseURL,
		APIKey:        cfg.APIKey,
		HeaderTimeout: cfg.HeaderTimeout,
		IdleTimeout:   cfg.StreamIdleTimeout,
		CallTimeout:   cfg.CallTimeout,
		HTTPClient:    hc,
	})
	if err != nil {
		return nil, err
	}
	return &Client{
		wire:      wire,
		interval:  cfg.RequestInterval,
		log:       cfg.Logger,
		heartbeat: cfg.HeartbeatInterval,
	}, nil
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

// The request struct that used to live here is gone: llmwire renders the body
// now, from the fields chatRequestFor fills in. What it sent is still asserted,
// on the wire, by the tests in client_test.go — the body is the contract, not the
// struct that produced it.
//
// Two absences that were deliberate and still are, recorded because a future
// reader will wonder. No tool_stream: it streams tool CALL arguments as they are
// generated and peeq sends no tools, so there is nothing for it to stream.

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

// usageFromWire folds llmwire's decoded accounting into this package's Usage.
// llmwire's lanes are pointers — nil is "not reported" — and Total says whether
// either token side arrived, so a reported zero stays a zero without this
// package re-deriving that from the pointers.
//
// Accounted keys on Total's ok, not on Reported: Reported also fires on a usage
// object that carries no token lane at all (a bare total_tokens, an empty
// details object), and banking such a call as 0/0/0 would log zeros as complete
// sums and write them to the video row. A call counts as accounted only when a
// token count did arrive.
func usageFromWire(w llmwire.Usage) Usage {
	u := Usage{
		Requests:         1,
		PromptTokens:     llmwire.Tokens(w.Input.Total),
		CachedTokens:     llmwire.Tokens(w.Input.CacheRead),
		CompletionTokens: llmwire.Tokens(w.Output.Total),
		ReasoningTokens:  llmwire.Tokens(w.Output.Reasoning),
	}
	var ok bool
	if u.TotalTokens, ok = w.Total(); ok {
		u.Accounted = 1
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
// than waiting for it. The returned string is still the whole answer, so a
// caller that streams and a caller that buffers see exactly the same text.
//
// onDelta now runs on the CALLING goroutine, not the socket reader: llmwire
// reads ahead into an unbounded queue precisely so a slow consumer cannot stop
// the idle guard being re-armed and get itself reported as a stalled model. A
// callback that blocks therefore delays this call and nothing else — it no
// longer risks killing the stream — but it is still the wrong place for work.
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
	wireReq := chatRequestFor(ctx, messages)

	// Accept-Encoding stays unset so net/http keeps negotiating and decompressing
	// gzip transparently. Setting it by hand would hand us a compressed body to
	// decode ourselves, mid-stream. llmwire leaves it alone for the same reason.
	started := time.Now()
	c.log.Debug("llm: request start", append(info.LogAttrs(),
		"model", modelFrom(ctx), "messages", len(messages),
		"reasoning_effort", reasoningEffortFrom(ctx))...)

	var counters streamCounters
	stop := StartHeartbeatFunc(ctx, c.log, c.heartbeat, "llm: still waiting for response",
		counters.attrs, info.LogAttrs()...)
	defer stop()

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

	// One call, one error path. llmwire names the bound that gave up — "no
	// response headers within 1m0s", "stream idle for 1m30s", "exceeded the 15m0s
	// call cap" — and surfaces a non-2xx as a typed error carrying the status and
	// the endpoint's own message, so the classification this package used to do
	// by hand arrives already done.
	res, err := c.runStream(ctx, c.wire, wireReq, &counters, onDelta)
	for _, w := range res.warnings {
		// DEBUG, not WARN. A warning here means llmwire coerced something or
		// could not price the call, and the commonest by far is "this response
		// carried no usage object" — which this package already reports once, as
		// `llm: no usage reported`, and which happens on every failed call. At
		// WARN it would put a second line on every one of those and bury the
		// failures that matter.
		c.log.Debug("llm: wire warning", append(info.LogAttrs(),
			"kind", string(w.Kind), "feature", w.Feature, "details", w.Details)...)
	}
	if err != nil {
		return fail(err)
	}

	// A finish_reason other than "stop" means the endpoint ended the answer on
	// its own terms — "length" being a completion cut off at a token limit,
	// i.e. a genuinely partial summary. It is not treated as an error, because
	// retrying an answer the model chose to truncate would just truncate again,
	// and the non-streaming client accepted it silently too. But it stops being
	// silent: a half summary that nobody can explain later is worse than a
	// warning nobody reads.
	if res.wire.FinishReason != "" && res.wire.FinishReason != "stop" {
		c.log.Warn("llm: answer ended early", append(info.LogAttrs(),
			"finish_reason", res.wire.FinishReason, "chars", res.chars)...)
	}

	// Inference is measured from `started`, which is taken after pace()
	// returns, so the deliberate gap between calls is accounted separately
	// instead of inflating the model's apparent latency.
	inference := time.Since(started)
	usage := usageFromWire(res.wire.Usage)
	usage.InferenceNanos = int64(inference)
	usage.PacedNanos = int64(pacedFor)
	TotalsFrom(ctx).Add(usage)

	if len(res.wire.Usage.Raw) > 0 {
		c.log.Debug("llm: usage raw", append(info.LogAttrs(), "usage", llmwire.Truncate(string(res.wire.Usage.Raw), maxRawUsage))...)
	} else {
		c.log.Debug("llm: no usage reported", info.LogAttrs()...)
	}
	// chat_inference_ms comes from usage.LogAttrs below and is this call's
	// duration, so printing duration_ms here too would be the same number
	// twice. status and the stream counts are what this line adds on top of
	// the accounting.
	attrs := append(info.LogAttrs(),
		"chunks", res.events, "finish_reason", res.wire.FinishReason)
	c.log.Debug("llm: request done", append(attrs, usage.LogAttrs()...)...)
	// Opt-in: a caller that must not persist a truncated answer (the single-pass
	// summary) turns a refusal/filter early-end into an error so the job retries,
	// rather than accepting the partial content. Accounting above still ran, so
	// the tokens this call spent are recorded either way. A deterministic cut is
	// tolerated: "length" is our own max_tokens, and Z.ai's
	// "model_context_window_exceeded" is the prompt itself being too big — both
	// come back identical on every attempt, so an error here would only walk the
	// job down its backoff ladder to the same partial answer. The warn line above
	// still says the answer is partial.
	if failOnEarlyFinishFrom(ctx) && res.wire.FinishReason != "" &&
		res.wire.FinishReason != "stop" && !deterministicCut(res.wire.FinishReason) {
		return "", fmt.Errorf("chat ended early: finish_reason=%s", res.wire.FinishReason)
	}
	return res.wire.Content, nil
}

// deterministicCut reports whether a finish_reason is a cut that every retry
// would repeat: the caller's own max_tokens ("length") or the model's context
// window ("model_context_window_exceeded", a Z.ai vocabulary word llmwire's
// profile records). A refusal or filter is NOT one — the same prompt can pass
// on the next attempt — which is what FailOnEarlyFinish exists to retry.
func deterministicCut(finishReason string) bool {
	return finishReason == "length" || finishReason == "model_context_window_exceeded"
}

// Naming which bound gave up now happens in llmwire, which owns the guard: it
// returns "no response headers within 1m0s", "stream idle for 1m30s" or
// "exceeded the 15m0s call cap" rather than a bare "context canceled". That
// classification was this package's deliverable when it streamed by hand, and it
// is preserved verbatim in the tests below — the strings are asserted, not the
// mechanism.
