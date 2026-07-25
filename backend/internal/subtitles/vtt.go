// Package subtitles parses WebVTT into a clean plain transcript plus a cue index
// (start-second -> text) for timestamp mapping. It strips WebVTT structure and
// inline tags and collapses YouTube auto-caption rolling duplicates (each line is
// re-emitted with the next word appended, so naive concatenation triples length).
// It also strips non-speech sound-event markers ([Music], (applause), music
// notes) and can report that a track carries no real speech at all.
//
// The UI has its own forgiving WebVTT parser for the transcript panel
// (ui/src/vtt.tsx parseVtt); the sound-event and entity rules below are
// mirrored there and the two must stay in lockstep.
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
	// SoundEventCues is how many of the emitted Cues carried at least one
	// non-speech marker before stripping. Compared against len(Cues) it says how
	// music-heavy a track is; IsNonSpeech uses it as a guard.
	SoundEventCues int
}

var (
	timingRe = regexp.MustCompile(`^(\d{2,}):(\d{2}):(\d{2})[.,](\d{3})\s*-->`)
	tagRe    = regexp.MustCompile(`<[^>]*>`)

	// bracketRe matches a square-bracketed span. YouTube uses square brackets
	// only for sound events and speaker labels, never for spoken words, so an
	// open rule is safe here.
	bracketRe = regexp.MustCompile(`\[[^\]]*\]`)
	// parenRe matches a parenthesised span. Parentheses DO occur in real speech
	// transcripts, so a match is only stripped when its inner text is in
	// parenSoundEvents below.
	parenRe = regexp.MustCompile(`\([^)]*\)`)
	// noteRe matches the musical-note characters used by Whisper and by manual
	// caption tracks to bracket lyrics.
	noteRe = regexp.MustCompile(`[\x{266A}\x{266B}\x{266C}\x{2669}]`)
	// spaceRe collapses the whitespace a stripped span leaves behind. Go's \s is
	// ASCII-only, so a literal no-break space in the caption file has to be
	// named explicitly — the JS mirror's \s already covers it, and a difference
	// here would feed the two rolling-duplicate collapses different strings.
	spaceRe = regexp.MustCompile(`[\s\x{00A0}]+`)

	// entityRe matches the HTML entities YouTube escapes caption text with. A
	// single pass, so "&amp;lt;" decodes to "&lt;" and not to "<".
	entityRe = regexp.MustCompile(`&(?:amp|lt|gt|quot|apos|nbsp|#39|#[xX]27);`)
)

// entities is the decode table for entityRe. Kept as an explicit closed list
// rather than html.UnescapeString so the TypeScript mirror in ui/src/vtt.tsx
// can decode exactly the same set — a wider Go decoder would quietly leave the
// transcript panel and the summary disagreeing about the text.
var entities = map[string]string{
	"&amp;":  "&",
	"&lt;":   "<",
	"&gt;":   ">",
	"&quot;": `"`,
	"&apos;": "'",
	"&nbsp;": " ",
	"&#39;":  "'",
	"&#x27;": "'",
}

// unescapeEntities decodes the entities above. &nbsp; becomes a plain space
// rather than U+00A0 so downstream word splitting and dedup see ordinary
// whitespace.
func unescapeEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	return entityRe.ReplaceAllStringFunc(s, func(m string) string {
		// ToLower folds the &#X27; spelling onto the &#x27; key; every other
		// form entityRe can match is already lower-case.
		return entities[strings.ToLower(m)]
	})
}

// parenSoundEvents is the closed list of parenthesised annotations treated as
// non-speech. Anything else in parentheses is kept verbatim.
var parenSoundEvents = map[string]bool{
	"music":            true,
	"background music": true,
	"musique":          true,
	"applause":         true,
	"applauses":        true,
	"cheering":         true,
	"cheers":           true,
	"laughter":         true,
	"laughs":           true,
	"laughing":         true,
	"singing":          true,
	"sings":            true,
	"silence":          true,
	"no audio":         true,
	"inaudible":        true,
	"foreign":          true,
}

// stripSoundEvents removes non-speech markers from one caption line and reports
// whether it removed anything. An all-marker line comes back empty, which the
// caller drops the same way it drops a blank line.
func stripSoundEvents(s string) (string, bool) {
	out := bracketRe.ReplaceAllString(s, " ")
	out = noteRe.ReplaceAllString(out, " ")
	out = parenRe.ReplaceAllStringFunc(out, func(m string) string {
		inner := strings.ToLower(strings.TrimSpace(m[1 : len(m)-1]))
		if parenSoundEvents[inner] {
			return " "
		}
		return m
	})
	out = strings.TrimSpace(spaceRe.ReplaceAllString(out, " "))
	return out, out != strings.TrimSpace(s)
}

// Thresholds for IsNonSpeech. Deliberately conservative: a false positive
// throws away a legitimate sparse-dialogue video's summary, which is worse than
// the occasional music video slipping through and getting one.
const (
	// nonSpeechMaxWPM — ordinary speech runs 130-160 words per minute and even a
	// slow, pause-heavy tutorial clears 60. A music track's stray lyric
	// fragments land far below this.
	nonSpeechMaxWPM = 25.0
	// nonSpeechMinMarkerRatio — at least this share of the surviving cues must
	// have carried a sound-event marker. This is what protects a quiet
	// documentary or a silent screencast: no [Music] in its captions means it is
	// never flagged, however few words it has.
	nonSpeechMinMarkerRatio = 0.25
	// nonSpeechMinSeconds floors the duration so a very short clip cannot divide
	// the word rate by ~0 and look like speech.
	nonSpeechMinSeconds = 30
)

// IsNonSpeech reports whether the track carries music/ambience rather than
// speech, so the caller can skip summarization instead of letting the model
// hallucinate a summary out of stray lyric fragments. durationSeconds is the
// video's length; pass 0 when unknown and the last cue's start time is used.
//
// Both conditions must hold — see the threshold comments above.
func (p Parsed) IsNonSpeech(durationSeconds int) bool {
	if len(p.Cues) == 0 {
		return false // nothing parsed at all is the caller's empty-transcript case
	}
	secs := durationSeconds
	if secs <= 0 {
		secs = p.Cues[len(p.Cues)-1].StartSeconds
	}
	if secs < nonSpeechMinSeconds {
		secs = nonSpeechMinSeconds
	}
	wpm := float64(len(strings.Fields(p.Transcript))) / (float64(secs) / 60.0)
	ratio := float64(p.SoundEventCues) / float64(len(p.Cues))
	return wpm < nonSpeechMaxWPM && ratio >= nonSpeechMinMarkerRatio
}

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
	var markers []bool // parallel to cues: did this cue carry a sound-event marker?
	var curStart = -1
	var curLines []string
	var curMarker bool     // any line of the cue being built carried a marker
	var last string        // joined text of the last emitted/merged cue, for whole-cue-dup collapse
	var lastLines []string // lines of the last emitted/merged cue, for sliding-window line collapse

	flush := func() {
		hadMarker := curMarker
		curMarker = false
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
				markers[len(markers)-1] = markers[len(markers)-1] || hadMarker
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
		markers = append(markers, hadMarker)
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
		// Tags first, then entities: decoding first would turn an escaped
		// "&lt;c&gt;" spoken on screen into a real tag and tagRe would eat it.
		clean := unescapeEntities(tagRe.ReplaceAllString(line, ""))
		clean = strings.TrimSpace(clean)
		// Strip non-speech markers before the rolling-duplicate collapse below
		// sees the line: YouTube re-emits "[Music] I play" then "[Music] I play
		// games", and the collapse only works on the words that remain.
		clean, hadMarker := stripSoundEvents(clean)
		if hadMarker {
			curMarker = true
		}
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
	soundEvents := 0
	for i, c := range cues {
		texts[i] = c.Text
		if markers[i] {
			soundEvents++
		}
	}
	return Parsed{
		Transcript:     strings.Join(texts, " "),
		Cues:           cues,
		SoundEventCues: soundEvents,
	}, nil
}
