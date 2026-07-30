import { useRef, useState, type FormEvent } from "react";
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
    lead: "Ask Peeq anything you've watched.",
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

export function Search({
  onOpen,
}: {
  onOpen: (videoId: string, startSeconds: number) => void;
}) {
  const [query, setQuery] = useState("");
  const [mode, setMode] = useState<SearchMode>("find");
  const [results, setResults] = useState<SearchResult[] | null>(null);
  const [searched, setSearched] = useState<{
    q: string;
    mode: SearchMode;
  } | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Every search takes a ticket; only the newest one may write state. A mode
  // switch fires a search on a single click, so two quick clicks put two
  // requests in flight — and without this the slower one could land last and
  // leave `searched` (which decides the empty-state sentence and which mode the
  // offer button proposes) describing a search the user has already left.
  const runId = useRef(0);

  function runSearch(q: string, m: SearchMode) {
    const trimmed = q.trim();
    const id = ++runId.current;
    if (!trimmed) {
      setResults(null);
      setSearched(null);
      setLoading(false);
      return;
    }
    setLoading(true);
    setError(null);
    searchVideos(trimmed, m)
      .then((r) => {
        if (id !== runId.current) return;
        setResults(r);
        setSearched({ q: trimmed, mode: m });
      })
      .catch((err: Error) => {
        if (id !== runId.current) return;
        setError(err.message);
        // Clear any previous query's results so the error state doesn't
        // render stale result cards underneath the error line.
        setResults(null);
        setSearched(null);
      })
      .finally(() => {
        if (id === runId.current) setLoading(false);
      });
  }

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    runSearch(query, mode);
  }

  // Switching mode re-runs the query that is already in the box, so "this found
  // nothing, try the other one" is one click and not a retype.
  function switchMode(m: SearchMode) {
    if (m === mode) return;
    setMode(m);
    if (results !== null || query.trim()) runSearch(query, m);
  }

  const copy = MODE_COPY[mode];
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
            onChange={(e) => setQuery(e.target.value)}
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

      {!loading && results !== null && (
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
            <EmptyResult
              mode={searched?.mode ?? mode}
              query={searched?.q ?? query.trim()}
              onSwitch={() => switchMode(mode === "find" ? "ask" : "find")}
            />
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

// EmptyResult says which mode came up empty and offers the other one. Find can
// legitimately find nothing — that is what a keyword search is for — and the
// useful next step is almost always to try meaning instead, or vice versa.
function EmptyResult({
  mode,
  query,
  onSwitch,
}: {
  mode: SearchMode;
  query: string;
  onSwitch: () => void;
}) {
  return (
    <div className="noresults">
      <p>
        {mode === "find"
          ? `None of your transcripts contain those words.`
          : `Nothing in your library covers that.`}
      </p>
      <button type="button" className="linkish" onClick={onSwitch}>
        {mode === "find"
          ? `Ask about "${query}" by meaning instead`
          : `Search for the exact words "${query}" instead`}
      </button>
    </div>
  );
}
