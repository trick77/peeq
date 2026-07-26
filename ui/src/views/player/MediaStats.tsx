import type { Video } from "../../api/types";
import {
  codecLabel,
  formatDuration,
  formatSize,
  resolutionLabel,
} from "../../format";

// MediaStats is the labelled strip of file facts under the action row: what
// this video is, as opposed to what it is about.
//
// It replaced a pill showing format_used — the resolved yt-dlp -f selector,
// which rendered as
// "bestvideo[height<=1080][vcodec*=avc1]+bestaudio[acodec*=mp4a]/mp4". That
// string describes the request, not the result, and is identical for every
// video downloaded under one preset. These values come from ffprobe reading
// the actual file.
//
// Every column is dropped when its value is missing, and the whole strip
// disappears when nothing is left, so a video the probe has not reached yet
// (or could not read) shows no empty scaffolding. Length and Size come from
// the download itself and are therefore almost always present; the other
// three arrive only once the file has been probed.
export function MediaStats({ video }: { video: Video }) {
  const stats: Array<{ k: string; v: string }> = [
    // formatDuration renders "--:--" for an unknown length, which is the
    // right answer under a scrubber and the wrong one here: a labelled stat
    // reading "--:--" claims the figure exists and is unreadable. Guard so
    // the column drops out instead.
    {
      k: "Length",
      v: video.duration_seconds ? formatDuration(video.duration_seconds) : "",
    },
    { k: "Size", v: formatSize(video.filesize_bytes) },
    { k: "Format", v: video.media_container?.toUpperCase() ?? "" },
    {
      k: "Video",
      // One column, because "1080p H.264" is how a person says it — two
      // separate cells would imply the resolution and the codec are
      // independently interesting, and they are not.
      v: [resolutionLabel(video.video_height), codecLabel(video.video_codec)]
        .filter(Boolean)
        .join(" "),
    },
    { k: "Audio", v: codecLabel(video.audio_codec) },
  ].filter((s) => s.v !== "");

  if (stats.length === 0) return null;
  return (
    <dl className="playstats">
      {stats.map((s) => (
        <div className="stat" key={s.k}>
          <dt>{s.k}</dt>
          <dd>{s.v}</dd>
        </div>
      ))}
    </dl>
  );
}
