import { useEffect, useRef, type FormEvent } from "react";
import { Icon } from "../icons";
import { Button, Spinner } from "../ui";
import type { SearchMode } from "../api/search";
import { AnswerPanel } from "../components/AnswerPanel";
import { ResultCards } from "../components/ResultCards";
import type { SearchState } from "../searchState";
import { DOT } from "../sep";

// Search — the global search view, with two modes over one query box.
//
// Find is a literal full-text search (FTS5, operators, bm25). Ask searches by
// meaning as well, using distance-bounded vector similarity. They differ only
// in how they retrieve: results render through the same cards either way, and
// the query text survives a mode switch, so coming up short in one mode and
// retrying in the other costs a single click.
//
// The view is presentational. Everything it shows lives in useSearchState,
// which App calls — see searchState.ts for why: results have to survive being
// left for a video and come back intact, and this component does not survive
// that trip.
//
// Clicking a match hands (videoId, startSeconds) up to `onOpen`, which App
// wires to the Player + a pending-seek (see App.tsx). The title, the thumbnail
// and a summary match go through `onOpenVideo` instead — no seek at all, which
// is not the same as a seek to zero.

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

export function Search({
  search,
  onOpen,
  onOpenVideo,
  onOpenChannel,
}: {
  search: SearchState;
  onOpen: (videoId: string, startSeconds: number) => void;
  onOpenVideo: (videoId: string) => void;
  onOpenChannel: (channelId: string) => void;
}) {
  const { mode, tab, answer } = search;

  const inputRef = useRef<HTMLInputElement>(null);

  function handleSubmit(e: FormEvent) {
    e.preventDefault();
    // Submitting hands the box back: the question is asked, the caret has
    // nothing left to do in it, and on a phone the keyboard covers the results
    // it was typed to find. Clicking the box returns focus, with the query
    // still in it to edit.
    inputRef.current?.blur();
    search.submit();
  }

  function handleClearResults() {
    search.clearResults();
    // Clearing puts the page away, and the button doing it disappears with the
    // results — leaving focus on nothing. It belongs back in the box, which is
    // the only thing left to act on. The query stays, with the caret at its
    // end: the usual next move is to amend the words that came up short, not
    // to start over.
    const input = inputRef.current;
    if (!input) return;
    input.focus();
    input.setSelectionRange(input.value.length, input.value.length);
  }

  const copy = MODE_COPY[mode];
  const { query, results, loading, error } = tab;

  // Landing on this view, the box is the only thing there is to do — so it
  // takes the caret rather than making you click it first. The dependency list
  // is empty on purpose: this view renders only on the search route, so the
  // effect runs once per arrival and the `query` it reads is the one that was
  // there on arrival, not whatever gets typed afterwards.
  //
  // Not when the box already holds something. A query and its results survive
  // navigation, so a non-empty box means coming BACK to an answer left behind
  // — and focusing then would scroll the page to the top and, on a phone, put
  // the keyboard over the thing being returned to.
  useEffect(() => {
    if (query) return;
    inputRef.current?.focus();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  // In Ask, everything BELOW the streaming answer waits for it to finish.
  // Retrieval returns long before generation does, so showing the moments and
  // the citation list first puts the evidence on screen ahead of the thing that
  // cites it — which reads backwards, and pulls the eye away from the text
  // actually being written.
  // Every phase before "done" counts: understanding, retrieving and generating
  // are all "the answer has not finished". Testing for one named phase here
  // would silently stop matching the moment the phases were split apart.
  const answerStreaming =
    mode === "ask" && answer != null && answer.status !== "done";
  const matchCount = results?.reduce((n, r) => n + r.matches.length, 0) ?? 0;

  // What retrieval found and the answer did not quote. The subtraction happens
  // here, and it has to happen on the client: the coverage frame goes out before
  // generation, when nothing yet knows what will be cited.
  //
  // `results` in Ask IS the cited set (searchState derives it from the moments
  // the answer named), so subtracting the videos in it is the whole derivation.
  // When it is null the answer cited nothing, and every retrieved video belongs
  // in this tier — which is the case that would otherwise lose the list entirely,
  // since the matches block above does not render at all.
  //
  // Gated only on the mode. WHEN it may be shown is the render's business, and
  // testing that here as well would be the same condition in two places, free to
  // drift apart.
  const alsoRows =
    mode === "ask"
      ? (() => {
          const cited = new Set((results ?? []).map((r) => r.video.id));
          return (answer?.coverage ?? []).filter((v) => !cited.has(v.id));
        })()
      : [];

  return (
    <>
      <div className="gsearch-hero">
        {/* Ask leads because Ask is the tab the view opens on. A selected tab
            sitting to the right of an unselected one reads as the second
            choice. */}
        <div className="modeswitch" role="group" aria-label="Search mode">
          <button
            type="button"
            aria-pressed={mode === "ask"}
            onClick={() => search.setMode("ask")}
          >
            <Icon name="sparkles" size="15px" />
            Ask
          </button>
          <button
            type="button"
            aria-pressed={mode === "find"}
            onClick={() => search.setMode("find")}
          >
            <Icon name="search" size="15px" />
            Find
          </button>
        </div>
        <p className="lead">{copy.lead}</p>
        <p className="hint">{copy.hint}</p>
        <form className="bigsearch" role="search" onSubmit={handleSubmit}>
          <Icon name={mode === "ask" ? "sparkles" : "search"} size="20px" />
          <input
            ref={inputRef}
            aria-label={mode === "ask" ? "Ask a question" : "Find words"}
            placeholder={copy.placeholder}
            value={query}
            onChange={(e) => search.setQuery(e.target.value)}
          />
          {/* Only while there is text to remove. It empties the box so the next
              question can be typed over nothing; the results below stay, and go
              away by their own button. */}
          {query ? (
            <button
              type="button"
              className="clear-query"
              aria-label="Clear the search box"
              onClick={search.clearQuery}
            >
              <Icon name="x" size="16px" />
            </button>
          ) : null}
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
      </div>

      {error ? <div className="errline">{error}</div> : null}
      {/* Find's wait indicator only. Ask stays "loading" for as long as the
          answer is being written — that is what keeps the previous query's
          moments off the screen — and its wait is already spoken for by the
          panel's own spinner, then by the words arriving. Two spinners for one
          wait is worse than the line this replaced. */}
      {loading && mode !== "ask" ? (
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
        <AnswerPanel
          state={answer}
          onOpen={onOpen}
          onOpenVideo={onOpenVideo}
          onOpenChannel={onOpenChannel}
        />
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
            {/* The way out of a search, next to the thing it puts away. The
                header deliberately does NOT also echo the query: an Ask query is
                a whole sentence, and a row carrying one alongside the counts
                wraps or truncates — worst on a phone. The box above already
                shows it, and scrolls. */}
            <Button
              type="button"
              variant="secondary"
              small
              className="clear-results"
              onClick={handleClearResults}
            >
              Clear results
            </Button>
          </div>
          {results.length === 0 ? (
            <EmptyResult mode={mode} />
          ) : (
            <ResultCards
              results={results}
              onOpen={onOpen}
              onOpenVideo={onOpenVideo}
              onOpenChannel={onOpenChannel}
            />
          )}
        </>
      )}

      {/* Retrieved, and the answer did not quote it. Its own block rather than a
          tail on the matches above, for two reasons. The counts in that header
          say what the answer stood on, and folding in videos it never cited would
          overstate them — this heading carries its own number instead. And when
          the answer cites nothing at all the header does not render, while these
          videos still should: they are the only thing left on the page that says
          retrieval found anything.

          The cards are compact because they have nothing to be tall for — no
          moments were matched in them, so a full-height card is a poster beside a
          title and then empty space. Each opens its video without seeking, as the
          text rows here used to: there IS a retrieved chunk behind every one, but
          the model never vouched for it, and jumping to a timestamp on the
          strength of a distance score asserts more than is known. */}
      {!loading && !answerStreaming && alsoRows.length ? (
        <>
          <div className="also-head">
            {/* Label and count are ONE flex item, not two. The separator between
                them is DOT, which carries its own en spaces (see sep.ts), so they
                must not also be spaced by the row's gap — and that gap has to
                stay, because it is the only thing keeping the clear button off
                the count once the row runs out of width on a phone. Wrapping the
                pair is what lets both be true. */}
            <span className="also-head-txt">
              <span className="also-head-t">Also in your library</span>
              {/* "N videos", not a bare "N" — the same words and the same place
                  the matches header counts in, so the two rows read as one family
                  rather than one of them dropping its unit. */}
              <span className="n mono">
                {DOT}
                {alsoRows.length} video{alsoRows.length === 1 ? "" : "s"}
              </span>
            </span>
            {/* The way out of the search lives in the matches header — and that
                header is exactly what is missing when the answer cited nothing,
                which is the one case this block renders on its own. Without this
                the reader would be left with up to coverageMaxVideos cards and no
                control to put them away. It appears ONLY then, so the ordinary
                page still has a single Clear results and not two. */}
            {results === null ? (
              <Button
                type="button"
                variant="secondary"
                small
                className="clear-results"
                onClick={handleClearResults}
              >
                Clear results
              </Button>
            ) : null}
          </div>
          <ResultCards
            compact
            results={alsoRows.map((video) => ({ video, matches: [] }))}
            onOpen={onOpen}
            onOpenVideo={onOpenVideo}
            onOpenChannel={onOpenChannel}
          />
        </>
      ) : null}
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
//
// MatchLead and Snippet used to live here too; they moved into ResultCards with
// the card markup, which Find and Ask now share.
function EmptyResult({ mode }: { mode: SearchMode }) {
  return (
    <p className="noresults">
      {mode === "find"
        ? `None of your transcripts contain those words.`
        : `Nothing in your library covers that.`}
    </p>
  );
}
