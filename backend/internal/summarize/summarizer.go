// Package summarize turns a transcript into three artifacts via map-reduce over
// chunks, dodging the model's context window: summarize each chunk, then reduce
// the chunk summaries. Chapters prefer yt-dlp's own metadata when present.
package summarize

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/subtitles"
	"github.com/trick77/peeq/internal/videos"
)

type Chapter struct {
	TS     int    `json:"ts"`
	Title  string `json:"title"`
	Source string `json:"source"`
}

type KeyPoint struct {
	TS   int    `json:"ts"`
	Text string `json:"text"`
}

// Completer is the subset of llm.Client the summarizer needs.
type Completer interface {
	Complete(ctx context.Context, messages []llm.Message) (string, error)
}

type Summarizer struct {
	c Completer
	// summaryChunkTokens is the coarse chunk budget for the prose summary (in
	// estimated tokens). A transcript that fits produces a single chunk, so the
	// summary is one call; only a marathon fans out into a few coarse sections.
	summaryChunkTokens int
}

// Option configures a Summarizer. Variadic so existing New(c) callers (and every
// test) keep compiling.
type Option func(*Summarizer)

// WithSummaryChunkTokens sets the coarse summary chunk budget. A non-positive n
// is ignored, leaving the default.
func WithSummaryChunkTokens(n int) Option {
	return func(s *Summarizer) {
		if n > 0 {
			s.summaryChunkTokens = n
		}
	}
}

func New(c Completer, opts ...Option) *Summarizer {
	s := &Summarizer{c: c, summaryChunkTokens: defaultSummaryChunkTokens}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Classify asks the model to pick exactly one category id from allowed,
// given the video title and its generated summary. It returns the model's
// raw reply unchanged; the caller normalizes it against the enum (an invalid
// or empty reply must degrade to "uncategorized", not error). This is a
// cheap call: the input is the short summary, not the full transcript.
//
// allowed carries labels (and, where the neighbours blur, a hint) as well as
// ids so the model sees what each id means; callers pass
// videos.ClassifiableCategories(), which excludes the 'uncategorized'
// fallback. The prompt forces a choice on purpose: offering an escape hatch
// made the model take it, and a rough-but-real category is more useful in the
// Library than a bucket nobody browses.
//
// The list is rendered one category per line rather than comma-joined: the
// hints contain commas of their own, and a 20-plus entry inline list reads as
// one run-on sentence.
func (s *Summarizer) Classify(ctx context.Context, title, summary string, allowed []videos.Category) (string, error) {
	labelled := make([]string, len(allowed))
	for i, c := range allowed {
		labelled[i] = "- " + c.ID + " (" + c.Label + ")"
		if c.Hint != "" {
			labelled[i] += ": " + c.Hint
		}
	}
	sys := "You classify a video into exactly one category. The categories are:\n" +
		strings.Join(labelled, "\n") +
		"\nReply with a single category id from that list and nothing else — no punctuation, " +
		"no explanation. Always choose the closest match even when the fit is imperfect. " +
		"Never invent an id and never refuse to choose."
	// No thinking: the answer is one id picked from a list the prompt already
	// spells out, and letting the model reason spends several hundred
	// completion tokens to emit a single word.
	return s.c.Complete(llm.WithoutThinking(ctx), []llm.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: "TITLE: " + title + "\n\nSUMMARY:\n" + summary},
	})
}

const (
	// defaultSummaryChunkTokens sizes the coarse summary chunk to ~3.5h of
	// transcript. The chat model's context window is ~1M tokens, so the whole
	// transcript is a single chunk (hence a single call) for all but multi-hour
	// videos; keeping each call's input near this size also holds time-to-first-
	// byte under the client's header timeout instead of prefilling for minutes.
	defaultSummaryChunkTokens = 48000

	// summaryMaxTokens bounds the summary call. It runs with thinking ON (this is
	// the summary a person reads), so the cap must fit reasoning plus a ~190-word
	// answer — generous, but far below a runaway (a keypoints call once spent
	// 44k). It is a spiral backstop, not a length target.
	summaryMaxTokens = 8000

	// keypointsMaxTokens bounds the keypoints JSON. It runs with thinking OFF, so
	// the whole budget is available for output; a rich video is a few thousand
	// tokens of chapters + points, so this is ample.
	keypointsMaxTokens = 4000
)

// wholeVideoSystemPrompt drives the single-pass summary: the full transcript in,
// one cohesive summary out. It is the synthesis the reader sees, so its call
// keeps thinking on (unlike the coarse-section pass below).
const wholeVideoSystemPrompt = "You are given the full transcript of one video. Write a single cohesive summary of at most 2 paragraphs and at most 190 words total. " +
	"Lead with what the video is about, then its main claims or moments. Be concrete and drop tangents; do not list every topic mentioned. " +
	"Output only the summary prose, with no preamble, headings, or labels."

// coarseSectionSystemPrompt summarizes one large section of a long transcript in
// the rare multi-chunk fallback. Unlike a 600-token map (which asked for 1-2
// sentences), it produces a short paragraph or two scaled to a ~48k-token
// section, so the reduce has real material to synthesize from.
const coarseSectionSystemPrompt = "You are given one section of a longer video transcript. Summarize it in 1-2 short paragraphs (at most ~120 words): its main claims, findings, or moments and the specifics they turn on. " +
	"Write plainly in the third person, present tense; use only what the section states. Output only the summary prose, with no preamble or labels."

// reduceSystemPrompt combines coarse-section summaries into the final summary
// (multi-chunk fallback only).
const reduceSystemPrompt = "Combine these section summaries of one video into a single cohesive summary of at most 2 paragraphs and at most 190 words total. " +
	"Lead with what the video is about, then its main claims or moments. " +
	"Be concrete and drop tangents; do not list every topic mentioned."

// SummarizeText produces the prose summary. Because the chat model has a ~1M-
// token context window, the whole transcript fits in a SINGLE call for all but
// multi-hour videos — so this is single-pass in the common case and only falls
// back to a coarse (few big sections) map-reduce for a marathon. It is the
// resumable worker's first step, persisted on its own so a later failure never
// discards it.
func (s *Summarizer) SummarizeText(ctx context.Context, transcript string) (string, error) {
	budget := s.summaryChunkTokens
	if budget <= 0 {
		budget = defaultSummaryChunkTokens
	}
	// Coarse chunking: rag.Chunk returns a single chunk when the transcript fits
	// the budget, so the common path below is one call. The overlap is small
	// relative to the section size and only matters on the rare fan-out.
	chunks := rag.Chunk(transcript, rag.ChunkOptions{
		TargetTokens: budget, MaxTokens: budget + budget/8, OverlapTokens: 500,
	})
	if len(chunks) == 0 {
		return "", fmt.Errorf("summarize: empty transcript")
	}

	// Single-pass: synthesize the whole video in one call. Thinking stays on
	// (default), bounded by summaryMaxTokens so it can reason without spiralling.
	// FailOnEarlyFinish makes a content_filter/refusal cut retry the job rather
	// than persist half a summary of the whole video (a "length" cut is our own
	// cap and is tolerated).
	if len(chunks) == 1 {
		summary, err := s.c.Complete(
			llm.WithMaxTokens(llm.FailOnEarlyFinish(ctx), summaryMaxTokens),
			[]llm.Message{
				{Role: "system", Content: wholeVideoSystemPrompt},
				{Role: "user", Content: chunks[0].Text},
			})
		if err != nil {
			return "", fmt.Errorf("summarize single-pass: %w", err)
		}
		return strings.TrimSpace(summary), nil
	}

	// Rare fallback (multi-hour): coarse map (thinking off — condensing text
	// already in front of the model) then reduce (thinking on — the reader's
	// summary). Sequential: it is a handful of sections, and pace() serializes
	// call starts regardless of goroutines, so concurrency would buy nothing.
	mapCtx := llm.WithoutThinking(ctx)
	sections := make([]string, 0, len(chunks))
	for _, ch := range chunks {
		out, err := s.c.Complete(mapCtx, []llm.Message{
			{Role: "system", Content: coarseSectionSystemPrompt},
			{Role: "user", Content: ch.Text},
		})
		if err != nil {
			return "", fmt.Errorf("summarize map: %w", err)
		}
		sections = append(sections, strings.TrimSpace(out))
	}
	summary, err := s.c.Complete(llm.WithMaxTokens(ctx, summaryMaxTokens), []llm.Message{
		{Role: "system", Content: reduceSystemPrompt},
		{Role: "user", Content: strings.Join(sections, "\n\n")},
	})
	if err != nil {
		return "", fmt.Errorf("summarize reduce: %w", err)
	}
	return strings.TrimSpace(summary), nil
}

// KeyPoints extracts key points — and chapters, when yt-dlp did not supply them
// — from the already-computed summary plus the cue index. It is the worker's
// fragile last step, split out so a failure here retries only this call and
// never re-runs the summary.
func (s *Summarizer) KeyPoints(ctx context.Context, summary string, cues []subtitles.Cue, ytdlpChapters []Chapter) (chapters []Chapter, keyPoints []KeyPoint, err error) {
	cueIndex := formatCues(cues)
	wantChapters := len(ytdlpChapters) == 0
	kpPrompt := "From the video, extract notable/surprising/quotable moments as JSON " +
		`{"key_points":[{"ts":<seconds>,"text":"..."}]}`
	if wantChapters {
		kpPrompt = "From the video, produce a timestamped chapter list AND key points as JSON " +
			`{"chapters":[{"ts":<seconds>,"title":"..."}],"key_points":[{"ts":<seconds>,"text":"..."}]}`
	}
	// Thinking OFF, and a max-tokens backstop: this is an extractive JSON step,
	// and it is the call that once spiralled to 44k reasoning tokens and returned
	// nothing (max_tokens counts reasoning, so a thinking-on cap would still hand
	// back empty — disabling reasoning is what guarantees output). If extraction
	// quality drops, WithReasoningEffort(ctx, "low") is the middle ground.
	kpCtx := llm.WithMaxTokens(llm.WithoutThinking(ctx), keypointsMaxTokens)
	raw, err := s.c.Complete(kpCtx, []llm.Message{
		{Role: "system", Content: kpPrompt + " Use only timestamps that appear in the cue index. Output JSON only."},
		{Role: "user", Content: "SUMMARY:\n" + summary + "\n\nCUE INDEX (seconds: text):\n" + cueIndex},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("summarize keypoints: %w", err)
	}

	var parsed struct {
		Chapters  []Chapter  `json:"chapters"`
		KeyPoints []KeyPoint `json:"key_points"`
	}
	_ = json.Unmarshal([]byte(extractJSON(raw)), &parsed) // tolerate malformed JSON: leave empty

	if wantChapters {
		for i := range parsed.Chapters {
			parsed.Chapters[i].Source = "mimo"
		}
		chapters = parsed.Chapters
	} else {
		chapters = ytdlpChapters
	}
	return chapters, parsed.KeyPoints, nil
}

func formatCues(cues []subtitles.Cue) string {
	var b strings.Builder
	for _, c := range cues {
		fmt.Fprintf(&b, "%d: %s\n", c.StartSeconds, c.Text)
	}
	return b.String()
}

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// extractJSON returns the substring from the first '{' to the last '}' in s,
// which recovers the JSON object even when the model prefixes prose before a
// ```json fence (e.g. "Here is the JSON:\n```json{...}```") — a reply
// stripFences alone cannot handle, since it only trims fence markers at exact
// string boundaries. Falls back to stripFences(s) when no brace pair is
// found, so genuinely non-JSON replies still degrade to the old "leave empty"
// behavior instead of erroring.
func extractJSON(s string) string {
	first := strings.IndexByte(s, '{')
	last := strings.LastIndexByte(s, '}')
	if first >= 0 && last > first {
		return s[first : last+1]
	}
	return stripFences(s)
}
