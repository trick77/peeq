// Package subtitles parses WebVTT into a clean plain transcript plus a cue index
// (start-second -> text) for timestamp mapping. It strips WebVTT structure and
// inline tags and collapses YouTube auto-caption rolling duplicates (each line is
// re-emitted with the next word appended, so naive concatenation triples length).
package subtitles

import (
	"bufio"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Cue is one subtitle line with its start time in whole seconds.
type Cue struct {
	StartSeconds int
	Text         string
}

// Parsed is the result of ParseVTT.
type Parsed struct {
	Transcript string
	Cues       []Cue
}

var (
	timingRe = regexp.MustCompile(`^(\d{2,}):(\d{2}):(\d{2})[.,](\d{3})\s*-->`)
	tagRe    = regexp.MustCompile(`<[^>]*>`)
)

// equalLines reports whether two line slices contain the same strings in order.
func equalLines(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ParseVTT reads WebVTT and returns the deduplicated transcript + cue index.
func ParseVTT(r io.Reader) (Parsed, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var cues []Cue
	var curStart = -1
	var curLines []string
	var last string        // joined text of the last emitted/merged cue, for whole-cue-dup collapse
	var lastLines []string // lines of the last emitted/merged cue, for sliding-window line collapse

	flush := func() {
		if curStart < 0 {
			return
		}
		lines := curLines
		curLines = nil
		if len(lines) == 0 {
			return
		}

		// Collapse a sliding-window duplicate: YouTube auto-captions often repeat
		// the tail line(s) of the previous cue as the leading line(s) of this one
		// (a rolling window). Drop the longest run of leading lines here that
		// exactly matches the trailing lines of the previously emitted cue.
		if len(lastLines) > 0 {
			maxK := len(lines)
			if len(lastLines) < maxK {
				maxK = len(lastLines)
			}
			for k := maxK; k >= 1; k-- {
				if equalLines(lines[:k], lastLines[len(lastLines)-k:]) {
					lines = lines[k:]
					break
				}
			}
		}
		if len(lines) == 0 {
			// every line in this cue was a rolling repeat of the previous cue —
			// nothing new to emit.
			return
		}

		text := strings.TrimSpace(strings.Join(lines, " "))
		if text == "" {
			return
		}

		// Collapse a whole-cue rolling duplicate (e.g. a caption that re-grows a
		// single line word-by-word): if this cue's text is the previous cue's
		// text with more appended (or identical), keep only the longer form.
		if last != "" && (text == last || strings.HasPrefix(text, last)) {
			if len(cues) > 0 {
				cues[len(cues)-1].Text = text
			}
			last = text
			lastLines = lines
			return
		}
		if last != "" && strings.HasPrefix(last, text) {
			// this cue is a prefix of the last one — drop it as a partial repeat
			return
		}
		cues = append(cues, Cue{StartSeconds: curStart, Text: text})
		last = text
		lastLines = lines
	}

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if m := timingRe.FindStringSubmatch(line); m != nil {
			flush()
			h, _ := strconv.Atoi(m[1])
			mnt, _ := strconv.Atoi(m[2])
			s, _ := strconv.Atoi(m[3])
			curStart = h*3600 + mnt*60 + s
			continue
		}
		if curStart < 0 {
			continue // header / NOTE / cue-id lines before the first timing
		}
		clean := strings.TrimSpace(tagRe.ReplaceAllString(line, ""))
		if clean == "" {
			continue
		}
		curLines = append(curLines, clean)
	}
	flush()
	if err := sc.Err(); err != nil {
		return Parsed{}, err
	}

	texts := make([]string, len(cues))
	for i, c := range cues {
		texts[i] = c.Text
	}
	return Parsed{Transcript: strings.Join(texts, " "), Cues: cues}, nil
}
