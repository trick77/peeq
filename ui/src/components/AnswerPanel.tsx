import { Icon } from "../icons";
import { Spinner } from "../ui";
import { formatDuration } from "../format";
import { splitCitations } from "../citations";
import { citedInOrder, type CitedSource } from "../answerSources";
import { splitIntoSegments } from "../streamFade";
import type { AnswerSource } from "../api/answer";

// AnswerPanel renders the grounded answer above Ask's moments.
//
// Sources are the ones the answer CITED, numbered from 1 in the order it
// mentions them. Retrieval hands the model twelve passages; the ones it did not
// use are a working set, not findings, and listing them made the answer look
// like it was skipping numbered evidence ("[2] [4] [5]", no [1] in sight).
// See answerSources.ts — the same derivation feeds the moments below.

export type AnswerState = {
  status: "streaming" | "done";
  text: string;
  sources: AnswerSource[];
  failed?: boolean;
};

export function AnswerPanel({
  state,
  onOpen,
}: {
  state: AnswerState;
  onOpen: (videoId: string, startSeconds: number) => void;
}) {
  const { status, text, sources, failed } = state;
  // `known` is the FULL retrieved set, not the cited one: that is what keeps a
  // hallucinated [9] rendering as the characters the model produced rather than
  // as a citation pointing nowhere.
  const known = new Set(sources.map((s) => s.n));
  const cited = citedInOrder(text, sources);
  const display = new Map(cited.map((s) => [s.n, s]));
  const streaming = status === "streaming";

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
          ) : !streaming && cited.length ? (
            `${cited.length} source${cited.length === 1 ? "" : "s"}`
          ) : null}
        </span>
      </div>

      <div className="answer-body" aria-live="polite" aria-busy={streaming}>
        {splitCitations(text, known).map((part, i) =>
          part.kind === "cite" ? (
            <CiteMark key={i} source={display.get(part.n)!} onOpen={onOpen} />
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
          rather than ending on a bare paragraph. */}
      {!streaming && !failed && text && !cited.length ? (
        <p className="answer-note">
          The answer didn't point at any particular moment.
        </p>
      ) : null}

      {/* Held back until the answer settles: a citation list above a
          half-written answer is evidence arriving before the claim. */}
      {cited.length && !streaming ? (
        <div className="answer-sources">
          <p className="lbl">Sources</p>
          {cited.map((s) => (
            <button
              key={s.n}
              type="button"
              className="srcrow"
              onClick={() => onOpen(s.video_id, s.start_seconds)}
            >
              <span className="n mono">{s.display}</span>
              <span className="ts mono">
                {s.kind === "summary" ? "—" : formatDuration(s.start_seconds)}
              </span>
              <span className="ttl">{s.title}</span>
              {s.channel_name ? (
                <span className="ch">{s.channel_name}</span>
              ) : null}
            </button>
          ))}
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
