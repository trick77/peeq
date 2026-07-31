import { useEffect, useRef, useState, type FormEvent } from "react";
import { Icon } from "../icons";
import { Spinner } from "../ui";
import {
  searchVideos,
  type SearchMatch,
  type SearchMode,
  type SearchResult,
} from "../api/search";
import { ThumbFill } from "../components/ThumbFill";
import { formatDuration } from "../format";
import { splitHighlights } from "../highlight";
import { streamAnswer, type AnswerSource } from "../api/answer";
import { AnswerPanel, type AnswerState } from "../components/AnswerPanel";
import { DOT } from "../sep";

// Search — the global search view, with two modes over one query box.
//
// Find is a literal full-text search (FTS5, operators, bm25). Ask searches by
// meaning as well, using distance-bounded vector similarity. They differ only
// in how they retrieve: results render through the same cards either way, and
// the query text survives a mode switch, so coming up short in one mode and
// retrying in the other costs a single click.
//
// Clicking a match hands (videoId, startSeconds) up to `onOpen`, which App
// wires to the Player + a pending-seek (see App.tsx).

// Copy per mode. Find leads with precision, Ask with the fact that a whole
// question is allowed — the signal that was missing when one box did both.
const MODE_COPY: Record<
  SearchMode,
  { lead: string; hint: string; placeholder: string }
> = {
  find: {
    lead: "Find the exact words.",
    hint: "Keyword search across every transcript, summary and chapter in your library.",
    placeholder: "electrolytes endurance",
  },
  ask: {
    lead: "Ask Peeq about anything in your library.",
    hint: "Searches by meaning, not just wording — ask a full question and jump straight to the moment.",
    placeholder: "Did someone ever talk about electrolytes in endurance sport?",
  },
};

// The FTS5 operators Find passes through, shown as a resting row rather than a
// help popover: the whole problem being solved is that the box did not look
// like a search engine.
const OPERATORS: { syntax: string; means: string }[] = [
  { syntax: '"exact phrase"', means: "words together, in order" },
  { syntax: "sodium OR calcium", means: "either term" },
  { syntax: "cramp*", means: "starts with" },
  { syntax: "hydration NOT ad", means: "exclude" },
];

// TabState is everything one mode owns. Find and Ask are tabs, not two settings
// of one box: each keeps its own text and its own results, so switching shows
// you what that tab last did rather than reinterpreting the other tab's query.
type TabState = {
  query: string;
  results: SearchResult[] | null;
  searchedQuery: string | null;
  loading: boolean;
  error: string | null;
};

const EMPTY_TAB: TabState = {
  query: "",
  results: null,
  searchedQuery: null,
  loading: false,
  error: null,
};

export function Search({
  onOpen,
}: {
  onOpen: (videoId: string, startSeconds: number) => void;
}) {
  const [mode, setMode] = useState<SearchMode>("ask");
  const [tabs, setTabs] = useState<Record<SearchMode, TabState>>({
    find: EMPTY_TAB,
    ask: EMPTY_TAB,
  });
  // The answer belongs to the Ask tab alone, so it lives outside the record.
  const [answer, setAnswer] = useState<AnswerState | null>(null);
  // Aborts the in-flight answer when a new search starts, the tab is left, or
  // the view unmounts, so a generation nobody is waiting for stops costing a
  // model call.
  const answerAbort = useRef<AbortController | null>(null);
  // Every search takes a ticket; only the newest one may write state, so a slow
  // response for an abandoned query cannot land on top of a newer one.
  const runId = useRef(0);

  const tab = tabs[mode];

  function patchTab(m: SearchMode, patch: Partial<TabState>) {
    setTabs((prev) => ({ ...prev, [m]: { ...prev[m], ...patch } }));
  }

  function runSearch(q: string, m: SearchMode) {
    const trimmed = q.trim();
    const id = ++runId.current;
    if (m === "ask") {
      answerAbort.current?.abort();
      setAnswer(null);
    }
    if (!trimmed) {
      // The error line belongs to the query that failed. Emptying the box
      // retires that query, so leaving the error up would strand a complaint
      // about a search that is no longer on screen.
      patchTab(m, {
        results: null,
        searchedQuery: null,
        error: null,
        loading: false,
      });
      return;
    }
    patchTab(m, { loading: true, error: null });
    // Ask also writes an answer. It is a SEPARATE request on purpose: retrieval
    // returns in a moment and generation takes seconds, so the results paint
    // straight away and the answer fills in above them. Nothing below waits on
    // this, and a failure here never touches the results.
    if (m === "ask") runAnswer(trimmed, id);
    searchVideos(trimmed, m)
      .then((r) => {
        if (id !== runId.current) return;
        patchTab(m, { results: r, searchedQuery: trimmed, loading: false });
      })
      .catch((err: Error) => {
        if (id !== runId.current) return;
        // Clear any previous query's results so the error state doesn't
        // render stale result cards underneath the error line.
        patchTab(m, {
          error: err.message,
          results: null,
          searchedQuery: null,
          loading: false,
        });
      });
  }

  // runAnswer streams the grounded answer for one search. It shares runId with
  // the search so a superseded run cannot write over a newer one's answer.
  function runAnswer(q: string, id: number) {
    const ac = new AbortController();
    answerAbort.current = ac;
    setAnswer({ status: "streaming", text: "", sources: [] });
    let sources: AnswerSource[] = [];
    let text = "";
    let failed = false;

    streamAnswer(
      q,
      (e) => {
        if (id !== runId.current) return;
        switch (e.type) {
          case "sources":
            sources = e.sources;
            break;
          case "token":
            text += e.text;
            break;
          case "error":
            failed = true;
            break;
          case "done":
            break;
        }
        setAnswer({
          status: e.type === "done" ? "done" : "streaming",
          text,
          sources,
          failed,
        });
      },
      ac.signal,
    ).catch(() => {
      if (id !== runId.current) return;
      // The stream itself broke. Keep whatever text arrived — truncated is more
      // use than blank — and drop the panel entirely if none did, so the plain
      // results stand alone rather than under an error box.
      setAnswer(text ? { status: "done", text, sources, failed } : null);
    });
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    runSearch(tab.query, mode);
  }

  // Switching tabs shows what that tab already held. It does NOT carry the
  // current text across and it does NOT search: the two modes read a query
  // differently, so silently re-running Find's keywords as a question (or the
  // reverse) spends a model call on something nobody asked for.
  function switchMode(m: SearchMode) {
    if (m === mode) return;
    // Leaving Ask abandons any generation in flight; the text it produced stays
    // in `answer`, so coming back shows the answer rather than restarting it.
    if (mode === "ask") answerAbort.current?.abort();
    setMode(m);
  }

  const copy = MODE_COPY[mode];
  const { query, results, loading, error } = tab;
  // In Ask, everything BELOW the streaming answer waits for it to finish.
  // Retrieval returns long before generation does, so showing the moments and
  // the citation list first puts the evidence on screen ahead of the thing that
  // cites it — which reads backwards, and pulls the eye away from the text
  // actually being written.
  const answerStreaming = mode === "ask" && answer?.status === "streaming";
  const matchCount = results?.reduce((n, r) => n + r.matches.length, 0) ?? 0;

  return (
    <>
      <div className="gsearch-hero">
        <div className="modeswitch" role="group" aria-label="Search mode">
          <button
            type="button"
            aria-pressed={mode === "find"}
            onClick={() => switchMode("find")}
          >
            <Icon name="search" size="15px" />
            Find
          </button>
          <button
            type="button"
            aria-pressed={mode === "ask"}
            onClick={() => switchMode("ask")}
          >
            <Icon name="sparkles" size="15px" />
            Ask
          </button>
        </div>
        <p className="lead">{copy.lead}</p>
        <p className="hint">{copy.hint}</p>
        <form className="bigsearch" role="search" onSubmit={handleSubmit}>
          <Icon name={mode === "ask" ? "sparkles" : "search"} size="20px" />
          <input
            aria-label={mode === "ask" ? "Ask a question" : "Find words"}
            placeholder={copy.placeholder}
            value={query}
            onChange={(e) => patchTab(mode, { query: e.target.value })}
          />
          <kbd>↵</kbd>
        </form>
        {mode === "find" ? (
          <div className="syntax">
            <span className="cap">Operators</span>
            {OPERATORS.map((o) => (
              <code key={o.syntax} title={o.means}>
                {o.syntax}
              </code>
            ))}
          </div>
        ) : null}
        {results !== null && !loading ? (
          <p className="semantic-note">
            <Icon name={mode === "ask" ? "sparkles" : "search"} size="14px" />
            {mode === "ask"
              ? "Keyword and meaning, across transcripts, summaries and chapters."
              : "Exact words, across transcripts, summaries and chapters."}
          </p>
        ) : null}
      </div>

      {error ? <div className="errline">{error}</div> : null}
      {loading ? (
        <p
          style={{
            display: "flex",
            alignItems: "center",
            gap: 8,
            color: "var(--color-faint)",
          }}
        >
          <Spinner size="15px" />
          Searching
        </p>
      ) : null}

      {mode === "ask" && answer ? (
        <AnswerPanel state={answer} onOpen={onOpen} />
      ) : null}

      {!loading && !answerStreaming && results !== null && (
        <>
          <div className="results-head">
            <span style={{ fontFamily: "var(--font-serif)", fontSize: 16 }}>
              Matches
            </span>
            <span className="n mono">
              {results.length} video{results.length === 1 ? "" : "s"}
              {DOT}
              {matchCount} moment
              {matchCount === 1 ? "" : "s"}
            </span>
          </div>
          {results.length === 0 ? (
            <EmptyResult mode={mode} />
          ) : (
            results.map((r) => (
              <div className="result" key={r.video.id}>
                <div className="thumb">
                  <ThumbFill
                    id={r.video.id}
                    hasThumbnail={r.video.has_thumbnail}
                  />
                  <div className="play">
                    <Icon name="play" size="30px" />
                  </div>
                  <span className="dur mono">
                    {formatDuration(r.video.duration_seconds)}
                  </span>
                </div>
                <div className="rmeta">
                  <h3>{r.video.title}</h3>
                  <div className="ch">
                    {r.video.channel_name || r.video.channel_id}
                  </div>
                  <div className="matches">
                    {r.matches.map((m, i) => (
                      <button
                        key={i}
                        type="button"
                        className="match"
                        onClick={() => onOpen(r.video.id, m.start_seconds)}
                      >
                        <MatchLead match={m} />
                        <span className="snip">
                          <Snippet text={m.snippet} />
                        </span>
                      </button>
                    ))}
                  </div>
                </div>
              </div>
            ))
          )}
        </>
      )}
    </>
  );
}

// MatchLead renders the left of a match row: where the hit came from, and when
// it happens. A summary describes the whole video and has no timestamp; a
// chapter and a transcript moment both do.
//
// This replaces the raw vector distance that used to sit at the end of the row.
// That number was retrieval diagnostics — it is meaningless for a bm25-ranked
// keyword hit, and it told a user nothing they could act on.
function MatchLead({ match }: { match: SearchMatch }) {
  if (match.kind === "summary") {
    return <span className="badge">Summary</span>;
  }
  return (
    <>
      <span className="ts mono">{formatDuration(match.start_seconds)}</span>
      {match.kind === "chapter" ? (
        <span className="badge">Chapter</span>
      ) : (
        <span className="src">Transcript</span>
      )}
    </>
  );
}

// Snippet renders a search preview, marking the terms that matched. The
// backend delimits them with control characters rather than markup precisely so
// this can build text nodes and <mark> elements instead of setting innerHTML.
function Snippet({ text }: { text: string }) {
  const segments = splitHighlights(text);
  return (
    <>
      {segments.map((s, i) =>
        s.match ? <mark key={i}>{s.text}</mark> : <span key={i}>{s.text}</span>,
      )}
    </>
  );
}

// EmptyResult says which mode came up empty. Both sentences are answers rather
// than apologies: Find legitimately finds nothing when the words are not there,
// and Ask can now say the library covers nothing — which it could not do at all
// before the distance bound, since a KNN query always returned its k nearest.
//
// It offers no way out. The other tab is one visible click away with its own
// text, and a suggestion here would be guessing at what the reader wants next.
function EmptyResult({ mode }: { mode: SearchMode }) {
  return (
    <p className="noresults">
      {mode === "find"
        ? `None of your transcripts contain those words.`
        : `Nothing in your library covers that.`}
    </p>
  );
}
