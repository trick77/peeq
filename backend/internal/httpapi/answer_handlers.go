package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/sse"
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
}

// answerSource is one cited passage, in the shape the UI needs to render a
// citation and open the player at it.
type answerSource struct {
	N            int    `json:"n"`
	VideoID      string `json:"video_id"`
	Title        string `json:"title"`
	ChannelName  string `json:"channel_name,omitempty"`
	StartSeconds int    `json:"start_seconds"`
	Kind         string `json:"kind"`
}

const (
	// answerMaxSources is how many passages the model is given. Enough for a
	// question spanning several videos, small enough that the citation list
	// stays readable and the prompt stays cheap.
	answerMaxSources = 12
	// answerMaxSourcesPerVideo stops one thorough video from being the entire
	// evidence set for a question the library answers from several angles.
	answerMaxSourcesPerVideo = 3
	// answerExcerptRunes truncates a single passage. A chunk is ~600 tokens;
	// the model needs the gist, not every word, and 12 untruncated chunks would
	// dominate the request.
	answerExcerptRunes = 1200
	// answerMaxTokens bounds what the answer can cost. The prompt asks for at
	// most a few sentences, so this is a ceiling rather than a target.
	answerMaxTokens = 700
)

// handleAnswer answers GET /api/search/answer?q=: it runs the same retrieval
// Ask mode uses, then streams a grounded answer over SSE.
//
// Frames are always sources, then zero or more token frames, then done — with
// an error frame in place of the tokens when there is no answer to give. The
// citation table goes first because retrieval finishes before generation
// starts, so it is already known, and a failure mid-answer still leaves the
// reader a usable list of moments.
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
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
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

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		send("sources", map[string]any{"sources": []any{}})
		return
	}

	sources, excerpts := s.buildAnswerContext(s.retrieveAsk(r, q))
	if !send("sources", map[string]any{"sources": sources}) {
		return // client gone
	}

	// Nothing retrieved. The honest answer is written here rather than asked
	// for, so the most important case — the library genuinely not covering
	// something — cannot depend on the model choosing to admit it. It also costs
	// nothing, which matters when Ask is the mode the page lands on.
	if len(sources) == 0 {
		send("token", map[string]string{"text": "Nothing in your library covers that."})
		return
	}

	// Chat unavailable: the citations above already went out, so Ask degrades to
	// what it was before this endpoint existed rather than showing an error.
	if s.ask == nil {
		send("error", map[string]string{"error": "answer unavailable"})
		return
	}

	ctx := llm.WithMaxTokens(r.Context(), answerMaxTokens)
	ctx = llm.WithCall(ctx, llm.CallInfo{Step: "answer"})
	_, err = s.ask.CompleteStream(ctx, answerMessages(q, excerpts), func(delta string) {
		// A send error means the browser disconnected. Nothing to do about it
		// here; the request context is already cancelled, which unwinds the
		// upstream call.
		send("token", map[string]string{"text": delta})
	})
	if err != nil {
		slog.Warn("answer: chat failed", "err", err)
		send("error", map[string]string{"error": "answer unavailable"})
	}
}

// buildAnswerContext turns ranked hits into the numbered citation table the UI
// renders and the excerpt block the model reads. The two are built together so
// a citation number always means the same passage in both.
func (s *server) buildAnswerContext(hits []rag.Hit) ([]answerSource, []string) {
	sources := make([]answerSource, 0, answerMaxSources)
	excerpts := make([]string, 0, answerMaxSources)
	perVideo := make(map[string]int)
	// A chapter chunk repeats the transcript of its own span, so the same words
	// can arrive twice under two kinds. Spending two of twelve slots on one
	// passage would crowd out a genuinely different one.
	seen := make(map[string]bool)

	for _, h := range hits {
		if len(sources) >= answerMaxSources {
			break
		}
		if perVideo[h.VideoID] >= answerMaxSourcesPerVideo {
			continue
		}
		key := fmt.Sprintf("%s:%d", h.VideoID, h.StartSeconds/answerMomentBucket)
		if h.Kind != rag.KindSummary && seen[key] {
			continue
		}
		v, err := s.videos.Get(h.VideoID)
		if err != nil || v == nil {
			continue
		}
		seen[key] = true
		perVideo[h.VideoID]++

		n := len(sources) + 1
		sources = append(sources, answerSource{
			N: n, VideoID: h.VideoID, Title: v.Title, ChannelName: v.ChannelName,
			StartSeconds: h.StartSeconds, Kind: h.Kind,
		})
		excerpts = append(excerpts, fmt.Sprintf("[%d] %q at %ds:\n%s",
			n, v.Title, h.StartSeconds, truncateRunes(h.Text, answerExcerptRunes)))
	}
	return sources, excerpts
}

// answerMomentBucket is how coarsely two passages count as the same moment when
// choosing what to send the model — the same reasoning as minMomentGapSeconds
// on the search response, applied to the evidence set.
const answerMomentBucket = 30

func truncateRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

// answerSystemPrompt constrains the model to the excerpts it is given.
//
// The instruction to say plainly when the excerpts do not answer the question
// is a backstop, not the primary defence: a query that retrieves nothing never
// reaches the model at all (see handleAnswer). This covers the subtler case
// where passages came back but none of them actually address what was asked.
const answerSystemPrompt = `You answer questions about a personal video library, using ONLY the numbered excerpts provided.

Rules:
- Cite every claim with the excerpt number in square brackets, like [1] or [3]. Cite the excerpt the claim actually came from.
- If the excerpts do not answer the question, say so plainly in one sentence. Do not pad it out.
- Never invent a video, a title, a timestamp, or a fact that is not in the excerpts.
- If the excerpts disagree with each other, say so and cite both.
- Answer in at most six sentences. Write plainly, in the reader's own terms.
- Do not list the sources at the end; the interface renders them.`

func answerMessages(q string, excerpts []string) []llm.Message {
	var b strings.Builder
	b.WriteString("Question: ")
	b.WriteString(q)
	b.WriteString("\n\nExcerpts:\n\n")
	for _, e := range excerpts {
		b.WriteString(e)
		b.WriteString("\n\n")
	}
	return []llm.Message{
		{Role: "system", Content: answerSystemPrompt},
		{Role: "user", Content: b.String()},
	}
}
