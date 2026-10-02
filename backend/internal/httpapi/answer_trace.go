package httpapi

import (
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// traceStage is one step of the pipeline, as the answer panel reports it.
//
// KEY, NOT PROSE. The label a reader sees ("Turned the question into numbers")
// lives in the frontend, because it is copy: it gets reworded, and rewording it
// must not be a backend deploy. What the backend owns is which step ran, what
// ran it, and what it cost — the three things only the backend can know.
//
// Tool is empty for a step that called nothing. That is not the same as unknown,
// and the panel renders it as an absence rather than as a word: a row saying "no
// tool" would spend a line on something that did not happen.
type traceStage struct {
	Key string `json:"key"`
	Ms  int64  `json:"ms"`
	// Tool is the model or storage engine that ran this step — a model id,
	// "sqlite-vec". Read from the thing that actually ran (see
	// llm.Client.ModelFor and SearchEmbedder.Model) rather than written out
	// here, so a model swap cannot leave the panel naming a model nobody is
	// using.
	Tool string `json:"tool,omitempty"`
	// Kind is how to read Tool: "model" for a call that left this machine,
	// "local" for a query against the library, "code" for neither.
	Kind string `json:"kind"`
}

const (
	traceKindModel = "model"
	traceKindLocal = "local"
	traceKindCode  = "code"
)

// semanticRan reports whether either vector lane actually reached the store.
//
// `failed` counts as ran: a KNN query that errored still went to sqlite-vec and
// still cost the wait. What this excludes is the case where no query was ever
// issued — no embedder wired, or an embedding that failed before the lanes
// could run — which is the difference between "sqlite-vec found nothing" and
// "sqlite-vec was never asked".
func semanticRan(d askDiag) bool {
	return d.semRaw.ran || d.semRaw.failed || d.semTopic.ran || d.semTopic.failed
}

// answerTrace accumulates the stages of one answer, in the order they ran.
//
// It is filled as the handler goes rather than assembled at the end, because
// half of these steps are conditional and the only place that knows a step
// happened is the branch that ran it.
type answerTrace struct {
	stages []traceStage
}

// add records a step that RAN. A step that was skipped never calls this, which
// is the whole mechanism behind "the panel lists what happened": there is no
// flag to forget to set, because a stage exists only if some branch appended it.
//
// A sub-millisecond step still gets a row. Rounding it away would drop the
// channel lookup and the merge from most traces, and "the search matched a
// channel" is worth saying even when it took no measurable time.
func (t *answerTrace) add(key, tool, kind string, ms int64) {
	if ms < 0 {
		// Derived spans can go negative when two clocks disagree by a tick (see
		// the vector stage, which is a subtraction). Report zero rather than a
		// bar that draws backwards.
		ms = 0
	}
	t.stages = append(t.stages, traceStage{Key: key, Ms: ms, Tool: tool, Kind: kind})
}

// log writes the same trace the panel gets, as one line.
//
// INFO, not Debug, for the reason askDiag.log gives above itself: an Ask is
// user-initiated and rare, one line each is not noise, and a diagnostic nobody
// can see without redeploying at a different level is a diagnostic nobody uses.
// "It is for debugging" is an argument for Info here, not against it — Debug is
// about volume, and this has none.
//
// It exists because until now the trace went to the BROWSER and nowhere else.
// Half these steps — the channel lookup, the merge, the count — had no
// server-side record at all, so a reader reporting what the panel showed them
// could not be matched against anything. Logged from the same defer that sends
// the frame, so the two cannot disagree, and so it still lands when the client
// has already disconnected.
//
// Generation is in here rather than on a line of its own. It is the step that
// had no measurement anywhere before this, and splitting it out would leave two
// uncorrelated lines per Ask to read together — there is no request id to join
// them by.
// coverageDiag is what the "Also in your library" tier did with what retrieval
// offered it. Three numbers rather than one, because "8 videos shown" cannot
// say whether the other fifteen were junk correctly dropped or good material
// wrongly barred — and rag.CoverageMaxDistance can only be tuned by someone who
// can see the difference.
type coverageDiag struct {
	// ran separates "the coverage tier was never reached" from "it was reached
	// and admitted nothing". A client that hangs up mid-retrieval returns before
	// the tier is built, and the zero value would log 0/0 — indistinguishable
	// from a search that genuinely found nothing, which is a real and different
	// outcome. Same "-" convention semLaneDiag uses for a lane that never ran.
	ran        bool
	shown      int
	considered int
	barred     int
}

func (c coverageDiag) String() string {
	if !c.ran {
		return "-"
	}
	return fmt.Sprintf("%d/%d barred=%d", c.shown, c.considered, c.barred)
}

func (t answerTrace) log(q string, ttft time.Duration, cov coverageDiag) {
	if len(t.stages) == 0 {
		return
	}
	// Packed into one field rather than spread across many, matching the
	// "embed=%d fts=%d retrieval=%d" shape askDiag.log already uses: the stage
	// list is variable-length, and a line whose KEYS change per request is one
	// no log query can group.
	parts := make([]string, 0, len(t.stages))
	models := make([]string, 0, 3)
	var total int64
	for _, s := range t.stages {
		parts = append(parts, fmt.Sprintf("%s=%d", s.Key, s.Ms))
		total += s.Ms
		// Only the model calls. The storage engines are compiled in and cannot
		// surprise anyone; a model id is configuration and is the thing worth
		// having in the record when an answer has to be explained later.
		if s.Kind == traceKindModel {
			models = append(models, s.Tool)
		}
	}
	slog.Info("ask trace",
		"q", q,
		"stages", strings.Join(parts, " "),
		"total_ms", total,
		// The part of the wait a reader actually experiences: after the first
		// token they are reading, not waiting. 0 when no token ever arrived,
		// which is itself the finding.
		"ttft_ms", ttft.Milliseconds(),
		"models", orDash(strings.Join(models, "|")),
		// shown/considered, then how many of the difference were turned away by
		// the distance bar specifically. A barred count that climbs with
		// complaints about junk means the bar is too loose; one that climbs with
		// complaints about a thin list means it is too tight.
		"coverage", cov.String(),
	)
}
