import { Icon } from "../icons";
import { thumbnailUrl } from "../api/videos";
import type { Video } from "../api/types";
import { daysSince, formatDuration, gradientClassFor } from "../format";

export type DownloadProgress = { percent: number; eta?: string };

// VideoCard — one grid tile, per the mockup's `.card`/`.thumb`/`.life`
// blocks. Pure presentational: all data comes in as props, all mutation
// (favorite/watched/open) goes out via callbacks so Library owns the
// actual API calls and can update its list optimistically.
export function VideoCard({
  video,
  retentionDays,
  progress,
  onOpen,
  onToggleFavorite,
  onToggleWatched,
}: {
  video: Video;
  /** settings.retention_days — needed to compute "Expires in N days". */
  retentionDays: number;
  /** Live SSE progress for this video's download job, if it's downloading. */
  progress?: DownloadProgress;
  onOpen: (id: string) => void;
  onToggleFavorite: (id: string) => void;
  onToggleWatched: (id: string) => void;
}) {
  const downloading = video.status === "queued" || video.status === "downloading";
  const isNew = !downloading && !video.watched && video.resume_position_seconds === 0 && video.has_media;
  const resuming =
    !downloading && !video.watched && video.resume_position_seconds > 0 && (video.duration_seconds ?? 0) > 0;
  const resumePercent = resuming
    ? Math.min(100, Math.round((video.resume_position_seconds / (video.duration_seconds ?? 1)) * 100))
    : 0;

  return (
    <article className="card">
      <button
        type="button"
        className="thumb"
        style={{ display: "block", width: "100%", padding: 0, border: "none", background: "none", cursor: "pointer" }}
        onClick={() => onOpen(video.id)}
        aria-label={`Open ${video.title}`}
      >
        {video.has_thumbnail ? (
          <img className="fill" src={thumbnailUrl(video.id)} alt="" loading="lazy" />
        ) : (
          <div className={`fill ${gradientClassFor(video.id)}`} />
        )}
        {isNew ? <span className="tag new">NEW</span> : null}
        <div className="acts">
          <span
            role="button"
            tabIndex={0}
            className={`iconbtn${video.favorite ? " on" : ""}`}
            aria-label={video.favorite ? "Remove from favorites" : "Add to favorites"}
            onClick={(e) => {
              e.stopPropagation();
              onToggleFavorite(video.id);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.stopPropagation();
                onToggleFavorite(video.id);
              }
            }}
          >
            <Icon name={video.favorite ? "starFilled" : "star"} size="16px" />
          </span>
          <span
            role="button"
            tabIndex={0}
            className={`iconbtn${video.watched ? " on" : ""}`}
            aria-label={video.watched ? "Mark unwatched" : "Mark watched"}
            onClick={(e) => {
              e.stopPropagation();
              onToggleWatched(video.id);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.stopPropagation();
                onToggleWatched(video.id);
              }
            }}
          >
            <Icon name="check" size="16px" />
          </span>
        </div>
        <span className="dur">{formatDuration(video.duration_seconds)}</span>
        {resuming ? (
          <div className="resume">
            <i style={{ width: `${resumePercent}%` }} />
          </div>
        ) : null}
        {downloading ? (
          <div className="dl">
            <div
              className="ring"
              data-p={`${Math.round(progress?.percent ?? 0)}%`}
              style={{ ["--p" as string]: `${progress?.percent ?? 0}%` }}
            />
            {progress?.eta ? <small>{progress.eta} left</small> : null}
          </div>
        ) : null}
      </button>

      <h3>{video.title}</h3>
      <div className="by">
        {video.channel_name || video.channel_id}
        {video.published_at ? (
          <>
            <span className="dot">·</span>
            {new Date(video.published_at).toLocaleDateString()}
          </>
        ) : null}
      </div>
      <Lifecycle video={video} retentionDays={retentionDays} progress={progress} downloading={downloading} />
    </article>
  );
}

function Lifecycle({
  video,
  retentionDays,
  progress,
  downloading,
}: {
  video: Video;
  retentionDays: number;
  progress?: DownloadProgress;
  downloading: boolean;
}) {
  if (downloading) {
    return (
      <div className="life fresh">
        Downloading…
        <span className="sz">{Math.round(progress?.percent ?? 0)}%</span>
      </div>
    );
  }
  if (video.favorite) {
    return (
      <div className="life kept">
        <span className="k">
          <Icon name="starFilled" size="13px" />
          Kept forever
        </span>
        {video.filesize_bytes ? <span className="sz">{formatSize(video.filesize_bytes)}</span> : null}
      </div>
    );
  }
  if (video.watched) {
    const expiresIn = Math.max(0, retentionDays - daysSince(video.watched_at));
    return (
      <div className="life expiring">
        {expiresIn === 0 ? "Expires soon" : `Expires in ${expiresIn} day${expiresIn === 1 ? "" : "s"}`}
        <span className="decay" />
      </div>
    );
  }
  return (
    <div className="life fresh">
      Not watched yet
      {video.filesize_bytes ? <span className="sz">{formatSize(video.filesize_bytes)}</span> : null}
    </div>
  );
}

function formatSize(bytes: number): string {
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(0)} MB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${bytes} B`;
}
