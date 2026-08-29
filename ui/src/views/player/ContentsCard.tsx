import { Icon } from "../../icons";
import { formatDuration } from "../../format";
import { seekOnClick } from "../../selection";
import { tocGridStyle } from "../../ui";
import type { Video } from "../../api/types";

// Chapters come from two places and the card says which: yt-dlp reads them off
// the video itself, the model derives them from the transcript when YouTube
// supplied none, and a reader deciding whether to trust a timestamp wants to
// know the difference. A video's chapters are written as a set, so the answer is
// the same for every row — the header says it once rather than tagging all
// twelve.
//
// The label names the KIND of generator, not the vendor: which model peeq runs
// has changed once and can change again, and a pill saying "MiMo" on chapters a
// different model wrote is worse than one saying nothing. "mimo" is the legacy
// stored value, kept so videos summarized before the switch keep their pill
// instead of silently losing it.
const SOURCE_LABELS: Record<string, string> = {
  "yt-dlp": "yt-dlp",
  llm: "LLM",
  mimo: "LLM",
};

// sourceLabels names each generator behind these chapters, in the order it
// first appears. Normally one; two only if a video ever mixes them, which
// nothing in the pipeline does today but nothing forbids either. An
// unrecognised source names no generator, so it contributes no pill.
function sourceLabels(chapters: Video["chapters"]): string[] {
  const seen: string[] = [];
  for (const c of chapters) {
    const label = SOURCE_LABELS[c.source];
    if (label && !seen.includes(label)) seen.push(label);
  }
  return seen;
}

// ContentsCard is the chapter list under the stage.
//
// Presentational: it owns no state and reaches nothing. seek is the Player's,
// so clicking a row moves the same <video> element the scrubber does — and
// omitting seek is how a caller says there is no such element (a tombstoned
// video, whose chapters are still worth reading). The rows then render as plain
// text rather than buttons that could only do nothing: no focus stop, no hover
// affordance, no pointer. The markup is otherwise identical, since every rule
// involved is element-agnostic.
export function ContentsCard({
  video,
  seek,
}: {
  video: Video;
  seek?: (seconds: number) => void;
}) {
  const sources = sourceLabels(video.chapters);
  return (
    <div className="card full contentspanel">
      <div className="hd hd-wrap">
        <Icon name="listTree" size="16px" />
        <span className="lbl">Contents</span>
        {sources.map((s) => (
          <span key={s} className="src-pill">
            {s}
          </span>
        ))}
        {video.chapters.length > 0 && (
          <span className="meta">{video.chapters.length} chapters</span>
        )}
      </div>
      <div className="tabbody">
        {video.chapters.length === 0 ? (
          <p className="placeholder">No chapters.</p>
        ) : (
          <div
            className="toc toc-grid"
            style={tocGridStyle(video.chapters.length)}
          >
            {video.chapters.map((c, i) => {
              const body = (
                <>
                  <span className="ts mono">{formatDuration(c.ts)}</span>
                  <span>
                    <span className="ttl">{c.title}</span>
                  </span>
                </>
              );
              return seek ? (
                <button
                  key={i}
                  type="button"
                  className="row"
                  onClick={seekOnClick(seek, c.ts)}
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
