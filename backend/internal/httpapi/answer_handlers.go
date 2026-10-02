package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/sse"
	"github.com/trick77/peeq/internal/videos"
)

// StreamCompleter is the slice of llm.Client the answer endpoint uses:
// a streaming completion whose fragments are relayed to the browser as they
// arrive. Declared at the consumer, like the other collaborator interfaces.
//
// Optional — a nil one degrades the endpoint to citations without an answer,
// never to an error. Same typed-nil caveat as the others: the handler checks
// the INTERFACE.
type StreamCompleter interface {
	CompleteStream(ctx context.Context, messages []llm.Message, onDelta func(string)) (string, error)
	modelNamer
}

// modelNamer names the model a call made with ctx reaches, so the trace can
// say what actually ran rather than a name written out here.
type modelNamer interface {
	ModelFor(ctx context.Context) string
}

// Completer is the non-streaming slice of llm.Client, used by the
// query-understanding step: one short reply read in full, not relayed. Declared
// separately from StreamCompleter rather than widened onto it so a deployment
// can wire the answer without the pre-step.
//
// Optional in the same way: a nil one skips understanding and Ask searches the
// raw question, exactly as it did before the step existed.
type Completer interface {
	Complete(ctx context.Context, messages []llm.Message) (string, error)
	modelNamer
}

// answerSource is one cited passage, in the shape the UI needs to render a
// citation, list it as a source, and open the player at it.
type answerSource struct {
	N            int    `json:"n"`
	VideoID      string `json:"video_id"`
	Title        string `json:"title"`
	ChannelName  string `json:"channel_name,omitempty"`
	StartSeconds int    `json:"start_seconds"`
	Kind         string `json:"kind"`
	// Snippet is the passage preview, match-centred and carrying
	// rag.HighlightStart/End around matched terms when the keyword lane found
	// it. Ask renders its moments from these sources rather than from a second
	// /api/search request, so the preview has to travel with them.
	Snippet string `json:"snippet"`
}

// answerVideo is the video behind one or more sources, in the shape a result
// card reads: a thumbnail, a duration, a title, a channel.
//
// Deliberately NOT videoDTO. That carries the full summary text, the chapter
// and key-point blobs, the description and the whole media-probe set — none of
// which a card renders, and twelve of them would dominate a stream whose point
// is to start fast.
type answerVideo struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	ChannelID       string `json:"channel_id"`
	ChannelName     string `json:"channel_name"`
	DurationSeconds int64  `json:"duration_seconds"`
	HasThumbnail    bool   `json:"has_thumbnail"`
	// ThumbnailVersion keeps a cited source's poster on the same immutable URL
	// the Library card already asked for, so an answer reuses that cache entry
	// instead of opening a second one for the same picture.
	ThumbnailVersion string `json:"thumbnail_version,omitempty"`
	// Status is here for one distinction the card has to draw: 'new' means peeq
	// read this video but never downloaded it, so the card badges it and drops
	// the play affordance rather than promising a file that does not exist.
	Status string `json:"status"`
	// PublishedAt is the air date, YYYY-MM-DD, so a cited card carries the same
	// byline every other card in the app does. Omitted for a video whose date
	// was never learned; the card then simply shows the channel.
	PublishedAt string `json:"published_at,omitempty"`
}

func toAnswerVideo(v *videos.Video) answerVideo {
	return answerVideo{
		ID: v.ID, Title: v.Title, ChannelID: v.ChannelID,
		ChannelName: v.ChannelName, DurationSeconds: v.DurationSeconds,
		HasThumbnail:     v.HasThumbnail,
		ThumbnailVersion: v.ThumbnailVersion,
		Status:           v.Status,
		PublishedAt:      v.PublishedAt,
	}
}

const (
	// answerMaxSources is how many passages the model is given. Enough for a
	// question spanning several videos, small enough that the citation list
	// stays readable and the prompt stays cheap.
	answerMaxSources = 12
	// answerMaxSourcesPerVideo stops one thorough video from being the entire
	// evidence set for a question the library answers from several angles.
	answerMaxSourcesPerVideo = 3
	// answerBreadthSources is how many of the slots the breadth pass may claim
	// before depth gets the rest. See chooseExcerpts: without it, a library with
	// twelve or more matching videos gives every one of them a single passage and
	// the best-matching video no more than the twelfth-best, which is the same
	// failure as concentrating on four videos, mirrored.
	//
	// Eight leaves four slots for depth and still shows more videos than the plain
	// keyword search does for the query this was measured on.
	answerBreadthSources = 8
	// answerExcerptRunes truncates a single passage. A chunk is ~600 tokens;
	// the model needs the gist, not every word, and 12 untruncated chunks would
	// dominate the request.
	answerExcerptRunes = 1200
	// answerMaxAnswerTokens bounds what the answer can cost. It is a ceiling,
	// not a target: the prompt asks for at most six sentences, which is a couple
	// of hundred tokens. The reasoning allowance on top is the model profile's
	// (llm.WithMaxAnswerTokens). A budget spent thinking still ends the stream
	// with finish_reason "length" and no content, which the caller reports as an
	// empty answer rather than a blank panel.
	answerMaxAnswerTokens = 1500
)

// handleAnswer answers GET /api/search/answer?q=: it runs the same retrieval
// Ask mode uses, then streams a grounded answer over SSE.
//
// Frames are always progress, then sources, then zero or more token frames,
// then done — with an error frame in place of the tokens when there is no answer
// to give. The citation table goes early because retrieval finishes before
// generation starts, so it is already known, and a failure mid-answer still
// leaves the reader a usable list of moments.
//
// The one exception is a blank query, which returns before any of that runs and
// sends sources, then done: there is no question to understand, so there is no
// progress to report.
//
// progress comes first and carries the understood query. It exists because the
// pre-retrieval step put a second or so of silence in front of everything else:
// without a frame there, the reader watches a spinner that claims searching has
// begun before it has. It is also where the extracted topic is surfaced, which
// is what makes a bad rewrite visible instead of silent.
func (s *server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	// The ONE case that must refuse rather than degrade, and therefore the one
	// check that has to happen before the stream opens: sse.NewWriter writes
	// headers, and after that the status is locked to 200 with no way back to a
	// 503. Everything else — a blank query, a query nothing matched, a chat
	// endpoint that is down — is a legitimate 200 whose content says so, and is
	// answered through the stream so the browser has a single code path.
	if s.rag == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "search is not configured")
		return
	}

	writer, err := sse.NewWriter(w)
	if err != nil {
		serverError(w, r, err, "streaming unsupported")
		return
	}
	stopHeartbeat := writer.Heartbeat(r.Context(), sseHeartbeatInterval)
	defer stopHeartbeat()
	send := func(event string, payload any) bool {
		b, merr := json.Marshal(payload)
		if merr != nil {
			return true
		}
		return writer.Send(event, string(b)) == nil
	}
	defer func() { send("done", map[string]string{"reason": "stop"}) }()

	// How the answer was made, sent as one frame once it has been.
	//
	// A DEFER, and registered after done's on purpose: defers unwind last-in
	// first-out, so this one runs BEFORE done and the frame lands where the
	// client expects it — last but one. It has to be a defer rather than a
	// statement because handleAnswer returns from six different places (client
	// gone, nothing retrieved, no chat wired, a failed stream), and a trace that
	// only arrived on the happy path would be missing from exactly the answers
	// worth investigating.
	//
	// A failed answer therefore traces every step that ran, with the generation
	// row named for what happened to it — the call was made and cost real time,
	// so it is listed, but as "Couldn't write the answer" rather than as having
	// written one. An answer the model was never asked for has no row at all.
	var tr answerTrace
	// Declared up here rather than beside the model call so the defer below can
	// read it: the log line and the stream frame are written in the same place,
	// which is what stops them ever describing different runs.
	var ttft time.Duration
	// What the "Also in your library" tier did with what retrieval offered.
	// Declared here for the same reason ttft is: the defer below logs it, and it
	// is computed much further down.
	var cov coverageDiag

	askCtx := r.Context()

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		// Registered below this return on purpose, so a blank query keeps its
		// two-frame stream and writes no log line. Nothing ran; there is nothing
		// to trace and nothing to say about it.
		send("sources", map[string]any{"sources": []any{}, "videos": []any{}})
		return
	}

	defer func() {
		// Belt and braces after the blank-query return above: a client that hung
		// up before the first stage completed leaves nothing worth reporting.
		if len(tr.stages) == 0 {
			return
		}
		send("trace", map[string]any{"stages": tr.stages})
		// Logged whether or not the frame reached anyone. A client that hung up
		// mid-answer is a case worth having a record of, not one worth losing.
		tr.log(q, ttft, cov)
	}()

	// Understand the question before searching for it. This is the step that
	// stops "what material about bike geometry do we have" searching for the word
	// "material"; see understand.go for why it adds a lane instead of replacing
	// the query. It never fails the request — a bad or absent understanding just
	// means the raw question, which is what this endpoint did before.
	u, ud := s.understandQuery(askCtx, q)
	// Skipped means no understander is wired, so there was no step to report.
	if ud.status != understandSkipped {
		tr.add("understand", ud.model, traceKindModel, ud.ms)
	}

	// Resolve the structured half against the library before searching under it.
	// The model reported channel NAMES; only the library can say which ids those
	// are, and a name it cannot place drops out here rather than filtering the
	// search down to nothing. See resolve_channel.go.
	channelStart := time.Now()
	ch := s.resolveChannels(u.Filters.Channels)
	filter := s.buildFilter(u.Filters, ch)
	applied := describeFilter(u.Filters, ch)
	// Only when the question actually named a channel. resolveChannels returns
	// immediately on an empty list without touching the library, so tracing it
	// unconditionally would report a lookup that never happened on the majority
	// of questions.
	if len(u.Filters.Channels) > 0 {
		tr.add("channels", "sqlite", traceKindLocal, time.Since(channelStart).Milliseconds())
	}

	// The reader has now been waiting a second or so with nothing on the wire, so
	// say what happened before starting retrieval. The topic travels with it: a
	// silent rewrite that quietly mangles the question is the main risk of this
	// whole design, and showing the reader what was actually searched for is the
	// cheapest possible guard against it. The filter travels for the same reason
	// and is the stronger case: a rewrite makes the answer worse, a filter makes
	// videos disappear.
	if !send("progress", map[string]any{
		"phase": "retrieving", "topic": u.Topic, "counting": u.Counting,
		"filters": applied, "unresolved_channels": ch.Unresolved,
	}) {
		return // client gone
	}

	var qv queryVectors
	lanes, diag := s.askLanes(r, q, u.Topic, filter, &qv)
	diag.understand, diag.understandMs = string(ud.status), ud.ms
	diag.counting = ud.counting
	diag.filters, diag.filtersDropped = strings.Join(applied, "|"), ud.dropped
	diag.unresolved = ch.Unresolved
	mergeStart := time.Now()
	hits := rag.FuseWeighted(lanes, searchCandidates)
	mergeMs := time.Since(mergeStart).Milliseconds()

	// Over-filtering is this feature's own failure mode, and it lies. "unwatched
	// videos about ontology" on a library holding three watched ones would
	// otherwise land on the empty answer below and report that nothing covers the
	// subject — when the truth is that everything covering it has been watched.
	//
	// So a filter that found nothing is dropped and the search re-run, once. The
	// vectors are already computed (see queryVectors), so this is SQL and nothing
	// else: no second embedding, no second model call. What it must never be is
	// silent — the sentence below is written here rather than asked for, exactly
	// like the empty answer it replaces.
	var relaxed []string
	if !filter.Empty() && len(hits) == 0 {
		wide, wdiag := s.askLanes(r, q, u.Topic, rag.Filter{}, &qv)
		wideStart := time.Now()
		wideHits := rag.FuseWeighted(wide, searchCandidates)
		// Accounted for OUTSIDE the branch below, because the second search ran
		// either way. A re-run that also found nothing is the slowest path this
		// endpoint has — a full FTS ladder and two KNN queries on top of the
		// first pass — and billing it only when it succeeded understated exactly
		// the wait most worth knowing about by roughly half.
		mergeMs += time.Since(wideStart).Milliseconds()
		wideFTS, wideRetrieval := wdiag.ftsMs, wdiag.retrievalMs
		wideQueried, wideSem := wdiag.ftsQueried, semanticRan(wdiag)
		if len(wideHits) > 0 {
			relaxed = applied
			// The wide pass's diag REPLACES the narrow one, which would drop the
			// timings of a search that genuinely happened. Carry them forward so
			// the trace reports the whole wait rather than the second half.
			//
			// embedMs is the exception and is deliberately not carried: the wide
			// pass reuses the memoized vectors (see queryVectors) and records 0ms
			// because that is what it cost. Adding the two would bill the reader
			// twice for one embedding.
			narrowFTS, narrowRetrieval := diag.ftsMs, diag.retrievalMs
			narrowEmbed := diag.embedMs
			narrowQueried, narrowSem := diag.ftsQueried, semanticRan(diag)
			// Both ladders' rungs, in the order they ran, separated by the
			// marker. ftsMs below is the sum of TWO ladders, so carrying only
			// the wide one's rungs would print a total the rungs cannot
			// account for — which is the exact confusion per-rung timing
			// exists to remove.
			narrowRungs := append(diag.rungs, secondLadderMarker)
			lanes, diag, hits = wide, wdiag, wideHits
			diag.rungs = append(narrowRungs, wdiag.rungs...)
			diag.ftsMs = wideFTS + narrowFTS
			diag.retrievalMs = wideRetrieval + narrowRetrieval
			diag.embedMs = narrowEmbed
			diag.ftsQueried = wideQueried || narrowQueried
			diag.understand, diag.understandMs = string(ud.status), ud.ms
			diag.counting = ud.counting
			diag.filters, diag.filtersDropped = strings.Join(applied, "|"), ud.dropped
			diag.unresolved, diag.relaxed = ch.Unresolved, true
			// semanticRan reads the two lane diags, which came across with wdiag.
			// If only the narrow pass reached the vector store, say so.
			if narrowSem && !wideSem {
				diag.semRaw.ran = true
			}
		} else {
			// Nothing swapped in, so the wide pass's cost has to be added to the
			// diag that stays — its rungs with it, for the same reason.
			diag.rungs = append(append(diag.rungs, secondLadderMarker), wdiag.rungs...)
			diag.ftsMs += wideFTS
			diag.retrievalMs += wideRetrieval
			diag.ftsQueried = diag.ftsQueried || wideQueried
			if wideSem {
				diag.semRaw.ran = true
			}
		}
	}

	// The two searches and the merge, in the order they happened.
	//
	// The vector span is DERIVED, not measured: retrievalMs runs from the same
	// start as ftsMs (see askLanes), so it is a total that already contains the
	// keyword ladder and the embedding. Reporting all three as siblings would
	// draw bars summing to nearly twice the wall clock.
	//
	// Each gated on whether it HAPPENED, never on its duration — a step that
	// never ran and one that ran in under a millisecond both report 0ms, so a
	// duration cannot tell them apart. Three real configurations reach here with
	// one of these missing: a query with no usable terms builds no FTS ladder at
	// all, a deployment with no embedder skips the semantic block wholesale, and
	// an embedding that fails leaves both vector lanes unrun while retrieval
	// carries on FTS-only. Each of those used to draw a row for work nobody did,
	// count it toward "N queries of your library", and add its bar to the total.
	if diag.ftsQueried {
		tr.add("keyword", "sqlite FTS5", traceKindLocal, diag.ftsMs)
	}
	if diag.embedQueried && s.embedder != nil {
		tr.add("embed", s.embedder.Model(), traceKindModel, diag.embedMs)
	}
	if semanticRan(diag) {
		tr.add("vector", "sqlite-vec", traceKindLocal, diag.retrievalMs-diag.ftsMs-diag.embedMs)
	}

	// A comparison is two channels the library actually HAS, named deliberately.
	// Counting the model's names instead would switch to summary-first selection
	// for "Veritasium and Numberphile" on a library holding only the first —
	// which is not a comparison at all — and channelResolution.Ambiguous keeps
	// one uncertain name that matched several channels out of it.
	selectStart := time.Now()
	// One batch read for every video the hits touch serves both the excerpt
	// choice and the coverage list below.
	lookup := newVideoLookup(s.videos, hits)
	sources, vids, excerpts, chosen := s.buildAnswerContext(lookup, hits, len(ch.Matched) > 1 && !ch.Ambiguous)
	// One row for fusing and choosing together, because they are one idea to a
	// reader: the two searches came back and this is what was kept out of them.
	// Split apart they would be two adjacent rows with no tool between them,
	// saying nothing the merged row does not.
	tr.add("merge", "", traceKindCode, mergeMs+time.Since(selectStart).Milliseconds())
	// Logged HERE rather than inside askLanes, because only now is it known which
	// passages the model was actually given — the number that says whether a lane
	// changed what was read, not merely what was found.
	diag.attribute(lanes, chosen)
	diag.log(q, hits)

	// An inventory question is asking HOW MUCH, and twelve excerpts cannot answer
	// that however well they are written. Count it in SQL instead, under the
	// ORIGINAL filter — a relaxed search still has to report zero unwatched
	// videos, with the disclosure below reconciling that against the watched ones
	// it is showing.
	//
	// Only for a question that NARROWED something, though. The count is over the
	// filter and knows nothing about the topic, so on "how many videos about
	// ontology do we have" an unfiltered count is the size of the whole library —
	// a number with no relation to the question, handed to the model as
	// authoritative and printed above the answer. A count is meaningful exactly
	// when there is a scope row beside it saying what it counts.
	countStart := time.Now()
	counts, counted := s.inventoryCount(r.Context(), u.Counting, u.Topic, filter)
	// On whether the query RAN, not on whether it produced a number: a count that
	// reached sqlite and errored also returns nil, and it cost the same wait as
	// one that worked.
	if counted {
		tr.add("count", "sqlite", traceKindLocal, time.Since(countStart).Milliseconds())
	}

	// The "Also in your library" tier. Its admission rule is stricter than
	// retrieval's on purpose — see rag.CoverageMaxDistance — because a passage
	// worth reasoning over and a video worth recommending are different claims.
	relevant, barredSet := relevantVideos(lanes, diag.topicLane, s.searchMaxDistance)
	// EVERY VIDEO THE MODEL WAS SHOWN STAYS LISTABLE, whatever its distance.
	//
	// The excerpt set is chosen from `hits`, which is bounded by
	// DefaultMaxDistance (1.25) rather than by the coverage bar (1.10) — so a
	// video sitting between the two can win an excerpt slot and then be barred
	// from this list. If the model goes on not to cite it, the client's
	// subtraction leaves it in neither tier and it vanishes from the page
	// entirely. That is the exact failure coverageVideos' own comment says the
	// design avoids by refusing to subtract the excerpt set server-side; a
	// distance bar that dropped them would reintroduce it by another route.
	for _, h := range chosen {
		relevant[h.VideoID] = true
	}
	coverage := s.coverageVideos(lookup, hits, relevant)
	// Netted off AFTER the excerpt videos are added back, so this counts what the
	// bar actually removed rather than what it merely objected to.
	barred := 0
	for id := range barredSet {
		if !relevant[id] {
			barred++
		}
	}
	// considered is every distinct video retrieval offered, counted off the fused
	// list itself rather than inside coverageVideos' loop — that loop stops at
	// coverageMaxVideos, so counting there reported "20/20" for every full list
	// and hid the denominator exactly when it mattered most.
	cov = coverageDiag{
		ran: true, shown: len(coverage),
		considered: distinctVideos(hits), barred: barred,
	}
	payload := map[string]any{
		"sources": sources, "videos": vids,
		"coverage": coverage,
		"filters":  applied, "relaxed": relaxed, "unresolved_channels": ch.Unresolved,
	}
	if counts != nil {
		payload["counts"] = counts
	}
	if !send("sources", payload) {
		return // client gone
	}

	// Everything the reader must be told regardless of what the model writes, in
	// the order it happened: a channel that is not here, then a filter that had
	// to be dropped to find anything.
	if note := deterministicNote(ch.Unresolved, relaxed, len(sources)); note != "" {
		if !send("token", map[string]string{"text": note}) {
			return
		}
	}

	// Nothing retrieved. The honest answer is written here rather than asked
	// for, so the most important case — the library genuinely not covering
	// something — cannot depend on the model choosing to admit it. It also costs
	// nothing, which matters when Ask is the mode the page lands on.
	if len(sources) == 0 {
		send("token", map[string]string{"text": emptyAnswer(applied, relaxed, counts)})
		return
	}

	// Chat unavailable: the citations above already went out, so Ask degrades to
	// what it was before this endpoint existed rather than showing an error.
	if s.ask == nil {
		send("error", map[string]string{"error": "answer unavailable"})
		return
	}

	// The one call in peeq a person waits on: it writes cited prose from a dozen
	// excerpts while a spinner is on screen.
	//
	// Balanced reasoning, not the model's default: the only place peeq asks for
	// less than the default on prose, and it is a latency decision. The stream
	// cannot start until reasoning ends, so depth is paid entirely in
	// time-to-first-token while a reader watches a blank panel — and this is
	// grounded extraction from excerpts retrieval already chose; the hard part
	// happened in embedding and ranking. Balanced keeps the reasoning that makes
	// citation placement stable.
	//
	// Do NOT drop this to minimal: measured against a shallower setting, the
	// answers stayed grounded and still refused a question the excerpts could
	// not answer, but ran thinner and drifted on citation placement, landing the
	// marker before the full stop rather than after it.
	ctx := llm.WithReasoning(askCtx, llm.ReasoningBalanced)
	ctx = llm.WithMaxAnswerTokens(ctx, answerMaxAnswerTokens)
	ctx = llm.WithCall(ctx, llm.CallInfo{Step: "answer"})
	// THE RAW QUESTION, and never the extracted topic. The two exist for
	// different consumers and must not be confused: the topic is a retrieval
	// input, deliberately stripped down to what would appear in a video about the
	// subject, and it throws away everything that tells the model what kind of
	// answer is wanted — "what material do we have on X" and "how does X work"
	// reduce to the same topic and want different answers. The model gets the
	// sentence the reader actually wrote; only the embedder sees the reduction.
	//
	// Timed here and nowhere else. diag.log fires above, before this call, so
	// until now the longest step of the whole pipeline — around five seconds, more
	// than everything else put together — was the one step with no measurement
	// anywhere. Time to the FIRST token is tracked separately because that is the
	// part a reader experiences as waiting; the rest of the stream arrives while
	// they are already reading. Both reach the log through answerTrace.log.
	answerStart := time.Now()
	answer, err := s.ask.CompleteStream(ctx, answerMessages(q, excerpts, applied, relaxed, counts), func(delta string) {
		if ttft == 0 {
			ttft = time.Since(answerStart)
		}
		// A send error means the browser disconnected. Nothing to do about it
		// here; the request context is already cancelled, which unwinds the
		// upstream call.
		send("token", map[string]string{"text": delta})
	})
	// The row stays even when the call failed, because the time is real and a
	// failure that took ninety seconds is exactly the one worth seeing. But it
	// must not say the model WROTE the answer while the panel above it says the
	// answer is unavailable — so the failure gets its own key and its own words.
	// Keys are how the panel names a step (see STAGE_LABELS), which is what makes
	// this a rename rather than a new field on the wire.
	answerKey := "answer"
	if err != nil || strings.TrimSpace(answer) == "" {
		answerKey = "answer_failed"
	}
	tr.add(answerKey, s.ask.ModelFor(ctx), traceKindModel, time.Since(answerStart).Milliseconds())
	switch {
	case err != nil:
		logAnswerFailed(ctx, err, time.Since(answerStart))
		send("error", map[string]string{"error": "answer unavailable"})
	case strings.TrimSpace(answer) == "":
		// A clean stream that carried no content is still a failure, and the
		// only one that arrives without an error: the endpoint can end with
		// finish_reason "length" after spending the whole token budget on
		// reasoning. Without this the panel renders a header, a source list and
		// nothing between them, which reads as a broken page rather than as a
		// call that did not produce an answer.
		slog.Warn("answer: chat returned no content")
		send("error", map[string]string{"error": "answer unavailable"})
	}
}
