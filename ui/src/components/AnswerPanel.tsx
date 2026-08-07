import { useState } from "react";
import { Icon } from "../icons";
import { Spinner } from "../ui";
import { formatDuration, formatMs } from "../format";
import {
  answerParts,
  citedInOrder,
  type CitedSource,
  type RenderedPart,
} from "../answerSources";
import { splitIntoSegments } from "../streamFade";
import type { EmphasisMark } from "../emphasis";
import type {
  AnswerSource,
  AnswerVideo,
  LibraryCount,
  TraceStage,
} from "../api/answer";

// AnswerPanel renders the grounded answer above Ask's moments.
//
// Sources are the videos the answer CITED, numbered from 1 in the order it
// mentions them — one row per video, not per passage. Retrieval hands the model
// twelve passages; the ones it did not use are a working set, not findings, and
// listing them made the answer look like it was skipping numbered evidence
// ("[2] [4] [5]", no [1] in sight).
// See answerSources.ts — the same derivation feeds the moments below.

export type AnswerState = {
  // The wait has three distinct parts and they are not interchangeable:
  //
  //   understanding — working out what the question is about (~1-2s)
  //   retrieving    — searching the library (well under a second)
  //   generating    — the model is thinking, then writing (~5s to first word)
  //
  // They exist as separate values because the panel has something different and
  // TRUE to say during each. Collapsing them back into one "streaming" is what
  // made the whole wait a single spinner that could not tell a reader whether
  // retrieval had already succeeded.
  //
  // Everything that used to ask `status === "streaming"` means "not finished",
  // which is now every value except "done" — see `streaming` below.
  status: "understanding" | "retrieving" | "generating" | "done";
  // The understood query, from the progress frame. Shown while retrieving so a
  // rewrite that mangles the question is visible rather than silent. Empty when
  // the question needed no rewriting or the step did not run.
  topic?: string;
  text: string;
  sources: AnswerSource[];
  // The videos frame of the same stream. A source carries channel_name but no
  // channel_id, and the channel name in a source row has to navigate somewhere —
  // so the id is looked up here by video_id rather than widened onto every
  // source server-side.
  videos?: AnswerVideo[];
  // Every video retrieval found, one entry each, best-ranked first — including
  // the ones the answer went on to cite. The panel does not render these: Search
  // subtracts the cited set and shows what is left as its own tier of cards under
  // the matches. It is carried on this state anyway because this is where the
  // answer stream's frames land, and the subtraction cannot happen server-side —
  // the frame carrying it is sent before the model has written anything.
  coverage?: AnswerVideo[];
  // The constraints the question named and the search applied — "unwatched",
  // "Veritasium". Shown for the same reason `topic` is, and with more at stake:
  // a mangled rewrite makes the answer worse, a wrong filter makes videos
  // disappear with nothing on screen to say so.
  filters?: string[];
  // Constraints that found nothing and were dropped so the search could return
  // something. Non-empty means what is shown is the whole library rather than
  // the slice that was asked for.
  relaxed?: string[];
  // Channel names the library has nothing under. The constraint was dropped;
  // the answer's first sentence says so, and the chip row shows it struck out.
  unresolvedChannels?: string[];
  // Only for an inventory question. Computed in SQL under the constraints, so
  // it is the whole count rather than a tally of what happened to be cited.
  counts?: LibraryCount;
  // How the answer was made, one entry per step that actually ran. Absent until
  // the stream is nearly over — the frame carrying it is sent after generation,
  // because it reports what generation cost — so the panel only ever draws it
  // settled, which is also the only time anyone wants it.
  trace?: TraceStage[];
  failed?: boolean;
};

// waitLabel names the phase the reader is waiting through. Each line is true
// only of its own phase, which is the whole reason the phases exist separately:
// one label across the entire wait could not say that retrieval had already
// succeeded, so five seconds of the model thinking read exactly like a stall.
//
// The UNDERSTOOD query rides on BOTH of the phases that have one, and that is
// not decoration. It is the reader's only view of what was actually searched
// for, and the only way a rewrite that drops the wrong word gets noticed instead
// of quietly returning worse answers. Shown on the retrieving line alone it
// could not do that job: retrieval is well under a second, while understanding
// takes a second or two and generating around five — so the one label carrying
// the topic would flash past between two long ones, and a topic mis-extracted as
// "material science" would be on screen for less time than it takes to read. It
// stays up through the long phase instead, where it can actually be seen.
function waitLabel(
  status: AnswerState["status"],
  topic: string | undefined,
  videoCount: number,
): string {
  switch (status) {
    case "understanding":
      return "Understanding your question";
    case "retrieving":
      return topic ? `Searching for “${topic}”` : "Searching your library";
    default: {
      // Generating: the long one. Naming the count is what makes the wait feel
      // like progress rather than a hang — it says retrieval already worked.
      //
      // "Thinking", not "Reading". Reading was accurate while ONE label covered
      // the whole wait, retrieval included — but by the time this phase starts,
      // the library has already been read and the passages are in hand. What the
      // five seconds actually are is the model reasoning over them: thinking is
      // on for this call, and that is where the time goes.
      if (videoCount > 0) {
        const base = `Thinking about ${videoCount} video${videoCount === 1 ? "" : "s"}`;
        // The count comes FIRST, so the part that says retrieval succeeded is
        // what survives if .lbl has to ellipsize a long topic.
        return topic ? `${base} on “${topic}”` : base;
      }
      // No count to name. Rare — generating begins on the sources frame, which
      // carries the videos — so this is the empty-retrieval case, and it must not
      // claim a number it does not have.
      return topic ? `Thinking about “${topic}”` : "Thinking";
    }
  }
}

// ScopeChip is one constraint and whether it survived.
type ScopeChip = { label: string; dropped: boolean; why?: string };

// scopeChips renders the search's scope as chips: what applied, then what was
// asked for and did not.
//
// The two dropped kinds are kept separate in the tooltip because they are
// different problems. An unresolved channel is a name the library has nothing
// under — usually a typo too far gone to match, sometimes a channel the reader
// only thinks they have. A relaxed constraint matched a real thing and simply
// found nothing under it. Both leave the reader looking at a wider search than
// they asked for, which is why neither is allowed to be silent.
function scopeChips(state: AnswerState): ScopeChip[] {
  const { filters = [], relaxed = [], unresolvedChannels = [] } = state;
  // A relaxed search applied nothing: `filters` is what was asked for, and
  // showing it as applied would be exactly backwards.
  const applied = relaxed.length ? [] : filters;
  return [
    ...applied.map((label) => ({ label, dropped: false })),
    ...relaxed.map((label) => ({
      label,
      dropped: true,
      why: "Nothing matched this, so the search was widened.",
    })),
    ...unresolvedChannels.map((label) => ({
      label,
      dropped: true,
      why: "No channel by this name is in your library.",
    })),
  ];
}

export function AnswerPanel({
  state,
  onOpen,
  onOpenVideo,
  onOpenChannel,
}: {
  state: AnswerState;
  onOpen: (videoId: string, startSeconds: number) => void;
  // Every source row opens its video where the viewer left it: a row stands for
  // the video, not for one of the moments cited inside it. Seeking is what the
  // inline numerals do.
  onOpenVideo: (videoId: string) => void;
  onOpenChannel: (channelId: string) => void;
}) {
  const { status, topic, text, sources, videos, failed, counts } = state;
  // The scope row: what the search was actually narrowed to, plus the
  // constraints that were asked for and did not survive. Both belong in one row
  // — a reader wants "what was searched", and a dropped constraint is part of
  // that answer, not a separate topic.
  const chips = scopeChips(state);
  const channelOf = new Map(
    (videos ?? []).map((v) => [v.id, v.channel_id] as const),
  );
  // The body's parts come from answerSources too, so the marks above and the
  // rows below cannot disagree about what a numeral means. It is what resolves
  // each mark to its source and collapses a run of adjacent same-numeral marks.
  // "not finished" — the meaning every existing use of this flag already had.
  const streaming = status !== "done";
  // The spinner covers the wait BEFORE the first word. Once text is arriving it
  // says the same thing better. This is the gate the phase label hangs off: a
  // wider status union alone would change nothing, because this condition reads
  // the same whichever unfinished value status holds.
  const waiting = streaming && !text;
  // `streaming` is what lets answerParts hold back a mark whose sentence has not
  // finished arriving — see marksAfterSentenceEnd.
  const parts = answerParts(text, sources, streaming);
  const cited = citedInOrder(text, sources);
  // One row per video. `cited` stays one entry per citation — the marks above
  // and the moment cards below both need that — but a video is ONE source
  // however many of its passages the answer leaned on. First mention wins, and
  // it is already the entry whose number the whole video carries.
  const seen = new Set<string>();
  const rows = cited.filter((s) => {
    if (seen.has(s.video_id)) return false;
    seen.add(s.video_id);
    return true;
  });

  // Nothing to show at all — the parent renders whatever it has rather than an
  // empty box.
  if (!text && !sources.length && !streaming) return null;

  return (
    <div className="answer">
      <div className="hd">
        <Icon name="sparkles" size="14px" />
        Answer
        {/* `waiting` reserves a fixed width for the label (see .status.waiting).
            The status block is pushed right and the spinner is its FIRST child,
            so without a reserve the spinner slides sideways every time the label
            changes length — and it now changes three times. The reserve is only
            applied while waiting, so the settled "N sources" keeps sitting hard
            against the right edge exactly as it always has. */}
        <span className={waiting ? "status waiting" : "status"}>
          {/* The spinner belongs to the wait before the first token. Once words
              are arriving they say the same thing better, and the caret below
              carries "still going". */}
          {waiting ? (
            <>
              <Spinner size="12px" />
              <span className="lbl">
                {waitLabel(status, topic, (videos ?? []).length)}
              </span>
            </>
          ) : !streaming && rows.length ? (
            `${rows.length} source${rows.length === 1 ? "" : "s"}`
          ) : null}
        </span>
      </div>

      {/* What the search was narrowed to, shown from the moment retrieval
          starts rather than at the end. A filter the reader did not intend is
          otherwise invisible: the prose reads perfectly whether or not it came
          from the slice they meant, and there is nothing else on the page that
          could tell them. A dropped constraint is struck through, so "asked for
          and not applied" cannot be mistaken for "applied". */}
      {chips.length ? (
        <div className="answer-scope">
          <span className="lbl">Searched</span>
          {chips.map((c) => (
            <span
              key={c.label}
              className={c.dropped ? "chip dropped" : "chip"}
              title={c.dropped ? c.why : undefined}
            >
              {c.label}
            </span>
          ))}
        </div>
      ) : null}

      {/* The count answers "how many", which twelve excerpts cannot. It is
          computed in SQL under the constraints above, so it stands apart from
          the prose rather than inside it. */}
      {counts ? (
        <p className="answer-count">
          <strong>{counts.videos}</strong>{" "}
          {counts.videos === 1 ? "video" : "videos"}
          {counts.channels > 1 ? ` across ${counts.channels} channels` : null}
          {counts.videos > 0
            ? ` · ${formatDuration(counts.duration_seconds)}`
            : null}
        </p>
      ) : null}

      <div className="answer-body" aria-live="polite" aria-busy={streaming}>
        {parts.map((part, i) =>
          part.kind === "cite" ? (
            <CiteMark
              key={i}
              source={part.source}
              onOpen={onOpen}
              tight={followsPunctuation(parts[i - 1])}
            />
          ) : (
            <MarkedText key={i} text={part.text} mark={part.mark} />
          ),
        )}
        {streaming ? <span className="caret" aria-hidden="true" /> : null}
      </div>

      {failed && !text ? (
        <p className="answer-note">Couldn't write an answer just now.</p>
      ) : null}

      {/* An answer that names no moment leaves nothing below it, so say why
          rather than ending on a bare paragraph.
          Gated on `sources` too: when retrieval came back empty the backend
          writes the answer itself ("Nothing in your library covers that."), and
          there was never a moment for it to point at. Saying it didn't point at
          one would be a second, quieter way of repeating the sentence directly
          above — which the empty-results line below already repeats verbatim. */}
      {!streaming && !failed && text && sources.length && !cited.length ? (
        <p className="answer-note">
          The answer didn't point at any particular moment.
        </p>
      ) : null}

      {/* Held back until the answer settles: a citation list above a
          half-written answer is evidence arriving before the claim. */}
      {cited.length && !streaming ? (
        <div className="answer-sources">
          <p className="lbl">Sources</p>
          {/* The row is two controls side by side, not one containing the
              other: the channel name navigates to the channel, and a button
              inside a button is invalid markup that no browser agrees on. The
              moment button keeps the row's whole width minus what the channel
              takes, so the click target is unchanged for everything except the
              channel name itself. */}
          {rows.map((s) => {
            const channelId = channelOf.get(s.video_id);
            return (
              <div className="srcline" key={s.video_id}>
                {/* A row stands for the whole video, so it opens the video where
                    the viewer left it rather than seeking. It carries no
                    timestamp for the same reason: the row can cover several
                    cited moments, and one of them printed beside the title reads
                    like the only one. The numerals in the answer are what seek
                    to a particular moment. */}
                <button
                  type="button"
                  className="srcrow"
                  onClick={() => onOpenVideo(s.video_id)}
                >
                  <span className="n mono">{s.display}</span>
                  <span className="ttl">{s.title}</span>
                </button>
                {s.channel_name && channelId ? (
                  <button
                    type="button"
                    className="chan-link"
                    onClick={() => onOpenChannel(channelId)}
                  >
                    {s.channel_name}
                  </button>
                ) : s.channel_name ? (
                  <span className="ch">{s.channel_name}</span>
                ) : null}
              </div>
            );
          })}
        </div>
      ) : null}

      {/* How the answer was made. Settled only, and closed by default: the
          reader asked a question, not for a pipeline. It is the last thing in
          the panel because it is about the answer rather than part of it. */}
      {!streaming && state.trace?.length ? (
        <AnswerTrace stages={state.trace} />
      ) : null}
    </div>
  );
}

// STAGE_LABELS turns the backend's stable keys into the words a reader sees.
//
// THE WORDS LIVE HERE, and that is the whole reason the wire carries a key. They
// are copy: "fuse" was what the code called merging two result lists, it went on
// screen verbatim, and nobody outside the codebase could tell what it meant.
// Rewording a row must cost a frontend change and nothing more.
//
// Every label says what the step DID, in words a reader already has. The
// technical name is in the badge beside it, which is where someone who wants
// "embedding" or "sqlite-vec" will find it — so the label never has to carry
// vocabulary and the badge never has to be a sentence.
const STAGE_LABELS: Record<string, string> = {
  understand: "Worked out what you’re asking",
  channels: "Matched the channel you named",
  keyword: "Looked for those words",
  // Not "embedded" and not "vector": an embedding IS a list of numbers, so this
  // is accurate rather than simplified, and it works for a reader who has never
  // met the word. The one who has is reading the model name directly below it.
  embed: "Turned the question into numbers",
  vector: "Found passages that mean the same",
  // One row for fusing and choosing, because they are one idea: the two
  // searches came back, and this is what was kept out of them. It only reads
  // correctly with the two search rows above it, which is why they are separate
  // and this is not.
  merge: "Merged both lists, kept the best 12",
  count: "Counted the matching videos",
  answer: "Wrote the answer",
};

// stageLabel falls back to the raw key rather than hiding a stage it does not
// recognise. A backend that adds a step should show up as an ugly row, not as a
// silently missing one — the panel's whole claim is that it lists what ran.
function stageLabel(key: string): string {
  return STAGE_LABELS[key] ?? key;
}

// AnswerTrace is the disclosure and the waterfall inside it.
//
// The bars are drawn to scale across the whole run, which is the one thing the
// panel says that a list of numbers would not: the model calls are nearly all of
// the wait and the entire search is a sliver. That shape is the answer to "why
// does this take six seconds", and it is visible before any number is read.
function AnswerTrace({ stages }: { stages: TraceStage[] }) {
  const [open, setOpen] = useState(false);
  const total = stages.reduce((sum, s) => sum + s.ms, 0);
  const modelMs = stages
    .filter((s) => s.kind === "model")
    .reduce((sum, s) => sum + s.ms, 0);
  const models = stages.filter((s) => s.kind === "model").length;
  const local = stages.filter((s) => s.kind === "local").length;

  return (
    <div className="answer-trace">
      <button
        type="button"
        className="trace-toggle"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <Icon
          name="chevronRight"
          size="11px"
          style={{
            transition: "transform .15s",
            transform: open ? "rotate(90deg)" : "none",
          }}
        />
        How this was answered
      </button>
      {open ? (
        <div className="trace-body">
          <p className="trace-hd">
            {stages.length} steps,{" "}
            <span className="num">{formatMs(total)}</span> end to end.
            {models > 0 ? (
              <span className="trace-key">
                <Icon name="sparkles" size="11px" />
                {models} {models === 1 ? "call" : "calls"} to a model
              </span>
            ) : null}
            {local > 0 ? (
              <span className="trace-key local">
                <Icon name="database" size="11px" />
                {local} {local === 1 ? "query" : "queries"} of your library
              </span>
            ) : null}
          </p>
          {stages.map((s, i) => (
            <TraceRow
              key={`${s.key}-${i}`}
              stage={s}
              // Bars are positioned along the run, not left-aligned, so the
              // waterfall reads as elapsed time rather than as a bar chart of
              // unrelated magnitudes.
              offset={stages.slice(0, i).reduce((sum, p) => sum + p.ms, 0)}
              total={total}
            />
          ))}
          <p className="trace-foot">
            The model calls are nearly all of the wait:{" "}
            <span className="num">{formatMs(modelMs)}</span> of the{" "}
            <span className="num">{formatMs(total)}</span>, and the only steps
            that left this machine. Two searches run every time — one on the
            words you used, one on what they mean — and the results are merged
            before the model reads anything.
          </p>
        </div>
      ) : null}
    </div>
  );
}

// TraceRow is one step: what it did, what ran it, and what it cost.
//
// A step that called nothing renders NO badge and no second line. The blank
// says it; a row reading "no tool" would spend a line on something that did not
// happen, and the eye would have to read it to learn nothing.
function TraceRow({
  stage,
  offset,
  total,
}: {
  stage: TraceStage;
  offset: number;
  total: number;
}) {
  // A zero-length run would make every bar NaN% wide. It cannot happen with a
  // stage list that exists, but the arithmetic should not depend on that.
  const scale = total > 0 ? total : 1;
  return (
    <div className={`trace-row${stage.kind === "model" ? " model" : ""}`}>
      <span className="trace-name">
        {stageLabel(stage.key)}
        {stage.tool ? (
          <span className={`trace-by ${stage.kind}`}>
            <Icon
              name={stage.kind === "model" ? "sparkles" : "database"}
              size="11px"
            />
            {stage.tool}
          </span>
        ) : null}
      </span>
      <span className="trace-track" aria-hidden="true">
        <i
          style={{
            left: `${(offset / scale) * 100}%`,
            width: `${(stage.ms / scale) * 100}%`,
          }}
        />
      </span>
      <span className="trace-ms">{formatMs(stage.ms)}</span>
    </div>
  );
}

// MarkedText renders one run of prose under whatever markdown the model wrapped
// it in — see emphasis.ts, which drops the delimiters and leaves the mark. The
// element goes OUTSIDE the fade segments rather than around each one: a span
// keeps one element for its whole life that way, so the segments already on
// screen are reconciled untouched when the closing delimiter arrives and none
// of them re-runs its animation.
//
// A heading is block-level (.answer-lead). The body sets no `white-space`, so
// bolding it inline would only produce a run-on in bold — "…real stars.³
// Ontology basics The video…" — and the break is the whole point of a heading.
function MarkedText({ text, mark }: { text: string; mark?: EmphasisMark }) {
  const body = <FadedText text={text} />;
  switch (mark) {
    case "strong":
      return <strong>{body}</strong>;
    case "em":
      return <em>{body}</em>;
    case "code":
      return <code>{body}</code>;
    case "heading":
      return <strong className="answer-lead">{body}</strong>;
    default:
      return body;
  }
}

// FadedText splits prose into the clause-sized segments the CSS animates, so
// newly arrived text brightens into place instead of appearing abruptly. The
// segments are derived from the whole accumulated answer on every render, which
// is what lets React reconcile the unchanged ones and animate only the new.
function FadedText({ text }: { text: string }) {
  return (
    <>
      {splitIntoSegments(text).map((seg, i) => (
        <span key={i} className="ans-seg">
          {seg}
        </span>
      ))}
    </>
  );
}

// A mark that lands right after a comma or a full stop closes up against it —
// see the .tight rule in the stylesheet. The punctuation leaves its own visual
// gap, so the mark's usual one reads as a space the sentence did not ask for.
// Only these two: a colon or a semicolon is a heavier mark that still wants the
// room, and after a word the gap is what separates the numeral from the letters.
function followsPunctuation(before: RenderedPart | undefined): boolean {
  return before?.kind === "text" && /[.,]$/.test(before.text);
}

// CiteMark is an inline citation. It rests visible — a bordered superscript
// numeral, not something that appears on hover — and carries the moment it
// points at in its accessible name, since the numeral alone says nothing.
//
// Every number it renders is the DISPLAY number. The backend number must not
// reach any rendered string: a mark drawn as 2 whose label says "Source 4"
// sends a screen-reader user to a row that is not there.
function CiteMark({
  source,
  onOpen,
  tight,
}: {
  source: CitedSource;
  onOpen: (videoId: string, startSeconds: number) => void;
  tight: boolean;
}) {
  const at =
    source.kind === "summary"
      ? "the summary"
      : formatDuration(source.start_seconds);
  return (
    <button
      type="button"
      className={tight ? "cite tight" : "cite"}
      title={`${source.title} · ${at}`}
      aria-label={`Source ${source.display}: ${source.title} at ${at}`}
      onClick={() => onOpen(source.video_id, source.start_seconds)}
    >
      {source.display}
    </button>
  );
}
