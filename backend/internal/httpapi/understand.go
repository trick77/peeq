package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/videos"
)

// Query understanding: the step that separates what a question is ABOUT from
// the words it is asked WITH.
//
// The problem it exists for. Ask embeds the sentence the reader typed, verbatim,
// and the keyword ladder ANDs its content terms. Neither has any notion that
// "what material about bike geometry do we have" is a question about bike
// geometry rather than about material. "material" is a real token in the query
// vector and a real term on three of the four keyword rungs, so a question
// phrased ABOUT THE LIBRARY searches for the words used to talk about libraries.
// The stopword list cannot fix this and should not try: it is deliberately
// conservative, and "material", "video" and "footage" are all words a reader
// might genuinely be searching for.
//
// So a small call reads the question first and reports two things: the topic,
// and whether the reader wants an answer or an inventory.
//
// WHAT IS DONE WITH THE TOPIC, and why it matters. The topic becomes an
// ADDITIONAL semantic lane beside the raw question — never a replacement for it.
// This is the whole safety property of the design. Rewriting is known to HURT
// retrieval when the query is already lexically aligned with the corpus, which
// is exactly what peeq's keyword ladder makes it; a rewrite that replaced the
// query would put that alignment at risk on every search. As a second lane, a
// good rewrite adds evidence, a bad one is outvoted by the four lanes that did
// not change, and the awkward question "does THIS query need rewriting?" never
// has to be answered at all.

// understandTimeout caps the pre-retrieval call. It is generous against a step
// measured in a second or so, and deliberately far below AskCallTimeout: this
// call sits in front of the first byte, so a slow one has to be abandoned rather
// than waited out. Falling back to the raw question costs a rewrite, not an
// answer.
const understandTimeout = 10 * time.Second

// understandMaxTokens bounds the reply. It is a topic phrase, a one-word label
// and a small filter object; anything longer is a model that has started
// explaining itself, and the parse will reject it anyway. Raised from 200 when
// the filters were added — a reply carrying two channel names and a date range
// is a few dozen tokens longer than one carrying a topic alone.
const understandMaxTokens = 350

// understandMaxChannels caps how many channel names one question may name.
// Beyond a handful the reader is not comparing channels, the model is
// hallucinating a list — and every name costs a resolution pass.
const understandMaxChannels = 4

// understandMaxChannelRunes bounds a single channel name. Real ones are short;
// a long one is a sentence that has been mislabelled as a channel.
const understandMaxChannelRunes = 60

// understandMaxTopicRunes rejects a "topic" long enough to be a paraphrase of
// the whole question. The point of the extra lane is a SHORT, topical query —
// one that sits closer to a chunk than a sentence of framing does. A model that
// hands back the original question with two words removed has not helped, and
// embedding it costs a lane slot for nothing.
const understandMaxTopicRunes = 80

// Intent labels. Deliberately two, and deliberately coarse: a label a short-gate
// model gets wrong is worse than no label, and these are the only two the answer
// path can actually do anything different about.
const (
	// intentContent — the reader wants an answer synthesized from what the
	// videos say. This is Ask's built shape and the safe default.
	intentContent = "content"
	// intentInventory — the reader wants to know WHAT THE LIBRARY HOLDS, not
	// what it says. "what material about bike geometry do we have" is this.
	intentInventory = "inventory"
)

// Watched labels. A question says one of three things about watch state, and
// "said nothing" has to be distinguishable from "said unwatched" — which is why
// this is a string here and a *bool by the time it reaches rag.Filter.
const (
	watchedUnwatched = "unwatched"
	watchedWatched   = "watched"
)

// queryFilters is the structured half of a question, in the model's own terms:
// NAMES, never ids. "does Veritasium have anything about ontology" carries a
// channel the same way it carries a topic — as the words the reader typed. Go
// resolves those words against the library (see resolve_channel.go); the model
// is never asked for a channel id, because an id is a thing it can only invent.
//
// Every field is optional and every field defaults to "the question did not say
// so". That asymmetry is the whole safety property: a filter the model imagines
// SHRINKS the search, and a shrunk search is the one failure a reader cannot
// see. The prompt says so twice for that reason, and the parse drops anything it
// cannot verify rather than passing it along.
type queryFilters struct {
	// Channels are channel names as written. An ARRAY, though it almost always
	// holds zero or one: "how do Veritasium and Kurzgesagt differ on X" has to
	// arrive as two names rather than as the single string "Veritasium and
	// Kurzgesagt", which would resolve to nothing at all.
	Channels []string `json:"channels"`
	// Watched is watchedUnwatched, watchedWatched, or "" for unsaid.
	Watched string `json:"watched"`
	// Favorite is set only by a question that actually says "favorite"; there
	// is no way to ask for non-favorites, and none is wanted.
	Favorite bool `json:"favorite"`
	// Category must be one of videos.Categories. A reply naming anything else
	// is dropped, never coined into a new category.
	Category string `json:"category"`
	// After and Before bound the release date as 'YYYY-MM-DD', inclusive.
	After  string `json:"after"`
	Before string `json:"before"`
}

// empty reports that the question carried no structured constraint at all, so
// the whole resolution step can be skipped.
func (f queryFilters) empty() bool {
	return len(f.Channels) == 0 && f.Watched == "" && !f.Favorite &&
		f.Category == "" && f.After == "" && f.Before == ""
}

// queryUnderstanding is what the call reports.
type queryUnderstanding struct {
	// Topic is the question with its framing removed: "bike geometry" from
	// "what material about bike geometry do we have". Empty when the question
	// carried no framing worth removing, when the model failed, or when there is
	// no understander wired — all of which mean "no extra lane", never an error.
	Topic string `json:"topic"`
	// Intent is one of the labels above. Always populated; defaults to
	// intentContent, because answering is the thing Ask can always do.
	Intent string `json:"intent"`
	// Filters is the structured half. The zero value means the question named
	// no constraint, which is both the common case and the safe one.
	Filters queryFilters `json:"filters"`
}

// understandStatus records how the step went, for the one log line. A silent
// fallback to the raw question is the failure most likely to go unnoticed —
// everything still works, just worse — so it has to be nameable.
type understandStatus string

const (
	understandOK       understandStatus = "ok"
	understandSkipped  understandStatus = "skipped" // no understander wired
	understandFailed   understandStatus = "failed"  // call or parse failed
	understandTimedOut understandStatus = "timeout"
	// understandNoop — the call succeeded and returned nothing worth using: an
	// empty topic, or one that merely repeats the question. Distinct from
	// "failed" because nothing went wrong; the question simply had no framing to
	// strip, which is the common case for a well-phrased query.
	understandNoop understandStatus = "noop"
)

// understandDiag is the step's contribution to the ask retrieval log line.
//
// No topic field: the topic reaches the log through askDiag, which is handed the
// same string as the retrieval input. Carrying a second copy here would give the
// line two sources for one fact, and they could disagree — askLanes is what
// decides whether the topic opened a lane at all.
type understandDiag struct {
	status understandStatus
	ms     int64
	intent string
	// filters is what survived the parse, rendered for the log.
	filters string
	// dropped names the filters the model produced and Go refused: an invalid
	// category, an unparseable date, a "watched" value that is neither label.
	// Without it a model quietly emitting garbage looks identical to a model
	// correctly emitting nothing, and the search silently loses a constraint the
	// reader did ask for.
	dropped []string
}

// understandSystemPrompt is built once at init because the category list is
// rendered into it — a model asked to pick a category must be shown the ones
// that exist, or it coins its own.
var understandSystemPrompt = buildUnderstandPrompt()

func buildUnderstandPrompt() string {
	return `You separate what a question is ABOUT from the words it is asked WITH, and from the constraints it puts on WHICH videos to look at.

You are given one question a reader typed into a personal video library. Reply with JSON and nothing else:

{"topic": "...", "intent": "content" | "inventory", "filters": {...}}

topic — the subject of the question, as the words that would appear in a video about it. Drop everything that refers to the library itself or to the act of asking: what, do we have, is there, show me, tell me, material, videos, content, footage, clips, anything. Drop the constraint words too — a channel name, "unwatched", a date — because those go in filters instead. Keep every word that carries subject matter, including ones that could look generic. If the question is already just its subject, repeat it unchanged.

intent — "inventory" when the reader is asking WHAT THE LIBRARY HOLDS ("what do we have on X", "any videos about X", "which videos cover X", "how many videos about X"). "content" when they want to know what the videos SAY ("how does X work", "what causes X", "why is X"). When it is genuinely both or you are unsure, answer "content".

filters — which videos to search, when the question restricts them. Every key is optional:

  "channels": ["..."] — channel names EXACTLY as the reader wrote them. Never an id, never a guess at the real spelling. One name per entry: "Veritasium and Kurzgesagt" is two entries, not one.
  "watched": "unwatched" | "watched" — only when the question says so.
  "favorite": true — only when the question says favorite, starred or loved.
  "category": one of ` + strings.Join(videos.CategoryIDs(), ", ") + ` — only when the question names a subject area as a CATEGORY of the library rather than as its topic. If in doubt this belongs in topic, not here.
  "after": "YYYY-MM-DD", "before": "YYYY-MM-DD" — release-date bounds, from a question that mentions time. The reader's date is given to you below; compute against it.

OMIT ANY FILTER THE QUESTION DOES NOT ACTUALLY STATE. An invented filter hides videos the reader asked to see, and they cannot tell that it happened. "filters": {} is the correct and common answer. When you are unsure whether the question meant a constraint, leave it out.

Examples:
Q: what material about bike geometry do we have
{"topic": "bike geometry", "intent": "inventory", "filters": {}}
Q: how does head angle affect handling
{"topic": "head angle handling", "intent": "content", "filters": {}}
Q: do we have unwatched videos about ontology
{"topic": "ontology", "intent": "inventory", "filters": {"watched": "unwatched"}}
Q: does Veritasium have anything about ontology
{"topic": "ontology", "intent": "inventory", "filters": {"channels": ["Veritasium"]}}
Q: how do Veritasium and Kurzgesagt differ on dark matter
{"topic": "dark matter", "intent": "content", "filters": {"channels": ["Veritasium", "Kurzgesagt"]}}
Q: anything on sourdough starters I haven't seen yet
{"topic": "sourdough starter", "intent": "inventory", "filters": {"watched": "unwatched"}}
Q: what did I watch about transients this year
{"topic": "transients", "intent": "inventory", "filters": {"watched": "watched", "after": "` + understandExampleYearStart + `"}}
Q: what are transients
{"topic": "transients", "intent": "content", "filters": {}}

No explanation, no code fences, no other keys.`
}

// understandExampleYearStart keeps the worked date example from teaching a
// specific year. The examples are static text, so a hard-coded 2024 would still
// be sitting there in 2027 as the model's idea of "this year".
const understandExampleYearStart = "<the 1st of January of the current year>"

// understandQuery runs the pre-retrieval call. It NEVER returns an error: every
// failure degrades to the raw question, which is exactly the behaviour Ask had
// before this step existed. The diag says which way it went.
// askerToday resolves the date the question is asked relative to. The client
// sends its OWN calendar date, because that is the only clock "yesterday" and
// "since March" can honestly mean — the server's local date belongs to whatever
// machine peeq happens to run on, which for a self-hosted instance reached from
// a phone abroad can be a day out.
//
// The fallback is UTC, not time.Now(): the bounds this call produces are matched
// against published_at, which is a UTC date. An unset or malformed parameter is
// silently ignored rather than failing the ask — it is a hint, and the whole
// understand step already degrades to the raw question on any trouble.
func askerToday(today string, now time.Time) string {
	if _, err := time.Parse("2006-01-02", today); err == nil {
		return today
	}
	return now.UTC().Format("2006-01-02")
}

func (s *server) understandQuery(ctx context.Context, q, today string) (queryUnderstanding, understandDiag) {
	// Not wired (chat unavailable, or a deployment that never configured it).
	// Ask still answers; it just answers the way it did before.
	if s.understand == nil {
		return queryUnderstanding{Intent: intentContent}, understandDiag{
			status: understandSkipped, intent: intentContent,
		}
	}

	cctx, cancel := context.WithTimeout(ctx, understandTimeout)
	defer cancel()
	// ShortGate and WithoutThinking say different things and both are needed:
	// the first asks for the deployment that answers sooner (Pro is where the
	// long thinking calls queue, and this call sits in front of the first byte),
	// the second stops the model reasoning about a question that is a labelling
	// job. See llm/calloptions.go. reasoning_effort is NOT the lever here — it is
	// measured inert against this endpoint.
	cctx = llm.ShortGate(llm.WithoutThinking(cctx))
	cctx = llm.WithMaxTokens(cctx, understandMaxTokens)
	cctx = llm.WithCall(cctx, llm.CallInfo{Step: "understand"})

	started := time.Now()
	// Today's date rides on the USER message, not the system one: it changes
	// daily, and putting it in the system prompt would break the prompt cache
	// every midnight for a call whose whole point is to be cheap. "since March"
	// cannot be resolved without it — and it is the ASKER's today, not this
	// machine's, which is what askerToday is for.
	raw, err := s.understand.Complete(cctx, []llm.Message{
		{Role: "system", Content: understandSystemPrompt},
		{Role: "user", Content: "Today is " + askerToday(today, started) + ".\nQ: " + q},
	})
	elapsed := time.Since(started).Milliseconds()

	if err != nil {
		status := understandFailed
		// Either bound counts as a timeout: understandTimeout above, or the
		// llm.Client's own call cap, which fires on a context this function never
		// sees — so cctx.Err() alone would report that one as a plain failure.
		if errors.Is(err, context.DeadlineExceeded) || cctx.Err() == context.DeadlineExceeded {
			status = understandTimedOut
		}
		return queryUnderstanding{Intent: intentContent}, understandDiag{
			status: status, ms: elapsed, intent: intentContent,
		}
	}

	u, dropped, ok := parseUnderstanding(raw)
	if !ok {
		return queryUnderstanding{Intent: intentContent}, understandDiag{
			status: understandFailed, ms: elapsed, intent: intentContent,
		}
	}
	// A topic that merely restates the question buys nothing and costs a lane.
	//
	// Compare NORMALIZED to normalized. u.Topic has been through sanitizeTopic,
	// which collapses runs of whitespace; q has not. Trimming q alone lets
	// "what  are  transients" (a paste, or ?q=what++are++transients) slip past as
	// a "different" phrasing, and the topic lane then embeds the SAME sentence a
	// second time. That is not a harmless duplicate: FuseWeighted sums across
	// lanes, so one phrasing counted twice earns 1.2 and outscores a strict
	// keyword rung at 1.0 — the score that is supposed to mean two phrasings
	// agreeing on a passage.
	//
	// Note this judges the TOPIC only. A question can leave nothing to strip and
	// still carry a filter — "unwatched ontology" is a noop topic and a real
	// constraint — so u is returned whole either way and only the status differs.
	if u.Topic == "" || strings.EqualFold(u.Topic, strings.Join(strings.Fields(q), " ")) {
		u.Topic = ""
		return u, understandDiag{
			status: understandNoop, ms: elapsed, intent: u.Intent,
			filters: u.Filters.describe(), dropped: dropped,
		}
	}
	return u, understandDiag{
		status: understandOK, ms: elapsed, intent: u.Intent,
		filters: u.Filters.describe(), dropped: dropped,
	}
}

// describe renders the filters for the log line in the order they are applied.
// Empty when nothing was asked for, which is what most questions produce.
func (f queryFilters) describe() string {
	var parts []string
	if len(f.Channels) > 0 {
		parts = append(parts, "channel="+strings.Join(f.Channels, "|"))
	}
	if f.Watched != "" {
		parts = append(parts, f.Watched)
	}
	if f.Favorite {
		parts = append(parts, "favorite")
	}
	if f.Category != "" {
		parts = append(parts, "category="+f.Category)
	}
	if f.After != "" {
		parts = append(parts, "after="+f.After)
	}
	if f.Before != "" {
		parts = append(parts, "before="+f.Before)
	}
	return strings.Join(parts, ",")
}

// parseUnderstanding reads the model's reply. It is defensive in the two ways
// that actually happen: a JSON object wrapped in a code fence, and prose before
// or after it. Anything else is a failure, and a failure means the raw question.
//
// The filters get the same treatment one level down, and then some: a filter is
// only kept when Go can independently verify it. An unknown category, a date
// that will not parse, a "watched" value that is neither label — each is
// DROPPED and named, not passed through and not treated as a failure. The
// asymmetry is deliberate. A wrong topic costs a lane and gets outvoted by four
// others; a wrong filter hides videos, and nothing downstream can tell that it
// happened.
//
// The second return value names what was dropped, for the log.
func parseUnderstanding(raw string) (queryUnderstanding, []string, bool) {
	body := strings.TrimSpace(raw)
	if body == "" {
		return queryUnderstanding{}, nil, false
	}
	// Strip a fenced block, ```json or bare ```.
	if strings.HasPrefix(body, "```") {
		if i := strings.IndexByte(body, '\n'); i >= 0 {
			body = body[i+1:]
		}
		body = strings.TrimSuffix(strings.TrimSpace(body), "```")
	}
	// Take the outermost object, so leading or trailing prose is survivable.
	start := strings.IndexByte(body, '{')
	end := strings.LastIndexByte(body, '}')
	if start < 0 || end <= start {
		return queryUnderstanding{}, nil, false
	}

	var parsed struct {
		Topic   string `json:"topic"`
		Intent  string `json:"intent"`
		Filters struct {
			Channels []string `json:"channels"`
			Watched  string   `json:"watched"`
			Favorite bool     `json:"favorite"`
			Category string   `json:"category"`
			After    string   `json:"after"`
			Before   string   `json:"before"`
		} `json:"filters"`
	}
	if err := json.Unmarshal([]byte(body[start:end+1]), &parsed); err != nil {
		return queryUnderstanding{}, nil, false
	}

	u := queryUnderstanding{
		Topic:  sanitizeTopic(parsed.Topic),
		Intent: intentContent,
	}
	// Only the label we know. An unrecognized one is not an error — the topic is
	// still usable — it just means the safe default applies.
	if strings.EqualFold(strings.TrimSpace(parsed.Intent), intentInventory) {
		u.Intent = intentInventory
	}

	var dropped []string
	drop := func(what string) { dropped = append(dropped, what) }

	for _, raw := range parsed.Filters.Channels {
		name := sanitizeChannelName(raw)
		if name == "" {
			// A blank entry is nothing to report; one that sanitizing emptied —
			// a whole sentence mislabelled as a channel — is a constraint the
			// reader may have asked for and did not get, so it is named.
			if strings.TrimSpace(raw) != "" {
				drop("channel:" + truncateRunes(strings.Join(strings.Fields(raw), " "), understandMaxChannelRunes))
			}
			continue
		}
		if len(u.Filters.Channels) >= understandMaxChannels {
			drop("channels:too-many")
			break
		}
		u.Filters.Channels = append(u.Filters.Channels, name)
	}

	switch strings.ToLower(strings.TrimSpace(parsed.Filters.Watched)) {
	case "":
		// The common case: the question said nothing about watch state.
	case watchedUnwatched:
		u.Filters.Watched = watchedUnwatched
	case watchedWatched:
		u.Filters.Watched = watchedWatched
	default:
		// "maybe", "partially", "seen" — a model improvising a third state. It
		// cannot be mapped to a column, so it is dropped rather than guessed at.
		drop("watched:" + strings.TrimSpace(parsed.Filters.Watched))
	}

	u.Filters.Favorite = parsed.Filters.Favorite

	if c := strings.TrimSpace(parsed.Filters.Category); c != "" {
		// NormalizeCategory repairs the wrappers models habitually add and maps
		// a display label back to its id; anything it cannot place comes back as
		// the uncategorized fallback, which is a state peeq assigns and never an
		// answer to a question. Either way it is not a category to filter on.
		if id := videos.NormalizeCategory(c); videos.ValidCategory(id) && id != videos.UncategorizedCategory {
			u.Filters.Category = id
		} else {
			drop("category:" + c)
		}
	}

	u.Filters.After, u.Filters.Before = parseDateBound(parsed.Filters.After, "after", drop),
		parseDateBound(parsed.Filters.Before, "before", drop)
	// An inverted range admits nothing at all, which would read to the reader as
	// "your library has nothing on this". Drop both bounds instead: a search that
	// is too wide is visibly too wide, a search that is empty is not.
	if u.Filters.After != "" && u.Filters.Before != "" && u.Filters.After > u.Filters.Before {
		drop("dates:inverted")
		u.Filters.After, u.Filters.Before = "", ""
	}

	return u, dropped, true
}

// parseDateBound keeps only a date Go can actually parse, in the one format the
// videos table stores. "last week", "2026", "March" and an empty string all
// yield nothing; only the first is worth naming as dropped.
func parseDateBound(s, which string, drop func(string)) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if _, err := time.Parse("2006-01-02", s); err != nil {
		drop(which + ":" + s)
		return ""
	}
	return s
}

// sanitizeChannelName bounds and cleans one name before it is used to look
// anything up. Same treatment as sanitizeTopic and for the same reason: it is
// model output, and it reaches both a SQL parameter and the reader's screen.
func sanitizeChannelName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len([]rune(s)) > understandMaxChannelRunes {
		return ""
	}
	return s
}

// sanitizeTopic reduces the returned topic to something safe to embed, to log
// and to show in the UI. It is model output that reaches a reader's screen, so
// it is treated like any other untrusted string: control characters out,
// whitespace collapsed, length bounded.
func sanitizeTopic(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len([]rune(s)) > understandMaxTopicRunes {
		// Dropped, and said so. Downstream this lands in the same "noop" status as
		// a question that simply had no framing to strip — but those are not the
		// same event: that one is the ordinary case, and this one is the model
		// handing back a paraphrase instead of a topic. Silent, it would look like
		// the step was working and merely finding nothing to do.
		slog.Warn("understand: topic too long, dropped",
			"runes", len([]rune(s)), "max", understandMaxTopicRunes)
		return ""
	}
	return s
}
