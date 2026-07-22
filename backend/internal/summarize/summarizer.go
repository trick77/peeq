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

type Summarizer struct{ c Completer }

func New(c Completer) *Summarizer { return &Summarizer{c: c} }

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
	return s.c.Complete(ctx, []llm.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: "TITLE: " + title + "\n\nSUMMARY:\n" + summary},
	})
}

// SummarizeText produces the prose summary by map-reducing the transcript: one
// summary per chunk, then a single reduce. It is the resumable worker's first
// step, persisted on its own so a later failure never discards it.
func (s *Summarizer) SummarizeText(ctx context.Context, transcript string) (string, error) {
	chunks := rag.Chunk(transcript, rag.DefaultChunkOptions())
	if len(chunks) == 0 {
		return "", fmt.Errorf("summarize: empty transcript")
	}

	// MAP: summarize each chunk.
	var chunkSummaries []string
	for _, ch := range chunks {
		out, err := s.c.Complete(ctx, []llm.Message{
			{Role: "system", Content: "You summarize one section of a video transcript in 1-2 sentences. Be concrete."},
			{Role: "user", Content: ch.Text},
		})
		if err != nil {
			return "", fmt.Errorf("summarize map: %w", err)
		}
		chunkSummaries = append(chunkSummaries, strings.TrimSpace(out))
	}
	joined := strings.Join(chunkSummaries, "\n\n")

	// REDUCE: cohesive prose summary.
	summary, err := s.c.Complete(ctx, []llm.Message{
		{Role: "system", Content: "Combine these section summaries of one video into a single cohesive summary of at most 2 paragraphs and at most 190 words total. " +
			"Lead with what the video is about, then its main claims or moments. " +
			"Be concrete and drop tangents; do not list every topic mentioned."},
		{Role: "user", Content: joined},
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
	raw, err := s.c.Complete(ctx, []llm.Message{
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
