package summarize

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/trick77/peeq/internal/rag"
)

// This file reads the in-depth summary's text format back into its lead and
// sections. It is the Go twin of ui/src/inDepth.ts and must stay in lockstep
// with it: the UI's parse is what the reader sees, this one is what Ask
// searches. Both read testdata/indepth_parse.{txt,json}; a rule added to one
// side goes into the other and into that fixture.
//
// Whitespace follows JavaScript, not Go: JS \s and trim() take U+FEFF and not
// U+0085, unicode.IsSpace the reverse, Go's regexp \s is ASCII only, and JS "."
// stops at \r, U+2028 and U+2029. jsSpaceClass and isJSSpace encode that.

// jsSpaceClass is JavaScript's \s as a Go character class.
const jsSpaceClass = `[\t\n\v\f\r \p{Zs}\x{2028}\x{2029}\x{FEFF}]`

// isJSSpace is JavaScript's \s and trim() set: unicode.IsSpace minus U+0085,
// plus U+FEFF.
func isJSSpace(r rune) bool {
	return r == 0xFEFF || (r != 0x85 && unicode.IsSpace(r))
}

func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

// inDepthHeading is a heading line: "###" and its text. The space after the
// hashes is optional — models drop it often enough to matter. The text class
// is JS ".", which does not cross a line terminator.
var inDepthHeading = regexp.MustCompile(`^###` + jsSpaceClass + `*([^\r\n\x{2028}\x{2029}]+)$`)

// inDepthStamp is the stamp, in square or round brackets, anywhere in the
// heading — models move it to the front or glue a period after it.
var inDepthStamp = regexp.MustCompile(`[\[(](\d{1,2}(?::\d{2}){1,2})[\])]`)

var (
	inDepthBold      = regexp.MustCompile(`\*\*|__`)
	inDepthParaBreak = regexp.MustCompile(`\n` + jsSpaceClass + `*\n`)
)

type parsedInDepth struct {
	Lead     []string
	Sections []parsedInDepthSection
}

// parsedInDepthSection holds a stamp as the model wrote it and in seconds.
// No stamp is Stamp "" and Seconds -1.
type parsedInDepthSection struct {
	Heading string
	Stamp   string
	Seconds int
	Body    []string
}

// parseInDepth is ui/src/inDepth.ts's parseInDepth: text before the first
// heading is the lead, every heading opens a section, paragraphs split on
// blank lines.
func parseInDepth(text string) parsedInDepth {
	var lead []string
	type open struct {
		heading, stamp string
		lines          []string
	}
	var sections []open
	for _, line := range strings.Split(text, "\n") {
		if m := inDepthHeading.FindStringSubmatch(jsTrim(line)); m != nil {
			h, s := readInDepthHeading(m[1])
			sections = append(sections, open{heading: h, stamp: s})
		} else if len(sections) > 0 {
			sections[len(sections)-1].lines = append(sections[len(sections)-1].lines, line)
		} else {
			lead = append(lead, line)
		}
	}
	out := parsedInDepth{Lead: inDepthParagraphs(lead)}
	for _, s := range sections {
		secs := -1
		if s.stamp != "" {
			secs = stampSeconds(s.stamp)
		}
		out.Sections = append(out.Sections, parsedInDepthSection{
			Heading: s.heading, Stamp: s.stamp, Seconds: secs, Body: inDepthParagraphs(s.lines),
		})
	}
	return out
}

// readInDepthHeading splits a heading's text into its words and its stamp,
// dropping the markup models add although the prompt forbids it: bold markers
// and the punctuation left behind once the stamp is cut out.
func readInDepthHeading(raw string) (heading, stamp string) {
	text := raw
	if loc := inDepthStamp.FindStringSubmatchIndex(raw); loc != nil {
		stamp = raw[loc[2]:loc[3]]
		text = raw[:loc[0]] + raw[loc[1]:]
	}
	text = strings.Join(strings.FieldsFunc(inDepthBold.ReplaceAllString(text, ""), isJSSpace), " ")
	heading = strings.TrimFunc(text, func(r rune) bool {
		return isJSSpace(r) || strings.ContainsRune(".:–—-", r)
	})
	return heading, stamp
}

func inDepthParagraphs(lines []string) []string {
	var out []string
	for _, p := range inDepthParaBreak.Split(strings.Join(lines, "\n"), -1) {
		if p = jsTrim(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func stampSeconds(stamp string) int {
	total := 0
	for _, part := range strings.Split(stamp, ":") {
		n, _ := strconv.Atoi(part) // the stamp regexp admits digits only
		total = total*60 + n
	}
	return total
}

// inDepthSections is what Ask indexes from a stored in-depth body: each
// section with its paragraphs rejoined. The lead is dropped (the summary chunk
// already says it).
//
// A section with no stamp, or a stamp at or past the video's end, is dropped
// too. Every hit seeks, so it would have to be parked somewhere: at 0s it
// rewinds the reader's resume position, borrows the first chapter's name in
// the Ask prompt and counts as a moment in Find. The prompt demands stamps, so
// this costs only the rare reply that ignored it; its transcript stays
// searchable. durationSeconds <= 0 is unknown and bounds nothing.
func inDepthSections(body string, durationSeconds int) []rag.InDepthSection {
	var out []rag.InDepthSection
	for _, s := range parseInDepth(body).Sections {
		if s.Seconds < 0 || (durationSeconds > 0 && s.Seconds >= durationSeconds) {
			continue
		}
		out = append(out, rag.InDepthSection{
			Heading:      s.Heading,
			StartSeconds: s.Seconds,
			Body:         strings.Join(s.Body, "\n\n"),
		})
	}
	return out
}
