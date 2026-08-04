// Package summarize turns a transcript into three artifacts via map-reduce over
// chunks, dodging the model's context window: summarize each chunk, then reduce
// the chunk summaries. Chapters prefer yt-dlp's own metadata when present.
package summarize

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
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
//
// "Classify by what the video is about, not by the institutions ... it mentions"
// is a tie-break, not decoration. A summary of a nuclear-weapons history or a
// UFO-investigation video is dense with Pentagon, Cold War and congressional
// vocabulary, and without this line the model reads that vocabulary as the
// topic and answers 'politics'. It sits before the output-format sentences so
// the last thing the model reads is still how to reply.
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
		"\nClassify by what the video is about, not by the institutions, agencies, countries " +
		"or eras it mentions. " +
		"Reply with a single category id from that list and nothing else — no punctuation, " +
		"no explanation. Always choose the closest match even when the fit is imperfect. " +
		"Never invent an id and never refuse to choose."
	// No thinking: the answer is one id picked from a list the prompt already
	// spells out, and letting the model reason spends several hundred
	// completion tokens to emit a single word.
	//
	// And on the non-Pro deployment, which is a separate point: this call wants a
	// fast answer rather than a deep one, and Pro is where the long summary calls
	// queue. The category ids never reach a reader as prose — they land in the
	// Library filter after NormalizeCategory — so the step that stands to gain
	// most from a shorter queue is also the one where the deployment matters
	// least. The backlog sweep fires it in bulk, which is where that adds up.
	//
	// The cap is a runaway backstop, not a length target: one id is a token or
	// two, and with thinking off the whole budget is output, so 32 cannot
	// truncate a real answer.
	ctx = llm.WithMaxTokens(llm.NonReasoning(llm.WithoutThinking(ctx)), classifyMaxTokens)
	return s.c.Complete(ctx, []llm.Message{
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
	// the whole budget is output and there is no reasoning to bound — this is only
	// a runaway backstop, set well clear of any real video's chapters + points
	// (hundreds of them) so it never truncates legitimate JSON, which would parse
	// as empty and silently drop every point.
	keypointsMaxTokens = 16000

	// classifyMaxTokens bounds the category call, which runs with thinking off and
	// answers with a single id. It was the one call site with no cap at all, so an
	// endpoint that ignored the prompt and started explaining itself had nothing
	// to stop it but its own default.
	classifyMaxTokens = 32
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
	// The cue index the key-points call sees has its ">>" speaker markers taken
	// out; stripping them here too keeps both prompts reading the same text.
	transcript = stripSpeakerMarkers(transcript)
	// budget is always positive — New defaults it and WithSummaryChunkTokens
	// ignores a non-positive override.
	budget := s.summaryChunkTokens
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
		return finalizeSummary(summary, "single-pass")
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
	// The reduce is the reader-facing summary too, so it carries the same guard
	// as the single-pass call: FailOnEarlyFinish (don't persist a filtered/cut
	// final summary) and the empty-result rejection below.
	summary, err := s.c.Complete(
		llm.WithMaxTokens(llm.FailOnEarlyFinish(ctx), summaryMaxTokens),
		[]llm.Message{
			{Role: "system", Content: reduceSystemPrompt},
			{Role: "user", Content: strings.Join(sections, "\n\n")},
		})
	if err != nil {
		return "", fmt.Errorf("summarize reduce: %w", err)
	}
	return finalizeSummary(summary, "reduce")
}

// finalizeSummary trims the model's summary and rejects an empty result. An
// empty-but-successful completion — a call that spent its whole token budget
// reasoning and ended on "length", or a filtered answer — must never be
// persisted as a blank "done" summary (the worker would then run classify and
// key-points against empty input, and a resume would re-summarize anyway).
// Returning an error instead lets the job retry.
func finalizeSummary(raw, stage string) (string, error) {
	if s := strings.TrimSpace(raw); s != "" {
		return s, nil
	}
	return "", fmt.Errorf("summarize %s: model returned an empty summary", stage)
}

// keyPointRules is the tail of BOTH key-points prompts (with and without
// chapters), so a rule added here can never miss the chapter-producing path.
//
// Every rule below answers a highlight that actually shipped: one row carried
// three unrelated claims about a bike race in ~70 words with stray reference
// digits glued to the sentence ends — a mini-summary, not a moment. The model
// reads the SUMMARY as well as the cue index, so without "one claim, one
// timestamp" it recaps the video instead of pointing at an instant, and without
// a count cap it pads the list.
//
// The length and count limits name key_points explicitly: chapter titles ride
// the same call, and a bare "keep each entry short" would bind those too.
//
// "Describe, never quote" is the other half of the same idea: a row pasted
// straight out of the transcript reads as a fragment with no context, so a key
// point says what is happening at that moment in the summarizer's own words.
const keyPointRules = " Each key point DESCRIBES what happens at one moment: one timestamp, one claim, in your own words. " +
	"Never merge several claims into one key point and never summarize the video — the summary already exists. " +
	"Each key_points text is a single sentence of at most 25 words, plain third-person prose. " +
	"Return at most 10 key points for the whole video, the most notable ones. " +
	"Never make a key point a verbatim transcript quote and nothing else: always say what is being said, " +
	"claimed, or shown — a short quoted phrase inside that sentence is fine, a quoted line on its own is not. " +
	"The text is prose only: no speaker markers such as '>>', no reference, citation or footnote numbers, " +
	"and no surrounding quotes, brackets, bullets or markdown. " +
	"Use only timestamps that appear in the cue index. Output JSON only."

// KeyPoints extracts key points — and chapters, when yt-dlp did not supply them
// — from the already-computed summary plus the cue index. It is the worker's
// fragile last step, split out so a failure here retries only this call and
// never re-runs the summary.
func (s *Summarizer) KeyPoints(ctx context.Context, summary string, cues []subtitles.Cue, ytdlpChapters []Chapter) (chapters []Chapter, keyPoints []KeyPoint, err error) {
	cueIndex := formatCues(cues)
	wantChapters := len(ytdlpChapters) == 0
	kpPrompt := "From the summary and cue index below, extract the notable or surprising moments as JSON " +
		`{"key_points":[{"ts":<seconds>,"text":"..."}]}`
	if wantChapters {
		kpPrompt = "From the summary and cue index below, produce a timestamped chapter list AND key points as JSON " +
			`{"chapters":[{"ts":<seconds>,"title":"..."}],"key_points":[{"ts":<seconds>,"text":"..."}]}`
	}
	// Thinking OFF, and a max-tokens backstop: this is an extractive JSON step,
	// and it is the call that once spiralled to 44k reasoning tokens and returned
	// nothing (max_tokens counts reasoning, so a thinking-on cap would still hand
	// back empty — disabling reasoning is what guarantees output). If extraction
	// quality drops, WithReasoningEffort(ctx, "low") is the middle ground.
	kpCtx := llm.WithMaxTokens(llm.WithoutThinking(ctx), keypointsMaxTokens)
	raw, err := s.c.Complete(kpCtx, []llm.Message{
		{Role: "system", Content: kpPrompt + keyPointRules},
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
			// A model-written title picks up the same debris a key point does.
			// yt-dlp's own chapters are left alone: they are YouTube's labels,
			// not the model's.
			parsed.Chapters[i].Title = sanitizeKeyPointText(parsed.Chapters[i].Title)
		}
		chapters = parsed.Chapters
	} else {
		chapters = ytdlpChapters
	}
	for i := range parsed.KeyPoints {
		parsed.KeyPoints[i].Text = sanitizeKeyPointText(parsed.KeyPoints[i].Text)
	}
	return chapters, parsed.KeyPoints, nil
}

// leadingListMarkerRe matches the bullet, dash or speaker marker a model
// sometimes keeps at the front of an extracted line. Anchored, so it never
// touches a hyphen inside a word or a minus in the middle of a sentence, and it
// catches the tight ">>Hello" spelling that stripSpeakerMarkers deliberately
// leaves alone mid-sentence.
var leadingListMarkerRe = regexp.MustCompile(`^(?:[-–—*•·]|>>+)+\s*`)

// keyPointSpaceRe collapses the whitespace a stripped marker leaves behind.
// Go's \s is ASCII-only, so a no-break space has to be named (the cue text this
// is derived from can carry one — see subtitles.spaceRe).
var keyPointSpaceRe = regexp.MustCompile(`[\s\x{00A0}]+`)

// sanitizeKeyPointText cleans one model-written line for display. The prompt
// already forbids all of this; the model does it anyway often enough that the
// panel showed rows starting with ">>", and a row is rendered verbatim in the
// Player and on the share page.
//
// Nothing here shortens the text: an over-long key point is a prompt problem,
// and truncating mid-sentence would look like a bug rather than a fix.
func sanitizeKeyPointText(s string) string {
	s = stripSpeakerMarkers(s)
	s = leadingListMarkerRe.ReplaceAllString(strings.TrimSpace(s), "")
	s = strings.TrimSpace(keyPointSpaceRe.ReplaceAllString(s, " "))
	// Wrapping quotes only: a quoted phrase INSIDE the sentence is the
	// "quotable moment" the prompt asks for and must survive.
	//
	// The inner text has to be quote-free for the pair to count as wrapping.
	// Without that check, `"Weight drop" beats "frame stiffness"` also starts
	// and ends with a quote, and stripping its ends would leave a mangled
	// sentence with the quotes around the wrong words.
	if len(s) >= 2 {
		for _, q := range []struct{ open, close string }{
			{`"`, `"`}, {"'", "'"}, {"“", "”"}, {"‘", "’"},
		} {
			if !strings.HasPrefix(s, q.open) || !strings.HasSuffix(s, q.close) {
				continue
			}
			inner := s[len(q.open) : len(s)-len(q.close)]
			if strings.Contains(inner, q.open) || strings.Contains(inner, q.close) {
				break
			}
			s = strings.TrimSpace(inner)
			break
		}
	}
	return s
}

// speakerMarkerRe matches the WebVTT speaker marker. Broadcast and manual
// caption tracks open a new speaker's line with ">>" (and a new SPEAKER's turn
// with ">>>"), which ParseVTT keeps on purpose — the transcript panel shows the
// captions as they were written, and vtt.go stays in lockstep with the
// TypeScript mirror in ui/src/vtt.tsx. It is only the LLM input that wants them
// gone, so the stripping lives here rather than in the parser.
//
// A marker stands on its own: it opens a line or follows a space, and a space
// follows it. Requiring that boundary is what keeps a right-shift out of the
// blast radius — "cout>>x" is left alone, and a caption that spells out an
// operator keeps whatever sits tight against it. The captured leading space is
// put back so the two speakers' sentences do not run together.
var speakerMarkerRe = regexp.MustCompile(`(^|\s)>>+(\s|$)`)

// stripSpeakerMarkers removes those markers from text on its way to the model.
func stripSpeakerMarkers(s string) string {
	if !strings.Contains(s, ">>") {
		return s
	}
	return strings.TrimSpace(speakerMarkerRe.ReplaceAllString(s, "$1"))
}

func formatCues(cues []subtitles.Cue) string {
	var b strings.Builder
	for _, c := range cues {
		fmt.Fprintf(&b, "%d: %s\n", c.StartSeconds, stripSpeakerMarkers(c.Text))
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
