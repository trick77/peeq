package httpapi

import (
	"log/slog"
	"strings"
	"unicode"

	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/videos"
)

// Turning the names a question used into the ids a search can filter on.
//
// The model never emits a channel id, and is told not to try. An id is a thing
// it can only invent, and an invented one filters the search down to nothing
// while looking exactly like a correct one. So the model reports the name as the
// reader wrote it, and this file matches that string against the channels the
// library actually holds.
//
// A match is applied SILENTLY: "Veritaseum" becomes Veritasium with no sentence
// spent on it, because the applied filter is already shown to the reader as a
// chip. A name that matches NOTHING is a different event and is not silent —
// see channelResolution.Unresolved and how handleAnswer uses it. The reader who
// asked about a channel that is not here must not be handed another channel's
// videos with no indication that their question was quietly widened.

// resolveMaxDistance is the edit-distance budget for the typo tier. Two is
// enough for a transposition and a wrong vowel ("Veritaseum", "Kurzgesagd") and
// small enough that it will not reach from one real channel name to another.
const resolveMaxDistance = 2

// resolveMinFuzzyRunes withholds the typo tier from very short names. At three
// characters a budget of two rewrites almost anything into almost anything.
const resolveMinFuzzyRunes = 5

// channelResolution is what the ladder made of the names a question carried.
type channelResolution struct {
	// IDs and Names go straight into rag.Filter. Names is the by-name arm for
	// legacy rows whose channel_id was never recorded.
	IDs   []string
	Names []string
	// Matched is the canonical display name of everything that resolved, in the
	// order asked. This is what the reader is shown, so a silent typo fix is
	// still visible as the corrected name rather than as the typo.
	Matched []string
	// Unresolved is every name that matched nothing at all. Not an error and
	// not a filter — the constraint is dropped — but the reader is told.
	Unresolved []string
	// Ambiguous marks that at least one name matched several channels at the
	// same tier and the union of them was taken. Kept apart from a reader who
	// deliberately named two channels: the first is uncertainty, the second is
	// a comparison, and only the second should switch the answer to summaries.
	Ambiguous bool
}

// any reports whether anything resolved, i.e. whether a channel constraint
// should be applied at all.
func (c channelResolution) any() bool { return len(c.IDs) > 0 || len(c.Names) > 0 }

// resolveChannels maps the names a question named onto the library's channels.
//
// It never fails the request: a directory that cannot be read logs and returns
// an empty resolution, which means no channel constraint — the same search the
// reader would have got before any of this existed.
func (s *server) resolveChannels(names []string) channelResolution {
	var out channelResolution
	if len(names) == 0 || s.videos == nil {
		return out
	}
	dir, err := s.videos.ChannelDirectory()
	if err != nil {
		slog.Warn("search: channel directory unavailable, ignoring channel filter", "err", err)
		return out
	}

	seenID := map[string]bool{}
	seenName := map[string]bool{}
	for _, name := range names {
		matches := matchChannels(dir, name)
		if len(matches) == 0 {
			out.Unresolved = append(out.Unresolved, name)
			continue
		}
		if len(matches) > 1 {
			out.Ambiguous = true
		}
		for _, m := range matches {
			// An empty id is a row from before ids were recorded; it can only be
			// matched by name, and the filter's name arm is gated on
			// channel_id = '' so it cannot widen an id match.
			if m.ID != "" && !seenID[m.ID] {
				seenID[m.ID] = true
				out.IDs = append(out.IDs, m.ID)
			}
			if m.ID == "" && !seenName[m.Name] {
				seenName[m.Name] = true
				out.Names = append(out.Names, m.Name)
			}
			out.Matched = append(out.Matched, m.Name)
		}
	}
	return out
}

// matchChannels runs one name down the ladder and returns everything that
// matched at the FIRST tier to match anything. Tiers are tried in order of how
// much they assume, so an exact name never competes with a substring.
//
// Several matches at one tier are all returned rather than arbitrated. Picking
// one would be a guess the reader cannot see; searching all of them is a wider
// answer they can.
func matchChannels(dir []videos.ChannelRef, name string) []videos.ChannelRef {
	want := strings.TrimSpace(name)
	if want == "" {
		return nil
	}
	wantNorm := normalizeChannel(want)
	if wantNorm == "" {
		return nil
	}
	wantHandle := strings.TrimPrefix(strings.ToLower(want), "@")

	tiers := []func(c videos.ChannelRef) bool{
		// Exact name, case-insensitive.
		func(c videos.ChannelRef) bool { return strings.EqualFold(c.Name, want) },
		// The @handle, with or without the sigil on either side.
		func(c videos.ChannelRef) bool {
			h := strings.TrimPrefix(strings.ToLower(c.Handle), "@")
			return h != "" && h == wantHandle
		},
		// Normalized: "the-verge" finds "The Verge", "veritasium " finds
		// "Veritasium".
		func(c videos.ChannelRef) bool { return normalizeChannel(c.Name) == wantNorm },
		// Substring, so "Kurzgesagt" finds "Kurzgesagt – In a Nutshell".
		func(c videos.ChannelRef) bool {
			n := normalizeChannel(c.Name)
			return n != "" && strings.Contains(n, wantNorm)
		},
		// Typo, last: only for names long enough that the budget cannot rewrite
		// one real channel into another.
		func(c videos.ChannelRef) bool {
			if len([]rune(wantNorm)) < resolveMinFuzzyRunes {
				return false
			}
			return editDistanceWithin(normalizeChannel(c.Name), wantNorm, resolveMaxDistance)
		},
	}

	for _, match := range tiers {
		var hits []videos.ChannelRef
		for _, c := range dir {
			if match(c) {
				hits = append(hits, c)
			}
		}
		if len(hits) > 0 {
			return hits
		}
	}
	return nil
}

// normalizeChannel reduces a name to its letters and digits, lowercased, so
// punctuation, spacing and decoration stop mattering. "Kurzgesagt – In a
// Nutshell" and "kurzgesagt in a nutshell" normalize alike.
func normalizeChannel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// editDistanceWithin reports whether a and b are within max edits of each other.
// It bails out as soon as the whole row exceeds max, so a long name is cheap to
// reject rather than expensive to measure.
func editDistanceWithin(a, b string, max int) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra)-len(rb) > max || len(rb)-len(ra) > max {
		return false
	}
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		best := curr[0]
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
			if curr[j] < best {
				best = curr[j]
			}
		}
		if best > max {
			return false
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)] <= max
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// buildFilter turns the understood question into the filter retrieval runs
// under. Everything in the result has been verified against something real: the
// channels against the library's own directory, the category and dates against
// the parse in understand.go. Nothing the model wrote reaches SQL as text.
func (s *server) buildFilter(f queryFilters, ch channelResolution) rag.Filter {
	out := rag.Filter{
		ChannelIDs:   ch.IDs,
		ChannelNames: ch.Names,
		Category:     f.Category,
		After:        f.After,
		Before:       f.Before,
	}
	switch f.Watched {
	case watchedUnwatched:
		no := false
		out.Watched = &no
	case watchedWatched:
		yes := true
		out.Watched = &yes
	}
	if f.Favorite {
		yes := true
		out.Favorite = &yes
	}
	return out
}

// describeFilter renders the filter that was ACTUALLY APPLIED, in the reader's
// words, for the progress frame and the log. It is built from the resolution
// rather than from the model's reply on purpose: a channel that did not resolve
// must not appear here as though it had narrowed the search.
func describeFilter(f queryFilters, ch channelResolution) []string {
	var out []string
	if len(ch.Matched) > 0 {
		out = append(out, ch.Matched...)
	}
	switch f.Watched {
	case watchedUnwatched:
		out = append(out, "unwatched")
	case watchedWatched:
		out = append(out, "watched")
	}
	if f.Favorite {
		out = append(out, "favorites")
	}
	if f.Category != "" {
		out = append(out, videos.CategoryLabel(f.Category))
	}
	switch {
	case f.After != "" && f.Before != "":
		out = append(out, f.After+" to "+f.Before)
	case f.After != "":
		out = append(out, "since "+f.After)
	case f.Before != "":
		out = append(out, "before "+f.Before)
	}
	return out
}
