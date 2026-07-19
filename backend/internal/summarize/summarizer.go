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

type Artifacts struct {
	Summary   string
	Chapters  []Chapter
	KeyPoints []KeyPoint
}

// Completer is the subset of llm.Client the summarizer needs.
type Completer interface {
	Complete(ctx context.Context, messages []llm.Message) (string, error)
}

type Summarizer struct{ c Completer }

func New(c Completer) *Summarizer { return &Summarizer{c: c} }

func (s *Summarizer) Run(ctx context.Context, transcript string, cues []subtitles.Cue, ytdlpChapters []Chapter) (Artifacts, error) {
	chunks := rag.Chunk(transcript, rag.DefaultChunkOptions())
	if len(chunks) == 0 {
		return Artifacts{}, fmt.Errorf("summarize: empty transcript")
	}

	// MAP: summarize each chunk.
	var chunkSummaries []string
	for _, ch := range chunks {
		out, err := s.c.Complete(ctx, []llm.Message{
			{Role: "system", Content: "You summarize one section of a video transcript in 2-3 sentences. Be concrete."},
			{Role: "user", Content: ch.Text},
		})
		if err != nil {
			return Artifacts{}, fmt.Errorf("summarize map: %w", err)
		}
		chunkSummaries = append(chunkSummaries, strings.TrimSpace(out))
	}
	joined := strings.Join(chunkSummaries, "\n\n")

	// REDUCE 1: prose summary.
	summary, err := s.c.Complete(ctx, []llm.Message{
		{Role: "system", Content: "Combine these section summaries of one video into a single cohesive summary of 2-4 short paragraphs."},
		{Role: "user", Content: joined},
	})
	if err != nil {
		return Artifacts{}, fmt.Errorf("summarize reduce: %w", err)
	}

	// REDUCE 2: key points (and chapters if yt-dlp didn't provide them). Provide
	// the cue index so the model can attach timestamps.
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
		return Artifacts{}, fmt.Errorf("summarize keypoints: %w", err)
	}

	var parsed struct {
		Chapters  []Chapter  `json:"chapters"`
		KeyPoints []KeyPoint `json:"key_points"`
	}
	_ = json.Unmarshal([]byte(stripFences(raw)), &parsed) // tolerate malformed JSON: leave empty

	art := Artifacts{Summary: strings.TrimSpace(summary), KeyPoints: parsed.KeyPoints}
	if wantChapters {
		for i := range parsed.Chapters {
			parsed.Chapters[i].Source = "mimo"
		}
		art.Chapters = parsed.Chapters
	} else {
		art.Chapters = ytdlpChapters
	}
	return art, nil
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
