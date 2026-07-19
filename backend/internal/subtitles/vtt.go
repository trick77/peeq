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

// ParseVTT reads WebVTT and returns the deduplicated transcript + cue index.
func ParseVTT(r io.Reader) (Parsed, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var cues []Cue
	var curStart = -1
	var curLines []string
	var last string // last emitted line, for rolling-dup collapse

	flush := func() {
		if curStart < 0 {
			return
		}
		text := strings.TrimSpace(strings.Join(curLines, " "))
		curLines = curLines[:0]
		if text == "" {
			return
		}
		// Collapse a rolling duplicate: if this cue's text is the previous line
		// with more appended (or identical), keep only the longer form.
		if last != "" && (text == last || strings.HasPrefix(text, last)) {
			// replace the previous cue's text with the extended one
			if len(cues) > 0 {
				cues[len(cues)-1].Text = text
			}
			last = text
			return
		}
		if last != "" && strings.HasPrefix(last, text) {
			// this cue is a prefix of the last one — drop it as a partial repeat
			return
		}
		cues = append(cues, Cue{StartSeconds: curStart, Text: text})
		last = text
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
