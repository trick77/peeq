import { Icon } from "../icons";
import { Spinner } from "../ui";
import { formatDuration } from "../format";
import {
  answerParts,
  citedInOrder,
  type CitedSource,
  type RenderedPart,
} from "../answerSources";
import { splitIntoSegments } from "../streamFade";
import type { AnswerSource, AnswerVideo, LibraryCount } from "../api/answer";

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
  // the ones the answer went on to cite. The cited set is subtracted here to get
  // "Also in your library"; it cannot be subtracted server-side, because the
  // frame carrying this is sent before the model has written anything.
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

  // What retrieval found and the answer did not use. `seen` is the cited videos,
  // so subtracting it is the whole derivation — and it has to happen here because
  // the coverage frame goes out before generation, when nothing yet knows what
  // will be cited. A video sent to the model and then left uncited belongs in this
  // list, which is exactly what a server-side subtraction would have lost.
  const alsoRows = (state.coverage ?? []).filter((v) => !seen.has(v.id));

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
            <FadedText key={i} text={part.text} />
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

          {/* Retrieved and not cited. Held to the same settled-answer gate as the
              citations above, and rendered as a real list rather than as rows
              that lost their numbers: these carry no numeral because no numeral
              means anything for them — the numbers say "cited as [n]", and
              inventing one would claim the answer leaned on a video it never
              mentioned.

              Each row opens its video without seeking, like a cited row. There IS
              a retrieved chunk behind each one, so seeking would be possible, but
              the model never vouched for that chunk — jumping to a timestamp on
              the strength of a distance score asserts more than is known. */}
          {alsoRows.length ? (
            <div className="also">
              <p className="lbl">Also in your library ({alsoRows.length})</p>
              <ul className="also-list">
                {alsoRows.map((v) => (
                  /* The li KEEPS srcline: the channel rules are direct-child
                     selectors (.srcline > .ch), so a row that merely looked like
                     one would lose the channel styling entirely. */
                  <li className="srcline" key={v.id}>
                    <button
                      type="button"
                      className="srcrow"
                      onClick={() => onOpenVideo(v.id)}
                    >
                      <span className="ttl">{v.title}</span>
                    </button>
                    {v.channel_name && v.channel_id ? (
                      <button
                        type="button"
                        className="chan-link"
                        onClick={() => onOpenChannel(v.channel_id)}
                      >
                        {v.channel_name}
                      </button>
                    ) : v.channel_name ? (
                      <span className="ch">{v.channel_name}</span>
                    ) : null}
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
        </div>
      ) : null}
    </div>
  );
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
