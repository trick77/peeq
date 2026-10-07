package summarize

import (
	"context"
	"fmt"
	"strings"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/subtitles"
)

// inDepthMaxAnswerTokens bounds the in-depth calls: the single pass, each map
// part and the merge. The longest target is 900 words, about 1,200 tokens of
// English and more in a wordier language, so this is a backstop several times
// that. A cut here keeps every finished section (see inDepthRules).
const inDepthMaxAnswerTokens = 4096

// inDepthRules is the shape and voice shared by the single pass and the merge,
// so a rule added here reaches the long-video path too.
//
// Plain text with "### " heading lines, not JSON. A call cut at the answer cap
// still renders every section it finished, where cut JSON parses as nothing;
// and a reply that ignores the format renders as plain paragraphs, which is
// the short summary's look.
//
// Ordered by weight in the argument, not by timeline: the Highlights already
// walk the video in order, so a second chronological list would repeat them.
const inDepthRules = `STRUCTURE
1. Open with one paragraph of 2-3 sentences: the question the video takes on and the answer it gives. State the answer itself, never that there is one.
2. Then 3 to 7 sections, one per key point. A key point is a claim, finding, method or recommendation the video spends real time on. Order them by how much the video's conclusion depends on them, not by when they appear. Merge points that make the same claim. Drop intros, outros, asides, jokes and requests to like or subscribe.
3. Each section starts on its own line with "### ", then the point as a plain statement of at most 8 words, then a space and the timestamp where the point is first made, in square brackets exactly as the input writes it, for example [12:34]. Then a blank line and one paragraph of 60 to 140 words.
4. Each paragraph gives the claim, then what it rests on: the figures, names, examples, steps, comparisons or measurements the video gives, with their units. Then any condition, limit or caveat the speaker attaches. When the reasoning is the point, give its steps in order.

WRITING
- State the content directly. Never write "the video", "the speaker discusses", "he goes on to explain", "in this section". Attribute only where it matters: an opinion, a disputed claim, a guest's view, by name if given.
- Prefer the specific: a number over "significant", the named product over "a tool", the example used over a paraphrase of its moral.
- Keep the video's line between what was measured, what was claimed and what was guessed. Do not upgrade a guess to a finding.
- Use only what the input says. Add no background, verdict or advice the video does not give. Captions are machine-made: correct a misheard name or term only when the context makes it certain, otherwise leave it out.
- Length follows substance. Aim for the word target; a thin video gets fewer sections, never padded ones.
- Third person, present tense, plain prose. No bullets, bold, links or other markdown besides the "### " lines. No preamble, no closing remarks.`

// inDepthTask is the single pass's task, sent after the shared video prefix
// (prefix.go). The short summary is not fed in on purpose: this text stands on
// its own and must not be a longer paraphrase of it.
const inDepthTask = `Write the in-depth summary of this video for a reader who will not watch it and wants its substance: what it claims, why, and on what evidence.

` + inDepthRules

// inDepthSectionPrompt is the map over one part of a video too long for one
// call. Its notes are the merge's only material, so it keeps the specifics and
// the timestamps the reader's text will need, in the same format.
const inDepthSectionPrompt = `You are given one part of a longer video's transcript as a cue index: one caption cue per line, starting with its timestamp in square brackets. Write notes for the video's in-depth summary covering this part only.

Write 2 to 5 sections. Each starts on its own line with "### ", then the point as a plain statement of at most 8 words, then a space and the timestamp where the point is first made, in square brackets exactly as the input writes it. Then a blank line and one paragraph of at most 140 words giving the claim, the figures, names, examples, steps or measurements it rests on with their units, and any caveat the speaker attaches.

Use only what the part says. Skip intros, outros, asides and requests to like or subscribe. Third person, present tense, plain prose. No preamble.`

// inDepthMergePrompt writes the reader's text from the map's notes.
const inDepthMergePrompt = `You write the in-depth summary of one video for a reader who will not watch it and wants its substance: what it claims, why, and on what evidence.

INPUT
The title, the length, a word target, and notes on the video's consecutive parts, each a set of "### " sections with the timestamp where its point is made. Choose from all parts; keep each point's timestamp as the notes write it.

` + inDepthRules

// InDepth writes the in-depth summary from the sponsor-stripped cues. One call
// for all but multi-hour videos, opening with the same bytes as the short
// summary's so it reads the transcript from the prompt cache (prefix.go); past
// the budget, a map over the parts and a merge. Every call runs at the model's default reasoning: the worker runs it
// offline, nobody waits on it, and it is prose a reader sees.
func (s *Summarizer) InDepth(ctx context.Context, title string, durationSeconds int, cues []subtitles.Cue) (string, error) {
	if len(cues) == 0 {
		return "", fmt.Errorf("in-depth: empty transcript")
	}
	target := "\nWORD TARGET: about " + fmt.Sprint(inDepthWordTarget(durationSeconds)) + " words"
	chunks := s.videoChunks(cues)
	if len(chunks) == 0 {
		return "", fmt.Errorf("in-depth: empty transcript")
	}
	callCtx := llm.WithMaxAnswerTokens(llm.FailOnEarlyFinish(ctx), inDepthMaxAnswerTokens)

	if len(chunks) == 1 {
		out, err := s.c.Complete(callCtx, []llm.Message{
			{Role: "system", Content: videoSystemPrompt},
			{Role: "user", Content: videoPrefix(title, durationSeconds, chunks[0].Text) + inDepthTask + target},
		})
		if err != nil {
			return "", fmt.Errorf("in-depth single-pass: %w", err)
		}
		return finalizeInDepth(out, "single-pass")
	}

	// An empty part is an error, as in SummarizeText's map: the merge would
	// otherwise write a text with that stretch of the video silently missing.
	notes := make([]string, 0, len(chunks))
	for i, ch := range chunks {
		out, err := s.c.Complete(callCtx, []llm.Message{
			{Role: "system", Content: inDepthSectionPrompt},
			{Role: "user", Content: ch.Text},
		})
		if err != nil {
			return "", fmt.Errorf("in-depth map: %w", err)
		}
		part := strings.TrimSpace(out)
		if part == "" {
			return "", fmt.Errorf("in-depth map: model returned an empty part (%d of %d)", i+1, len(chunks))
		}
		notes = append(notes, fmt.Sprintf("PART %d OF %d\n%s", i+1, len(chunks), part))
	}
	out, err := s.c.Complete(callCtx, []llm.Message{
		{Role: "system", Content: inDepthMergePrompt},
		{Role: "user", Content: "TITLE: " + title + "\nLENGTH: " + clock(durationSeconds) + target +
			"\n\nNOTES:\n\n" + strings.Join(notes, "\n\n")},
	})
	if err != nil {
		return "", fmt.Errorf("in-depth merge: %w", err)
	}
	return finalizeInDepth(out, "merge")
}

// inDepthWordTarget scales the length to the video: about 25 words a minute,
// never under 350 or over 900. An unknown duration gets the middle.
func inDepthWordTarget(durationSeconds int) int {
	if durationSeconds <= 0 {
		return 600
	}
	return min(900, max(350, durationSeconds*25/60))
}

// finalizeInDepth trims the reply and rejects an empty one, for the reason
// finalizeSummary does: a blank text must retry, not be stored.
func finalizeInDepth(raw, stage string) (string, error) {
	if s := strings.TrimSpace(raw); s != "" {
		return s, nil
	}
	return "", fmt.Errorf("in-depth %s: model returned an empty text", stage)
}

// formatClockCues renders the cue index with the stamps the reader will see,
// so the model copies a timestamp rather than converting seconds.
func formatClockCues(cues []subtitles.Cue) string {
	var b strings.Builder
	for _, c := range cues {
		fmt.Fprintf(&b, "[%s] %s\n", clock(c.StartSeconds), c.Text)
	}
	return b.String()
}

// clock renders seconds as m:ss, or h:mm:ss from an hour on — the Player's
// own timestamp format (formatDuration in ui/src/format.ts).
func clock(secs int) string {
	if secs < 0 {
		secs = 0
	}
	h, m, s := secs/3600, secs%3600/60, secs%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
