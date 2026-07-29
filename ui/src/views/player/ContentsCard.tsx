import { Icon } from "../../icons";
import { formatDuration } from "../../format";
import type { Video } from "../../api/types";

// Chapters come from two places and the card says which: yt-dlp reads them off
// the video itself, MiMo derives them from the transcript when YouTube supplied
// none, and a reader deciding whether to trust a timestamp wants to know the
// difference. A video's chapters are written as a set, so the answer is the
// same for every row — the header says it once rather than tagging all twelve.
const SOURCE_LABELS: Record<string, string> = {
  "yt-dlp": "yt-dlp",
  mimo: "MiMo",
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
// so clicking a row moves the same <video> element the scrubber does.
export function ContentsCard({
  video,
  seek,
}: {
  video: Video;
  seek: (seconds: number) => void;
}) {
  const sources = sourceLabels(video.chapters);
  return (
    <div className="card full">
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
          <div className="toc toc-grid">
            {video.chapters.map((c, i) => (
              <button
                key={i}
                type="button"
                className="row"
                onClick={() => seek(c.ts)}
              >
                <span className="ts mono">{formatDuration(c.ts)}</span>
                <span>
                  <span className="ttl">{c.title}</span>
                </span>
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
