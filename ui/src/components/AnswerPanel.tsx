import { Icon } from "../icons";
import { Spinner } from "../ui";
import { formatDuration } from "../format";
import { answerParts, citedInOrder, type CitedSource } from "../answerSources";
import { splitIntoSegments } from "../streamFade";
import type { AnswerSource, AnswerVideo } from "../api/answer";

// AnswerPanel renders the grounded answer above Ask's moments.
//
// Sources are the videos the answer CITED, numbered from 1 in the order it
// mentions them — one row per video, not per passage. Retrieval hands the model
// twelve passages; the ones it did not use are a working set, not findings, and
// listing them made the answer look like it was skipping numbered evidence
// ("[2] [4] [5]", no [1] in sight).
// See answerSources.ts — the same derivation feeds the moments below.

export type AnswerState = {
  status: "streaming" | "done";
  text: string;
  sources: AnswerSource[];
  // The videos frame of the same stream. A source carries channel_name but no
  // channel_id, and the channel name in a source row has to navigate somewhere —
  // so the id is looked up here by video_id rather than widened onto every
  // source server-side.
  videos?: AnswerVideo[];
  failed?: boolean;
};

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
  const { status, text, sources, videos, failed } = state;
  const channelOf = new Map(
    (videos ?? []).map((v) => [v.id, v.channel_id] as const),
  );
  // The body's parts come from answerSources too, so the marks above and the
  // rows below cannot disagree about what a numeral means. It is what resolves
  // each mark to its source and collapses a run of adjacent same-numeral marks.
  const streaming = status === "streaming";
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
        <span className="status">
          {/* The spinner belongs to the wait before the first token. Once words
              are arriving they say the same thing better, and the caret below
              carries "still going". */}
          {streaming && !text ? (
            <>
              <Spinner size="12px" />
              Reading your library
            </>
          ) : !streaming && rows.length ? (
            `${rows.length} source${rows.length === 1 ? "" : "s"}`
          ) : null}
        </span>
      </div>

      <div className="answer-body" aria-live="polite" aria-busy={streaming}>
        {parts.map((part, i) =>
          part.kind === "cite" ? (
            <CiteMark key={i} source={part.source} onOpen={onOpen} />
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
}: {
  source: CitedSource;
  onOpen: (videoId: string, startSeconds: number) => void;
}) {
  const at =
    source.kind === "summary"
      ? "the summary"
      : formatDuration(source.start_seconds);
  return (
    <button
      type="button"
      className="cite"
      title={`${source.title} · ${at}`}
      aria-label={`Source ${source.display}: ${source.title} at ${at}`}
      onClick={() => onOpen(source.video_id, source.start_seconds)}
    >
      {source.display}
    </button>
  );
}
