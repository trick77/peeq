import { Icon } from "../../icons";
import { formatDuration } from "../../format";
import type { Video } from "../../api/types";

// ContentsCard is the chapter list under the stage. Chapters come from two
// places and the card says which: yt-dlp reads them off the video itself, MiMo
// derives them from the transcript when YouTube supplied none, and a reader
// deciding whether to trust a timestamp wants to know the difference.
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
  return (
    <div className="card full">
      <div className="hd">
        <Icon name="listTree" size="16px" />
        <span className="lbl">Contents</span>
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
                {c.source === "yt-dlp" && <span className="src">yt-dlp</span>}
                {c.source === "mimo" && <span className="src">MiMo</span>}
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
