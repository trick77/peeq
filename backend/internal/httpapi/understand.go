package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/trick77/peeq/internal/llm"
)

// Query understanding: the step that separates what a question is ABOUT from
// the words it is asked WITH.
//
// The problem it exists for. Ask embeds the sentence the reader typed, verbatim,
// and the keyword ladder ANDs its content terms. Neither has any notion that
// "what material about bike geometry do we have" is a question about bike
// geometry rather than about material. "material" is a real token in the query
// vector and a real term on three of the four keyword rungs, so a question
// phrased ABOUT THE LIBRARY searches for the words used to talk about libraries.
// The stopword list cannot fix this and should not try: it is deliberately
// conservative, and "material", "video" and "footage" are all words a reader
// might genuinely be searching for.
//
// So a small call reads the question first and reports two things: the topic,
// and whether the reader wants an answer or an inventory.
//
// WHAT IS DONE WITH THE TOPIC, and why it matters. The topic becomes an
// ADDITIONAL semantic lane beside the raw question — never a replacement for it.
// This is the whole safety property of the design. Rewriting is known to HURT
// retrieval when the query is already lexically aligned with the corpus, which
// is exactly what peeq's keyword ladder makes it; a rewrite that replaced the
// query would put that alignment at risk on every search. As a second lane, a
// good rewrite adds evidence, a bad one is outvoted by the four lanes that did
// not change, and the awkward question "does THIS query need rewriting?" never
// has to be answered at all.

// understandTimeout caps the pre-retrieval call. It is generous against a step
// measured in a second or so, and deliberately far below AskCallTimeout: this
// call sits in front of the first byte, so a slow one has to be abandoned rather
// than waited out. Falling back to the raw question costs a rewrite, not an
// answer.
const understandTimeout = 10 * time.Second

// understandMaxTokens bounds the reply. It is a topic phrase and a one-word
// label; anything longer is a model that has started explaining itself, and the
// parse will reject it anyway.
const understandMaxTokens = 200

// understandMaxTopicRunes rejects a "topic" long enough to be a paraphrase of
// the whole question. The point of the extra lane is a SHORT, topical query —
// one that sits closer to a chunk than a sentence of framing does. A model that
// hands back the original question with two words removed has not helped, and
// embedding it costs a lane slot for nothing.
const understandMaxTopicRunes = 80

// Intent labels. Deliberately two, and deliberately coarse: a label a short-gate
// model gets wrong is worse than no label, and these are the only two the answer
// path can actually do anything different about.
const (
	// intentContent — the reader wants an answer synthesized from what the
	// videos say. This is Ask's built shape and the safe default.
	intentContent = "content"
	// intentInventory — the reader wants to know WHAT THE LIBRARY HOLDS, not
	// what it says. "what material about bike geometry do we have" is this.
	intentInventory = "inventory"
)

// queryUnderstanding is what the call reports.
type queryUnderstanding struct {
	// Topic is the question with its framing removed: "bike geometry" from
	// "what material about bike geometry do we have". Empty when the question
	// carried no framing worth removing, when the model failed, or when there is
	// no understander wired — all of which mean "no extra lane", never an error.
	Topic string `json:"topic"`
	// Intent is one of the labels above. Always populated; defaults to
	// intentContent, because answering is the thing Ask can always do.
	Intent string `json:"intent"`
}

// understandStatus records how the step went, for the one log line. A silent
// fallback to the raw question is the failure most likely to go unnoticed —
// everything still works, just worse — so it has to be nameable.
type understandStatus string

const (
	understandOK       understandStatus = "ok"
	understandSkipped  understandStatus = "skipped" // no understander wired
	understandFailed   understandStatus = "failed"  // call or parse failed
	understandTimedOut understandStatus = "timeout"
	// understandNoop — the call succeeded and returned nothing worth using: an
	// empty topic, or one that merely repeats the question. Distinct from
	// "failed" because nothing went wrong; the question simply had no framing to
	// strip, which is the common case for a well-phrased query.
	understandNoop understandStatus = "noop"
)

// understandDiag is the step's contribution to the ask retrieval log line.
//
// No topic field: the topic reaches the log through askDiag, which is handed the
// same string as the retrieval input. Carrying a second copy here would give the
// line two sources for one fact, and they could disagree — askLanes is what
// decides whether the topic opened a lane at all.
type understandDiag struct {
	status understandStatus
	ms     int64
	intent string
}

const understandSystemPrompt = `You separate what a question is ABOUT from the words it is asked WITH.

You are given one question a reader typed into a personal video library. Reply with JSON and nothing else:

{"topic": "...", "intent": "content" | "inventory"}

topic — the subject of the question, as the words that would appear in a video about it. Drop everything that refers to the library itself or to the act of asking: what, do we have, is there, show me, tell me, material, videos, content, footage, clips, anything. Keep every word that carries subject matter, including ones that could look generic. If the question is already just its subject, repeat it unchanged.

intent — "inventory" when the reader is asking WHAT THE LIBRARY HOLDS ("what do we have on X", "any videos about X", "which videos cover X"). "content" when they want to know what the videos SAY ("how does X work", "what causes X", "why is X"). When it is genuinely both or you are unsure, answer "content".

Examples:
Q: what material about bike geometry do we have
{"topic": "bike geometry", "intent": "inventory"}
Q: how does head angle affect handling
{"topic": "head angle handling", "intent": "content"}
Q: do we have anything on sourdough starters
{"topic": "sourdough starter", "intent": "inventory"}
Q: what are transients
{"topic": "transients", "intent": "content"}

No explanation, no code fences, no other keys.`

// understandQuery runs the pre-retrieval call. It NEVER returns an error: every
// failure degrades to the raw question, which is exactly the behaviour Ask had
// before this step existed. The diag says which way it went.
func (s *server) understandQuery(ctx context.Context, q string) (queryUnderstanding, understandDiag) {
	// Not wired (chat unavailable, or a deployment that never configured it).
	// Ask still answers; it just answers the way it did before.
	if s.understand == nil {
		return queryUnderstanding{Intent: intentContent}, understandDiag{
			status: understandSkipped, intent: intentContent,
		}
	}

	cctx, cancel := context.WithTimeout(ctx, understandTimeout)
	defer cancel()
	// ShortGate and WithoutThinking say different things and both are needed:
	// the first asks for the deployment that answers sooner (Pro is where the
	// long thinking calls queue, and this call sits in front of the first byte),
	// the second stops the model reasoning about a question that is a labelling
	// job. See llm/calloptions.go. reasoning_effort is NOT the lever here — it is
	// measured inert against this endpoint.
	cctx = llm.ShortGate(llm.WithoutThinking(cctx))
	cctx = llm.WithMaxTokens(cctx, understandMaxTokens)
	cctx = llm.WithCall(cctx, llm.CallInfo{Step: "understand"})

	started := time.Now()
	raw, err := s.understand.Complete(cctx, []llm.Message{
		{Role: "system", Content: understandSystemPrompt},
		{Role: "user", Content: "Q: " + q},
	})
	elapsed := time.Since(started).Milliseconds()

	if err != nil {
		status := understandFailed
		// Either bound counts as a timeout: understandTimeout above, or the
		// llm.Client's own call cap, which fires on a context this function never
		// sees — so cctx.Err() alone would report that one as a plain failure.
		if errors.Is(err, context.DeadlineExceeded) || cctx.Err() == context.DeadlineExceeded {
			status = understandTimedOut
		}
		return queryUnderstanding{Intent: intentContent}, understandDiag{
			status: status, ms: elapsed, intent: intentContent,
		}
	}

	u, ok := parseUnderstanding(raw)
	if !ok {
		return queryUnderstanding{Intent: intentContent}, understandDiag{
			status: understandFailed, ms: elapsed, intent: intentContent,
		}
	}
	// A topic that merely restates the question buys nothing and costs a lane.
	//
	// Compare NORMALIZED to normalized. u.Topic has been through sanitizeTopic,
	// which collapses runs of whitespace; q has not. Trimming q alone lets
	// "what  are  transients" (a paste, or ?q=what++are++transients) slip past as
	// a "different" phrasing, and the topic lane then embeds the SAME sentence a
	// second time. That is not a harmless duplicate: FuseWeighted sums across
	// lanes, so one phrasing counted twice earns 1.2 and outscores a strict
	// keyword rung at 1.0 — the score that is supposed to mean two phrasings
	// agreeing on a passage.
	if u.Topic == "" || strings.EqualFold(u.Topic, strings.Join(strings.Fields(q), " ")) {
		u.Topic = ""
		return u, understandDiag{status: understandNoop, ms: elapsed, intent: u.Intent}
	}
	return u, understandDiag{
		status: understandOK, ms: elapsed, intent: u.Intent,
	}
}

// parseUnderstanding reads the model's reply. It is defensive in the two ways
// that actually happen: a JSON object wrapped in a code fence, and prose before
// or after it. Anything else is a failure, and a failure means the raw question.
func parseUnderstanding(raw string) (queryUnderstanding, bool) {
	body := strings.TrimSpace(raw)
	if body == "" {
		return queryUnderstanding{}, false
	}
	// Strip a fenced block, ```json or bare ```.
	if strings.HasPrefix(body, "```") {
		if i := strings.IndexByte(body, '\n'); i >= 0 {
			body = body[i+1:]
		}
		body = strings.TrimSuffix(strings.TrimSpace(body), "```")
	}
	// Take the outermost object, so leading or trailing prose is survivable.
	start := strings.IndexByte(body, '{')
	end := strings.LastIndexByte(body, '}')
	if start < 0 || end <= start {
		return queryUnderstanding{}, false
	}

	var parsed struct {
		Topic  string `json:"topic"`
		Intent string `json:"intent"`
	}
	if err := json.Unmarshal([]byte(body[start:end+1]), &parsed); err != nil {
		return queryUnderstanding{}, false
	}

	u := queryUnderstanding{
		Topic:  sanitizeTopic(parsed.Topic),
		Intent: intentContent,
	}
	// Only the label we know. An unrecognized one is not an error — the topic is
	// still usable — it just means the safe default applies.
	if strings.EqualFold(strings.TrimSpace(parsed.Intent), intentInventory) {
		u.Intent = intentInventory
	}
	return u, true
}

// sanitizeTopic reduces the returned topic to something safe to embed, to log
// and to show in the UI. It is model output that reaches a reader's screen, so
// it is treated like any other untrusted string: control characters out,
// whitespace collapsed, length bounded.
func sanitizeTopic(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len([]rune(s)) > understandMaxTopicRunes {
		// Dropped, and said so. Downstream this lands in the same "noop" status as
		// a question that simply had no framing to strip — but those are not the
		// same event: that one is the ordinary case, and this one is the model
		// handing back a paraphrase instead of a topic. Silent, it would look like
		// the step was working and merely finding nothing to do.
		slog.Warn("understand: topic too long, dropped",
			"runes", len([]rune(s)), "max", understandMaxTopicRunes)
		return ""
	}
	return s
}
