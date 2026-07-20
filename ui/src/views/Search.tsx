import { useState, type FormEvent } from "react";
import { Icon } from "../icons";
import { Spinner } from "../ui";
import { searchVideos, type SearchResult } from "../api/search";
import { thumbnailUrl } from "../api/videos";
import { formatDuration, gradientClassFor } from "../format";

// Search — the global semantic-search view (Task 18), per the mockup's
// `.gsearch-hero`/`.result`/`.match` blocks: a query box over `searchVideos`
// (Task 16's sqlite-vec KNN endpoint), results grouped by video, each a
// video card listing its matched transcript/summary chunks (snippet +
// timestamp + distance). Clicking a match hands (videoId, startSeconds) up
// to `onOpen`, which App wires to the Player + a pending-seek (see App.tsx).
export function Search({ onOpen }: { onOpen: (videoId: string, startSeconds: number) => void }) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SearchResult[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const q = query.trim();
    if (!q) {
      setResults(null);
      return;
    }
    setLoading(true);
    setError(null);
    searchVideos(q)
      .then((r) => setResults(r))
      .catch((err: Error) => {
        setError(err.message);
        // Clear any previous query's results so the error state doesn't
        // render stale result cards underneath the error line.
        setResults(null);
      })
      .finally(() => setLoading(false));
  }

  const matchCount = results?.reduce((n, r) => n + r.matches.length, 0) ?? 0;

  return (
    <>
      <div className="gsearch-hero">
        <p className="lead">
          Search everything you've archived — by keyword and meaning, across transcripts and summaries.
        </p>
        <p className="hint">Semantic search over every transcript. Jumps straight to the moment.</p>
        <form className="bigsearch" role="search" onSubmit={handleSubmit}>
          <Icon name="search" size="20px" />
          <input
            placeholder="Search everything you've watched…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          <kbd>↵</kbd>
        </form>
        {results !== null && !loading ? (
          <p className="semantic-note">
            <Icon name="search" size="14px" /> Semantic search over every transcript chunk.
          </p>
        ) : null}
      </div>

      {error ? <div className="errline">{error}</div> : null}
      {loading ? (
        <p style={{ display: "flex", alignItems: "center", gap: 8, color: "var(--color-faint)" }}>
        <Spinner size="15px" />
        Searching
      </p>
      ) : null}

      {!loading && results !== null && (
        <>
          <div className="results-head">
            <span style={{ fontFamily: "var(--font-serif)", fontSize: 16 }}>Matches</span>
            <span className="n mono">
              {results.length} video{results.length === 1 ? "" : "s"} · {matchCount} moment
              {matchCount === 1 ? "" : "s"}
            </span>
          </div>
          {results.length === 0 ? (
            <p style={{ color: "var(--color-faint)" }}>No matches. Try a different phrase.</p>
          ) : (
            results.map((r) => (
              <div className="result" key={r.video.id}>
                <div className="thumb">
                  {r.video.has_thumbnail ? (
                    <img className="fill" src={thumbnailUrl(r.video.id)} alt="" loading="lazy" />
                  ) : (
                    <div className={`fill ${gradientClassFor(r.video.id)}`} />
                  )}
                  <div className="play">
                    <Icon name="play" size="30px" />
                  </div>
                  <span className="dur mono">{formatDuration(r.video.duration_seconds)}</span>
                </div>
                <div className="rmeta">
                  <h3>{r.video.title}</h3>
                  <div className="ch">{r.video.channel_name || r.video.channel_id}</div>
                  <div className="matches">
                    {r.matches.map((m, i) => (
                      <button
                        key={i}
                        type="button"
                        className="match"
                        onClick={() => onOpen(r.video.id, m.start_seconds)}
                      >
                        {m.kind === "summary" ? (
                          <span className="badge">Summary</span>
                        ) : (
                          <span className="ts mono">{formatDuration(m.start_seconds)}</span>
                        )}
                        <span className="snip">{m.snippet}</span>
                        <span className="score">{m.distance.toFixed(2)}</span>
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
