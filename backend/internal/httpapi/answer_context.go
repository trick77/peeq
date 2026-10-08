package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/videos"
)

// buildAnswerContext turns ranked hits into the numbered citation table the UI
// renders, the videos those citations belong to, and the excerpt block the
// model reads. Sources and excerpts are built together so a citation number
// always means the same passage in both.
//
// The video list is separate rather than embedded in each source because a
// video contributes up to answerMaxSourcesPerVideo passages, and repeating its
// record three times would put the same title, channel and duration on the wire
// three times.
// The chosen hits are returned alongside, for the retrieval log's per-lane
// attribution. They carry the chunk ordinal, which an answerSource does not —
// and the ordinal is what makes a passage identifiable. Video plus start second
// is NOT unique: a chapter chunk contains the transcript of its own span, so the
// same moment is indexed twice under two kinds (the reason minMomentGapSeconds
// exists). Keying attribution on the pair would credit a lane for a passage it
// never found.
//
// compare switches excerpt selection to summary-first. A question naming two
// channels is asking how they differ, and twelve interleaved transcript
// fragments are the worst possible evidence for that — each one a sentence out
// of the middle of an argument. Whole summaries, one per video across more
// videos, are what a comparison is actually made of.
func (s *server) buildAnswerContext(lookup *videoLookup, hits []rag.Hit, compare bool) ([]answerSource, []answerVideo, []string, []rag.Hit) {
	sources := make([]answerSource, 0, answerMaxSources)
	vids := make([]answerVideo, 0, answerMaxSources)
	excerpts := make([]string, 0, answerMaxSources)
	chosen := make([]rag.Hit, 0, answerMaxSources)
	seenVideo := make(map[string]bool)

	for _, c := range s.chooseExcerpts(lookup, hits, compare) {
		chosen = append(chosen, c.hit)
		if !seenVideo[c.hit.VideoID] {
			seenVideo[c.hit.VideoID] = true
			vids = append(vids, toAnswerVideo(c.video))
		}

		n := len(sources) + 1
		sources = append(sources, answerSource{
			N: n, VideoID: c.hit.VideoID, Title: c.video.Title,
			ChannelName:  c.video.ChannelName,
			StartSeconds: c.hit.StartSeconds, Kind: c.hit.Kind,
			Snippet: matchSnippet(c.hit),
		})
		// The chapter this moment falls in, when the video has chapters. It is
		// what lets an answer say WHERE in a two-hour lecture something is
		// covered, instead of leaving the model to infer a location from a
		// transcript fragment that mentions none.
		excerpts = append(excerpts, formatExcerpt(n, c.video.Title, chapterAt(c.video, c.hit.StartSeconds), c.hit))
	}
	return sources, vids, excerpts, chosen
}

// formatExcerpt fences one passage for the answer prompt.
//
// kind="analysis" marks a summary or in-depth chunk: peeq's own condensed
// reading of the video, not words spoken in it. The system prompt tells the
// model to cite it but never to quote it as the speaker.
//
// Sanitize BEFORE truncating, never after: stripping a sentinel out of
// already-shortened text can leave a dangling "</excerp" that whatever is
// written next completes.
func formatExcerpt(n int, title, chapter string, h rag.Hit) string {
	attrs := fmt.Sprintf(" title=%q", stripExcerptTags(title))
	if chapter != "" {
		attrs += fmt.Sprintf(" chapter=%q", stripExcerptTags(chapter))
	}
	if rag.IsAnalysis(h.Kind) {
		attrs += ` kind="analysis"`
	}
	return fmt.Sprintf("<excerpt n=\"%d\"%s at=\"%ds\">\n%s\n</excerpt>",
		n, attrs, h.StartSeconds, truncateRunes(stripExcerptTags(h.Text), answerExcerptRunes))
}

// coverageMaxVideos caps the retrieved-video list the panel shows under its
// citations. Twenty mirrors defaultSearchK, so "what else do I have on this"
// answers with the same breadth the search box would.
const coverageMaxVideos = 20

// coverageVideos is every video retrieval found, best-ranked first, one entry
// each — the answer to "what else is in here", which the citation list cannot
// give because the model only cites what it used.
//
// Three dedups, and all three are needed. The fused list is 200 CHUNKS and one
// video routinely owns many of them (58 chunks across 6 videos for one word on
// the library this was built against), so it collapses by video; the cap counts
// videos rather than chunks, and is applied AFTER collapsing, since truncating
// chunks first would yield far fewer than twenty videos; and ordering follows the
// fused rank of each video's best chunk, so the list reads strongest-first.
//
// It deliberately includes the videos that won excerpt slots. The frame goes out
// BEFORE generation, so the server cannot know what the model will cite —
// subtracting the excerpt set here would strand a video that was sent to the
// model and then not cited in neither list. The client owns that subtraction,
// because only the client has the finished answer.
// relevantVideos is the set of videos some lane ABOVE the recall floor found.
//
// The floor rung matches any one content word, which is a net for fusion and not
// a claim about a video. Treated as one, it fills a list of "also in your library"
// with whatever shares a word: a question about bike geometry listed eighteen
// videos, one of them about skateboards, because the transcript says "bike".
//
// Every other lane is a real signal — the semantic lane placed the chunk near the
// question, and the strict, content and prefix rungs mean every content word is
// present. WeightKeywordAny is the lowest weight of the five, so "above the floor"
// is the test, and it stays correct if a rung is added.
//
// THE TOPIC LANE IS EXCLUDED, at excludeLane, and its weight is not why. The
// safety argument for the rewritten query is that a bad rewrite is outvoted by
// the four lanes that did not change — and that argument holds at FUSION, which
// is a vote, but NOT here, which is a UNION. One lane is enough to put a video in
// this list, so a topic mis-extracted as "material science" would seat
// material-science videos in it with nothing to outrank them. That is exactly the
// failure #350 tightened this list against.
//
// It costs the feature's best case: a good extraction reaching videos the raw
// question never did. Taken deliberately while the rewrite is unmeasured — the
// answer still gets the topic lane's evidence, which is where the value is, and
// this is one line to give back once the logs say the rewrite can be trusted.
//
// excludeLane is -1 when there is no topic lane.
//
// The second return is how many videos the distance bar turned away, for the
// log. It is the number that says whether rag.CoverageMaxDistance is set
// anywhere near right, and there is nowhere else to get it from.
func relevantVideos(lanes []rag.Lane, excludeLane int, searchMaxDistance float64) (map[string]bool, map[string]bool) {
	out := make(map[string]bool)
	barred := make(map[string]bool)
	// A negative searchMaxDistance is the documented opt-out from bounding the
	// vector lane, and semanticLane honours it by skipping BOTH of its bounds —
	// "an operator who asks for unbounded KNN gets unbounded KNN, not a floor
	// they cannot see in any setting". A coverage bar that kept cutting at 1.10
	// under that setting would be exactly the invisible floor that promises not
	// to exist, so it opts out too.
	bar := rag.CoverageMaxDistance
	if searchMaxDistance < 0 {
		bar = 0
	}
	for i, lane := range lanes {
		if i == excludeLane || lane.Weight <= rag.WeightKeywordAny {
			continue
		}
		for _, h := range lane.Hits {
			// THE BAR APPLIES TO DISTANCE, WHICH ONLY THE VECTOR LANES CARRY.
			// A keyword hit leaves Distance at zero and sails through, which is
			// the intent rather than an accident of the zero value: a strict,
			// content or prefix rung means every content word the reader typed
			// is in that chunk, and that is a claim about the video no measure
			// of vector proximity should be able to overturn.
			if bar > 0 && h.Distance > bar {
				barred[h.VideoID] = true
				continue
			}
			out[h.VideoID] = true
		}
	}
	// The barred SET rather than a count, because the caller adds the excerpt
	// videos to `out` afterwards and only then is it known which of these were
	// genuinely turned away. A video barred by one lane and vouched for by
	// another was never rejected at all.
	return out, barred
}

func (s *server) coverageVideos(lookup *videoLookup, hits []rag.Hit, relevant map[string]bool) []answerVideo {
	seen := make(map[string]bool)
	out := make([]answerVideo, 0, coverageMaxVideos)
	for _, h := range hits {
		if len(out) >= coverageMaxVideos {
			break
		}
		if seen[h.VideoID] {
			continue
		}
		seen[h.VideoID] = true
		// Ranked by the fused list, but admitted only on a signal stronger than
		// sharing one word. Ordering still comes from the fusion, so the list reads
		// strongest-first among the videos that qualify.
		if !relevant[h.VideoID] {
			continue
		}
		v := lookup.get(h.VideoID)
		if v == nil {
			continue
		}
		out = append(out, toAnswerVideo(v))
	}
	return out
}

// excerptCandidate is a hit that could be an excerpt, paired with the video
// record the citation table needs. Resolving the video once here is also what
// keeps the two selection passes below from hitting the store twice per hit.
type excerptCandidate struct {
	hit   rag.Hit
	video *videos.Video
}

// videoLookup resolves a video id at most once per request.
//
// It exists because of what breadth-first selection costs. The old loop stopped
// at answerMaxSources, so it resolved a dozen videos; this one has to consider
// every fused hit — up to searchCandidates of them — before it knows which
// twelve to keep. Resolving per hit would put 200 joined queries in front of the
// sources frame, which is the first thing the reader waits for. Hits cluster
// heavily by video (200 chunks came from ~32 videos on the library this was
// measured against), so caching turns that back into a few dozen.
//
// A video that is GONE is cached as gone: that answer cannot change within a
// request, and re-asking once per chunk is the cost this type exists to avoid.
//
// An ERROR is not cached. A busy database is a transient answer, and caching it
// would drop every remaining chunk of that video from the evidence set on the
// strength of one failed query. Retrying costs at most one query per chunk of one
// video — what the code did before this cache existed.
type videoLookup struct {
	store *videos.Store
	seen  map[string]*videos.Video
}

// newVideoLookup reads the videos behind hits in one batch up front; get
// then answers from memory and only reaches the store for an id the batch
// did not cover (or could not read — errors are not cached, so a retry gets
// another chance).
func newVideoLookup(store *videos.Store, hits []rag.Hit) *videoLookup {
	l := &videoLookup{store: store, seen: make(map[string]*videos.Video, len(hits))}
	if store == nil {
		return l
	}
	ids := idsOf(hits, func(h rag.Hit) string { return h.VideoID })
	if index, err := store.GetMany(ids); err != nil {
		slog.Warn("answer: video preload failed", "count", len(ids), "err", err)
	} else {
		for id, v := range index {
			l.seen[id] = v
		}
	}
	return l
}

func (l *videoLookup) get(id string) *videos.Video {
	if v, ok := l.seen[id]; ok {
		return v
	}
	if l.store == nil {
		return nil
	}
	v, err := l.store.Get(id)
	if err != nil {
		slog.Warn("answer: video lookup failed", "err", err, "video_id", id)
		return nil
	}
	l.seen[id] = v
	return v
}

// chooseExcerpts picks the passages the model reads, in fused-rank order.
//
// It runs TWO passes over the candidates, and that is the whole point:
//
//	pass 1 — at most one passage per video
//	pass 2 — fill what is left, up to answerMaxSourcesPerVideo per video
//
// Taking the top answerMaxSources by score alone concentrates the evidence on
// whichever videos happen to rank highest, and measurably so: on the library this
// was tuned against, a question about "transients" had its keyword lane's top
// twelve chunks spread across just FOUR videos, and with three passages allowed
// per video those four were the entire evidence set. The plain keyword search the
// same reader ran found six videos, so Ask looked like it knew less than the
// search box did — which is exactly the complaint that produced this function.
//
// A breadth-first pass fixes that without widening retrieval or spending more
// context: the same twelve slots now reach up to answerBreadthSources distinct
// videos, and pass 2 hands the rest back as depth. A narrow question still gets
// three passages from one video.
//
// Pass 1 is capped rather than unlimited because unlimited breadth is the same
// bug mirrored. Let it claim all twelve and a library with twelve or more
// matching videos gives each exactly one passage — so the lecture that actually
// answers the question is quoted no more deeply than the twelfth-best video that
// merely mentions it, and the prefix floor makes reaching twelve marginal videos
// easier than it used to be. Eight for breadth, four for depth: more videos than
// the keyword search shows, and the top of the ranking still gets quoted properly.
//
// It also fixes something the lane weights could not. WeightKeywordAny (0.4) sits
// below WeightSemantic (0.6), so on a question that falls through to the OR floor
// the fused top twelve can be entirely semantic — the keyword lane's best row
// scores 0.4/61, which loses to a semantic row all the way down to rank 31. Pass 1
// walks the whole fused list rather than its head, so a keyword-lane video ranked
// below the semantic block still reaches the model. Doing it here rather than by
// re-tuning the weights leaves the ranking contract in rag/relevance_test.go
// intact: this changes which passages are SELECTED, not how any of them rank.
//
// compare adds a pass in front of the other two: one summary chunk per video,
// across as many videos as the breadth budget allows. A question comparing two
// channels wants each video's whole argument, not a sentence from the middle of
// it. The two normal passes still run afterwards over whatever slots are left,
// so a video with no summary indexed is not silently excluded from a comparison
// — it just contributes transcript, as it always did.
func (s *server) chooseExcerpts(lookup *videoLookup, hits []rag.Hit, compare bool) []excerptCandidate {
	// A chapter chunk repeats the transcript of its own span, so the same words
	// can arrive twice under two kinds. Spending two of twelve slots on one
	// passage would crowd out a genuinely different one.
	seen := make(map[string]bool)
	cands := make([]excerptCandidate, 0, len(hits))
	for _, h := range hits {
		// A summary chunk describes the whole video and is stored at second 0
		// (rag.buildRows), so it is exempt from the moment bucket in BOTH
		// directions: it is never suppressed by an earlier hit, and it must
		// never claim bucket 0 either — doing so would drop the genuine
		// transcript hit in the video's first thirty seconds. An in-depth
		// section is exempt the same way: it is the analysis of a point, not a
		// repeat of the transcript at its stamp.
		exempt := rag.IsAnalysis(h.Kind)
		key := fmt.Sprintf("%s:%d", h.VideoID, h.StartSeconds/answerMomentBucket)
		if !exempt && seen[key] {
			continue
		}
		v := lookup.get(h.VideoID)
		if v == nil {
			continue
		}
		if !exempt {
			seen[key] = true
		}
		cands = append(cands, excerptCandidate{hit: h, video: v})
	}

	perVideo := make(map[string]int)
	taken := make([]bool, len(cands))
	picked := make([]int, 0, answerMaxSources)
	// Each pass has its own per-video limit AND its own ceiling on the slots it
	// may fill. Sharing one cap check keeps pass 2 inside answerMaxSourcesPerVideo
	// and stops pass 1 from ever being the reason that cap is reached.
	passes := []struct {
		perVideoLimit, slots int
		summaryOnly          bool
	}{
		{perVideoLimit: 1, slots: answerBreadthSources},
		{perVideoLimit: answerMaxSourcesPerVideo, slots: answerMaxSources},
	}
	if compare {
		passes = append([]struct {
			perVideoLimit, slots int
			summaryOnly          bool
		}{{perVideoLimit: 1, slots: answerBreadthSources, summaryOnly: true}}, passes...)
	}
	for _, pass := range passes {
		for i, c := range cands {
			if len(picked) >= pass.slots {
				break
			}
			if taken[i] || perVideo[c.hit.VideoID] >= pass.perVideoLimit {
				continue
			}
			if pass.summaryOnly && c.hit.Kind != rag.KindSummary {
				continue
			}
			taken[i] = true
			perVideo[c.hit.VideoID]++
			picked = append(picked, i)
		}
	}

	// Back into fused-rank order, so citation [1] is still the best passage
	// retrieval found rather than whichever video pass 1 happened to reach first.
	sort.Ints(picked)
	out := make([]excerptCandidate, 0, len(picked))
	for _, i := range picked {
		out = append(out, cands[i])
	}
	return out
}

// Relaxation fires on an EMPTY filtered result and on nothing else.
//
// An earlier version relaxed below two distinct videos, on the reasoning that
// one video is usually a filter cutting too deep. It is not: "does Veritasium
// cover ontology" answered from the one Veritasium video that covers it is
// exactly right, and widening it to the whole library replaces a correct
// narrow answer with a vaguer broad one. The only unambiguous signal is nothing
// at all — where the alternative is telling the reader their library covers
// nothing, which may be false.

// inventoryCount answers "how many" in SQL, for a question that asked it.
//
// Returns nil for a content question, for an unavailable store, or on error:
// every one of those means the answer is written from the excerpts alone, which
// is what it did before counting existed. A count that cannot be trusted is
// worse than no count, because the prompt tells the model to believe it.
// The second return says whether the COUNT QUERY RAN, which nil cannot: this
// returns nil both for a question that was never counting and for a count that
// reached sqlite and errored. The answer trace has to tell those apart — the
// second one cost the reader a query and belongs in the list.
func (s *server) inventoryCount(ctx context.Context, counting bool, topic string, f rag.Filter) (*rag.LibraryCount, bool) {
	if !counting || s.rag == nil {
		return nil, false
	}
	// THE COUNT CANNOT SEE THE TOPIC. It is SQL over the videos table, and no
	// column holds "is about ontology" — only retrieval knows that, and only
	// approximately, bounded by the candidate cap and the distance floor.
	//
	// So a question carrying a topic gets no count. "How many videos about
	// ontology do I have" would otherwise be answered with the size of the whole
	// unwatched shelf, printed above the answer and handed to the model under a
	// rule saying it is authoritative and must not be contradicted. A confidently
	// wrong number is far worse than none.
	//
	// What is left is the question this was built for and the one that is
	// genuinely answerable: "how many unwatched Veritasium videos do I have" —
	// structural, no subject, exact.
	//
	// The filter check earns its place separately: with no constraint either,
	// the count is just "how big is my library", and there would be no scope row
	// above it saying what it counted.
	if topic != "" || f.Empty() {
		return nil, false
	}
	c, err := s.rag.CountVideos(ctx, f)
	if err != nil {
		slog.Warn("answer: inventory count failed", "err", err)
		return nil, true
	}
	return &c, true
}

// chapterAt names the chapter a moment falls in, or "" when the video has no
// chapters or the moment sits before the first one. Chapters are stored as the
// JSON array the summarize step produced; a malformed one is simply no chapter,
// never an error on the answer path.
//
// The timestamp key is "ts", which is what summarize.Chapter marshals and what
// every other reader of videos.chapters expects. Decoding it as "start_seconds"
// would leave every chapter at 0 and silently label every excerpt with the LAST
// chapter of its video.
func chapterAt(v *videos.Video, startSeconds int) string {
	if v == nil || strings.TrimSpace(v.Chapters) == "" {
		return ""
	}
	var chapters []struct {
		Title string `json:"title"`
		TS    int    `json:"ts"`
	}
	if err := json.Unmarshal([]byte(v.Chapters), &chapters); err != nil {
		return ""
	}
	title := ""
	best := -1
	for _, c := range chapters {
		// The LAST chapter starting at or before the moment, not the first —
		// chapters are usually ordered but nothing guarantees it, and an
		// unordered list would otherwise label every moment with chapter one.
		if c.TS <= startSeconds && c.TS >= best {
			best, title = c.TS, strings.TrimSpace(c.Title)
		}
	}
	return title
}
