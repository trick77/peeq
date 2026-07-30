import { Icon } from "../icons";
import { Spinner } from "../ui";
import { formatDuration } from "../format";
import { splitCitations } from "../citations";
import { splitIntoSegments } from "../streamFade";
import type { AnswerSource } from "../api/answer";

// AnswerPanel renders the grounded answer above Ask's results.
//
// It never blocks the results: the parent fires retrieval and generation
// together, so the ranked moments paint as soon as they arrive and this fills
// in above them. If the answer fails the panel keeps whatever text it has —
// truncated is more use than blank.

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
  const known = new Set(sources.map((s) => s.n));
  const byNumber = new Map(sources.map((s) => [s.n, s]));
  const streaming = status === "streaming";

  // Nothing to show at all — the parent renders the plain results alone rather
  // than an empty box.
  if (!text && !sources.length && !streaming) return null;

  return (
    <div className="answer">
      <div className="hd">
        <Icon name="sparkles" size="14px" />
        Answer
        <span className="status">
          {streaming ? (
            <>
              <Spinner size="12px" />
              Reading your library
            </>
          ) : sources.length ? (
            `${sources.length} source${sources.length === 1 ? "" : "s"}`
          ) : null}
        </span>
      </div>

      <div className="answer-body" aria-live="polite" aria-busy={streaming}>
        {splitCitations(text, known).map((part, i) =>
          part.kind === "cite" ? (
            <CiteMark key={i} source={byNumber.get(part.n)!} onOpen={onOpen} />
          ) : (
            <FadedText key={i} text={part.text} />
          ),
        )}
        {streaming ? <span className="caret" aria-hidden="true" /> : null}
      </div>

      {failed && !text ? (
        <p className="answer-note">
          Couldn't write an answer just now — the moments below are still good.
        </p>
      ) : null}

      {sources.length ? (
        <div className="answer-sources">
          <p className="lbl">Sources</p>
          {sources.map((s) => (
            <button
              key={s.n}
              type="button"
              className="srcrow"
              onClick={() => onOpen(s.video_id, s.start_seconds)}
            >
              <span className="n mono">{s.n}</span>
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
function CiteMark({
  source,
  onOpen,
}: {
  source: AnswerSource;
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
      aria-label={`Source ${source.n}: ${source.title} at ${at}`}
      onClick={() => onOpen(source.video_id, source.start_seconds)}
    >
      {source.n}
    </button>
  );
}
