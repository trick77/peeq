import { Icon } from "../../icons";
import type { VideoEmbeddings } from "../../api/types";

// KIND_LABELS names each chunk kind the pipeline writes (rag/build.go's
// KindTranscript/KindSummary/KindChapter). A kind with no entry here is shown
// under its own wire word rather than dropped: a row saying "keyframe 12" is
// odd-looking but true, and silently omitting it would make the rows stop
// adding up to the header's total.
const KIND_LABELS: Record<string, string> = {
  transcript: "Transcript",
  chapter: "Chapters",
  summary: "Summary",
};

function kindLabel(kind: string): string {
  return KIND_LABELS[kind] ?? kind;
}

// IndexCard reports what search actually holds for this video: how the
// transcript was cut up, how much text that came to, and which model turned it
// into vectors.
//
// It sits under the Summary in the sidebar because it answers the question that
// card raises — the summary says what the video is about, this says how much of
// it a question can reach. Presentational: the Player fetches, so a failed
// request renders nothing at all rather than an apology in the rail.
export function IndexCard({ stats }: { stats: VideoEmbeddings }) {
  return (
    /* indexpanel is a placement hook like summarypanel: in one column the
       panels are ordered individually (see .playgrid's media query). */
    <div className="card indexpanel">
      <div className="hd hd-wrap">
        <Icon name="search" size="16px" />
        <span className="lbl">Search index</span>
        {stats.chunks > 0 && (
          <span className="meta">
            {stats.chunks} {stats.chunks === 1 ? "chunk" : "chunks"}
          </span>
        )}
      </div>
      <div className="tabbody">
        {stats.chunks === 0 ? (
          <p className="placeholder">Nothing indexed yet.</p>
        ) : (
          <dl className="idxstats">
            {stats.kinds.map((k) => (
              <div className="row" key={k.kind}>
                <dt>{kindLabel(k.kind)}</dt>
                <dd>{k.count}</dd>
              </div>
            ))}
            <div className="row">
              <dt>Text indexed</dt>
              <dd>{stats.tokens.toLocaleString()} tokens</dd>
            </div>
            {/* The model wrote the vectors that are stored, so it is reported
                even when the server has since been pointed at another one —
                which is exactly when knowing this is worth anything. */}
            {stats.model && (
              <div className="row">
                <dt>Model</dt>
                <dd>
                  {stats.model}
                  {!!stats.dimensions && (
                    <span className="sub">{stats.dimensions} dimensions</span>
                  )}
                </dd>
              </div>
            )}
          </dl>
        )}
      </div>
    </div>
  );
}
