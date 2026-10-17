package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/trick77/peeq/internal/subtitles"
)

const (
	// A transcript line gathers consecutive cues until it holds this much
	// text or spans this many seconds: YouTube cues are a few words each, and
	// a timestamp per three words would spend the budget on timestamps.
	lineTargetRunes   = 240
	lineMaxSpanSecond = 30
	// continuationRoom keeps space under the cap for the closing hint.
	continuationRoom = 200
)

type transcriptArgs struct {
	ID           string `json:"id"`
	StartSeconds *int   `json:"start_seconds"`
	EndSeconds   *int   `json:"end_seconds"`
}

type transcriptLine struct {
	start int
	// last is the second of the latest cue merged in.
	last int
	text string
}

func runGetTranscript(ctx context.Context, h *handler, raw json.RawMessage) toolResult {
	var a transcriptArgs
	if err := decodeArgs(raw, &a); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	id := strings.TrimSpace(a.ID)
	if id == "" {
		return errorResult("id is required")
	}
	start, end := 0, -1
	if a.StartSeconds != nil {
		start = max(*a.StartSeconds, 0)
	}
	if a.EndSeconds != nil {
		end = *a.EndSeconds
		if end <= start {
			return errorResult("end_seconds must be after start_seconds")
		}
	}
	t, err := h.lib.Transcript(ctx, id)
	if err != nil {
		slog.Warn("mcp: get transcript failed", "video_id", id, "err", err)
		return errorResult("reading the transcript failed; try again")
	}
	if t == nil || len(t.Cues) == 0 {
		return errorResult(fmt.Sprintf("peeq has no transcript for video %q.", id))
	}

	lines := mergeCues(t.Cues, start, end)
	var b strings.Builder
	title := t.Title
	if title == "" {
		title = t.VideoID
	}
	fmt.Fprintf(&b, "Transcript of %s (%s) from [%s]:\n", title, t.VideoID, clock(start))
	if len(lines) == 0 {
		b.WriteString("(no transcript lines in this window)")
		return toolResult{Text: b.String()}
	}
	limit := maxToolTextBytes - continuationRoom
	for i, l := range lines {
		line := fmt.Sprintf("[%s] %s\n", clock(l.start), l.text)
		if b.Len()+len(line) > limit {
			next := lines[i].start
			if i == 0 {
				// One line (a second with a wall of cues) outgrows the whole
				// budget. Advertising its own start would page in place
				// forever, so cut it and resume at the next line, which starts
				// on a later second (mergeCues never splits one).
				b.WriteString(capText(line, limit-b.Len()) + "\n")
				if len(lines) == 1 {
					b.WriteString("[line cut at the size limit]")
					return toolResult{Text: b.String()}
				}
				next = lines[1].start
			}
			fmt.Fprintf(&b, "[transcript truncated at the size limit; call get_transcript again with start_seconds=%d to continue]", next)
			return toolResult{Text: b.String()}
		}
		b.WriteString(line)
	}
	return toolResult{Text: strings.TrimRight(b.String(), "\n")}
}

// mergeCues keeps the cues starting inside [start, end) (end < 0: no end) and
// joins neighbours into lines. Each line starts on a cue boundary, so its
// timestamp is a valid start_seconds to resume from.
func mergeCues(cues []subtitles.Cue, start, end int) []transcriptLine {
	var out []transcriptLine
	var cur *transcriptLine
	for _, c := range cues {
		if c.StartSeconds < start || (end >= 0 && c.StartSeconds >= end) {
			continue
		}
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		// A cue on the same second as the line's latest one always joins it,
		// whatever the size: a second split across two lines would make a
		// resume from the second line's start repeat the first line's tail.
		// So every line starts on a second no earlier line holds.
		if cur != nil && (c.StartSeconds <= cur.last ||
			len([]rune(cur.text)) < lineTargetRunes && c.StartSeconds-cur.start < lineMaxSpanSecond) {
			cur.text += " " + text
			cur.last = max(cur.last, c.StartSeconds)
			continue
		}
		out = append(out, transcriptLine{start: c.StartSeconds, last: c.StartSeconds, text: text})
		cur = &out[len(out)-1]
	}
	return out
}
