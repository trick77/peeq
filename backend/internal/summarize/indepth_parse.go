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
// Whitespace classes are widened to JavaScript's \s where the two regexp
// engines differ (Go's \s is ASCII only), so a non-breaking space splits and
// collapses the same on both sides.

// inDepthHeading is a heading line: "###" and its text. The space after the
// hashes is optional — models drop it often enough to matter.
var inDepthHeading = regexp.MustCompile(`^###\s*(.+)$`)

// inDepthStamp is the stamp, in square or round brackets, anywhere in the
// heading — models move it to the front or glue a period after it.
var inDepthStamp = regexp.MustCompile(`[\[(](\d{1,2}(?::\d{2}){1,2})[\])]`)

var (
	inDepthBold      = regexp.MustCompile(`\*\*|__`)
	inDepthParaBreak = regexp.MustCompile(`\n[\s\v\p{Zs}\x{2028}\x{2029}\x{FEFF}]*\n`)
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
		if m := inDepthHeading.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
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
	text = strings.Join(strings.Fields(inDepthBold.ReplaceAllString(text, "")), " ")
	heading = strings.TrimFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(".:–—-", r)
	})
	return heading, stamp
}

func inDepthParagraphs(lines []string) []string {
	var out []string
	for _, p := range inDepthParaBreak.Split(strings.Join(lines, "\n"), -1) {
		if p = strings.TrimSpace(p); p != "" {
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
// already says it), and an unstamped section sits at 0 like the summary does.
func inDepthSections(body string) []rag.InDepthSection {
	var out []rag.InDepthSection
	for _, s := range parseInDepth(body).Sections {
		out = append(out, rag.InDepthSection{
			Heading:      s.Heading,
			StartSeconds: max(s.Seconds, 0),
			Body:         strings.Join(s.Body, "\n\n"),
		})
	}
	return out
}
