import { useRef, useState } from "react";
import { searchVideos, type SearchMode } from "./api/search";
import {
  streamAnswer,
  type AnswerSource,
  type AnswerVideo,
} from "./api/answer";
import { citedInOrder, groupCited } from "./answerSources";
import type { AnswerState } from "./components/AnswerPanel";
import type { ResultCardGroup } from "./components/ResultCards";

// searchState — the Search view's engine, held ABOVE the view.
//
// Search used to own all of this in local useState, and ViewSwitch is a plain
// switch: opening a result unmounted the view and destroyed the query, the
// results and the answer with it. That is the wrong trade for this view in
// particular. An Ask query costs an embedding, a keyword ladder and a model
// call, and finding four promising videos and watching two of them is the
// normal way to use it — every one of those trips paid for the same answer
// again.
//
// So App calls this hook and hands the result down (the same shape the five
// per-view search boxes already take: App holds the text, the view renders it).
// The refs live here too, which is the part that matters: a run ticket or an
// abort controller recreated on every mount cannot arbitrate between a stream
// started before a detour and a search started after one.

export type { SearchMode };

// TabState is everything one mode owns. Find and Ask are tabs, not two settings
// of one box: each keeps its own text and its own results, so switching shows
// you what that tab last did rather than reinterpreting the other tab's query.
export type TabState = {
  query: string;
  // Find's groups come from /api/search; Ask's are derived from the moments the
  // answer cited. ResultCardGroup is what both render as.
  results: ResultCardGroup[] | null;
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

export type SearchState = {
  mode: SearchMode;
  tab: TabState;
  answer: AnswerState | null;
  setMode: (m: SearchMode) => void;
  setQuery: (q: string) => void;
  submit: () => void;
  // Empties the box's text and nothing else — the results stay up, because
  // retyping over a search is not the same as being done with it.
  clearQuery: () => void;
  // Puts the whole search away: this tab's results, its error, and (on Ask) the
  // answer above them. The other tab is untouched, like every other thing in
  // TabState.
  clearResults: () => void;
};

export function useSearchState(): SearchState {
  const [mode, setMode] = useState<SearchMode>("ask");
  const [tabs, setTabs] = useState<Record<SearchMode, TabState>>({
    find: EMPTY_TAB,
    ask: EMPTY_TAB,
  });
  // The answer belongs to the Ask tab alone, so it lives outside the record.
  const [answer, setAnswer] = useState<AnswerState | null>(null);
  // Aborts the in-flight answer when a NEW search starts. It deliberately no
  // longer fires when the view is left: the model call is already paid for, and
  // a generation that finishes into this state is one the reader gets to see
  // when they come back, instead of a half-written paragraph frozen mid-word.
  const answerAbort = useRef<AbortController | null>(null);
  // Every search takes a ticket; only the newest one may write state, so a slow
  // response for an abandoned query cannot land on top of a newer one. Living
  // in the hook rather than in the view is what makes that true across a detour
  // into a video: a ticket counter that resets on remount would let a stream
  // started before the detour overwrite a search started after it.
  //
  // ONE TICKET PER TAB. A single shared counter looks equivalent — only one
  // search runs at a time from the reader's point of view — but the two tabs
  // hold separate state and can genuinely be in flight together, and a Find
  // search would then invalidate an Ask answer still being written. Nothing
  // aborts that stream any more, so it would run to completion and have its
  // answer thrown away on arrival: the model call paid for, the tab still empty,
  // and asking again the only way to see it.
  const runIds = useRef<Record<SearchMode, number>>({ find: 0, ask: 0 });

  function patchTab(m: SearchMode, patch: Partial<TabState>) {
    setTabs((prev) => ({ ...prev, [m]: { ...prev[m], ...patch } }));
  }

  function runSearch(q: string, m: SearchMode) {
    const trimmed = q.trim();
    const id = ++runIds.current[m];
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
    // Ask makes ONE request. Its moments are the ones the answer cited, which
    // the answer stream already carries — a second /api/search would spend
    // another embedding call and another keyword ladder to produce a wider list
    // that this view no longer shows.
    if (m === "ask") {
      runAnswer(trimmed, id);
      return;
    }
    searchVideos(trimmed, m)
      .then((r) => {
        if (id !== runIds.current[m]) return;
        patchTab(m, { results: r, searchedQuery: trimmed, loading: false });
      })
      .catch((err: Error) => {
        if (id !== runIds.current[m]) return;
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

  // runAnswer streams the grounded answer for one search. It shares the Ask
  // tab's ticket with the search that started it, so a superseded run cannot
  // write over a newer one's answer.
  function runAnswer(q: string, id: number) {
    const ac = new AbortController();
    answerAbort.current = ac;
    // The first phase: the backend is working out what the question is about,
    // and nothing is on the wire yet. Starting at "understanding" rather than at
    // a generic streaming state is what lets the panel say so.
    setAnswer({ status: "understanding", text: "", sources: [] });
    let phase: AnswerState["status"] = "understanding";
    let topic = "";
    let sources: AnswerSource[] = [];
    let videos: AnswerVideo[] = [];
    let coverage: AnswerVideo[] = [];
    let text = "";
    let failed = false;
    // Whether retrieval reported at all. An empty source list means the library
    // covers nothing; never hearing one means the request broke, and the two
    // must not read the same on screen.
    let retrieved = false;

    // settle writes the moments once the answer is done with them. They are the
    // cited ones and nothing else:
    //
    //   the library covers nothing -> [] , so EmptyResult offers Find instead
    //   the answer cited moments   -> those moments, in citation order
    //   anything else              -> null, and no Matches section at all
    //
    // The last row covers a failed answer and an answer that named no moment.
    // Both used to fall back to the whole retrieved set, which is how a question
    // about transients ended up listing videos that never mention them.
    const settle = () => {
      const empty = retrieved && sources.length === 0;
      const cited = empty
        ? []
        : groupCited(citedInOrder(text, sources), videos);
      patchTab("ask", {
        results: empty || cited.length ? cited : null,
        searchedQuery: q,
        loading: false,
      });
    };

    streamAnswer(
      q,
      (e) => {
        if (id !== runIds.current.ask) return;
        switch (e.type) {
          case "progress":
            phase = "retrieving";
            topic = e.topic;
            break;
          case "sources":
            sources = e.sources;
            videos = e.videos;
            coverage = e.coverage;
            retrieved = true;
            // Retrieval is done and the model call starts here — the long, silent
            // part of the wait. This is the transition the panel could not see
            // before: the frame already arrived, nothing acted on it.
            phase = "generating";
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
          status: e.type === "done" ? "done" : phase,
          topic,
          text,
          sources,
          videos,
          coverage,
          failed,
        });
        // The done frame is the normal end, and acting on it rather than
        // waiting for the socket to close puts the moments up a beat sooner.
        // settle is idempotent, so the safety net below can run again.
        if (e.type === "done") settle();
      },
      ac.signal,
    )
      // Whatever text arrived is kept — truncated is more use than blank — but
      // a stream that broke before saying anything has to SAY so. Ask makes one
      // request now, so there is no longer a parallel /api/search whose own
      // error line covers this: swallowing it left the whole page below the box
      // blank, and pressing enter looked like it did nothing at all. A 401
      // arrives here as AuthExpiredError ("auth expired") and reads the same way
      // it does in Find, which is the only place it is ever surfaced.
      //
      // An abort is not a failure: starting a newer search rejects this promise
      // on purpose, and that does not owe the reader an error.
      .catch((err: Error) => {
        if (id !== runIds.current.ask || ac.signal.aborted) return;
        patchTab("ask", { error: err.message });
      })
      .finally(() => {
        if (id !== runIds.current.ask) return;
        // Every way the stream can end comes through here: a done frame, a
        // broken connection, or an abort when a newer search starts. Settling
        // only on `done` left the other two streaming forever — a blinking
        // caret over a spinner that never stopped.
        //
        // Nothing written and nothing to report means no panel at all. A
        // reported failure keeps one: with no moments below it either, dropping
        // it would leave a page that says nothing happened.
        setAnswer(
          text || failed
            ? { status: "done", text, sources, videos, coverage, failed }
            : null,
        );
        settle();
      });
  }

  const tab = tabs[mode];

  return {
    mode,
    tab,
    answer,
    // Switching tabs shows what that tab already held. It does NOT carry the
    // current text across and it does NOT search: the two modes read a query
    // differently, so silently re-running Find's keywords as a question (or the
    // reverse) spends a model call on something nobody asked for.
    //
    // Leaving Ask no longer aborts a generation in flight either. It costs
    // nothing to let it land, and coming back to a finished answer beats coming
    // back to one that stopped mid-sentence.
    setMode,
    setQuery: (q: string) => patchTab(mode, { query: q }),
    submit: () => runSearch(tab.query, mode),
    clearQuery: () => patchTab(mode, { query: "" }),
    clearResults: () => {
      // Retiring this tab's ticket is what makes the clear stick: a request
      // still in flight would otherwise land afterwards and repopulate the page
      // the reader just emptied. Both modes, not just Ask — the header only
      // offers this button once results are on screen, so the race is not
      // reachable today, but "cleared" has to mean cleared whatever the button
      // is wired to next.
      runIds.current[mode]++;
      // A generation still being written is part of what is being put away.
      // Unlike leaving the view, this says the reader is done with it.
      if (mode === "ask") {
        answerAbort.current?.abort();
        setAnswer(null);
      }
      patchTab(mode, {
        results: null,
        searchedQuery: null,
        error: null,
        loading: false,
      });
    },
  };
}
