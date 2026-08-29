import { Icon } from "../../icons";
import { Spinner } from "../../ui";
import { formatDuration } from "../../format";
import { seekOnClick } from "../../selection";
import type { Video } from "../../api/types";

// DONE_STATUSES is every summary_status the panel below renders explicitly.
// Anything outside it falls through to "No summary yet." — so a status added
// on the backend shows a truthful placeholder rather than an empty card.
const DONE_STATUSES = new Set([
  "done",
  "no_transcript",
  "pending",
  "running",
  "error",
]);

// SummaryCard renders the prose summary and every non-done state it can be in.
export function SummaryCard({ video }: { video: Video }) {
  return (
    /* summarypanel is a placement hook, not a style: in one column the four
       panels are ordered individually (see .playgrid's media query), and both
       this page and the share page name them the same way. */
    <div className="card summarypanel">
      <div className="hd">
        <Icon name="alignLeft" size="16px" />
        <span className="lbl">Summary</span>
      </div>
      <div className="tabbody summ">
        {video.summary_status === "done" &&
          (video.summary.trim() ? (
            video.summary
              .split("\n\n")
              .filter((p) => p.trim())
              .map((p, i) => <p key={i}>{p}</p>)
          ) : (
            <p className="placeholder">No summary text.</p>
          ))}
        {/* no_transcript covers both "there are no captions" and "the
            captions turned out to be music/ambience rather than speech",
            so the copy has to fit both. */}
        {video.summary_status === "no_transcript" && (
          <p className="placeholder">No speech in this video.</p>
        )}
        {/* Split on has_subtitles: 'pending' with no transcript means no
            summary job exists at all — the video is waiting on YouTube's ASR
            pass, and the next caption attempt can be a day out. No spinner
            there, because a spinner re-asserts the "work in flight" claim the
            wording just removed. */}
        {(video.summary_status === "pending" ||
          video.summary_status === "running") &&
          (video.has_subtitles ? (
            <p
              className="placeholder"
              style={{ display: "flex", alignItems: "center", gap: 8 }}
            >
              <Spinner size="15px" />
              Summarizing
            </p>
          ) : (
            <p className="placeholder">Waiting for captions</p>
          ))}
        {video.summary_status === "error" && (
          <p className="errline">Summarization failed.</p>
        )}
        {/* The summary is finished and printed above, but the indexing step
            behind it did not get through. Said here rather than as a summary
            failure, which is what this used to report: the text is right there
            on the page, so calling it failed reads as a bug in the page. */}
        {video.summary_status === "done" && !video.indexed && (
          <p className="placeholder">Not searchable yet.</p>
        )}
        {!DONE_STATUSES.has(video.summary_status) && (
          <p className="placeholder">No summary yet.</p>
        )}
      </div>
    </div>
  );
}

// HighlightsCard is the key-points list. Each row seeks, so it is the same
// contract as ContentsCard: presentational, with the Player's seek passed in —
// and an omitted seek meaning there is no <video> to move, which renders the
// rows as plain text instead of buttons that could only do nothing.
export function HighlightsCard({
  video,
  seek,
}: {
  video: Video;
  seek?: (seconds: number) => void;
}) {
  return (
    <div className="card hlpanel">
      <div className="hd">
        <Icon name="star" size="16px" />
        <span className="lbl">Highlights</span>
      </div>
      <div className="tabbody">
        {video.key_points.length === 0 ? (
          <p className="placeholder">No highlights.</p>
        ) : (
          <div className="hl">
            {video.key_points.map((k, i) => {
              // No per-row star: it was identical on all ten rows and said
              // only what the header's star says once, while costing every
              // row 26px of the width its sentence wanted. The gold survives
              // in the timestamp, which is the row's own gutter.
              const body = (
                <>
                  <span className="ts mono">{formatDuration(k.ts)}</span>
                  <span className="txt">{k.text}</span>
                </>
              );
              return seek ? (
                <button
                  key={i}
                  type="button"
                  className="row"
                  onClick={seekOnClick(seek, k.ts)}
                >
                  {body}
                </button>
              ) : (
                <div key={i} className="row inert">
                  {body}
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
