import type { MouseEvent } from "react";
import { Icon } from "../icons";
import { Button } from "../ui";
import { thumbnailUrl } from "../api/videos";
import type { Video } from "../api/types";
import {
  daysSince,
  formatAge,
  formatAgo,
  formatDuration,
  gradientClassFor,
} from "../format";
import { CATEGORY_BY_ID, UNCATEGORIZED } from "../categories";

// VideoCard — one grid tile, per the mockup's `.card`/`.thumb`/`.life`
// blocks. Pure presentational: all data comes in as props, all mutation
// (favorite/watched/open) goes out via callbacks so Library owns the
// actual API calls and can update its list optimistically.
export function VideoCard({
  video,
  retentionDays,
  onOpen,
  onToggleFavorite,
  onToggleWatched,
  onRedownload,
  onOpenChannel,
}: {
  video: Video;
  /** settings.retention_days — needed to compute "Expires in N days". */
  retentionDays: number;
  onOpen: (id: string) => void;
  onToggleFavorite: (id: string) => void;
  onToggleWatched: (id: string) => void;
  /** Re-queues a failed or tombstoned video's download; Library owns the actual call. */
  onRedownload?: (id: string) => void;
  // onOpenChannel — optional: wired by App (Task 11), rendered as a channel
  // name link in Task 15.
  onOpenChannel?: (id: string) => void;
}) {
  // No "downloading" branch any more: the library list excludes in-flight
  // rows entirely (see videos.Store.List), so a card is never rendered for a
  // video that is still being fetched. Progress lives in the rail's status
  // panel, which is on screen wherever you are.
  const isNew =
    !video.watched && video.resume_position_seconds === 0 && video.has_media;
  const resuming =
    !video.watched &&
    video.resume_position_seconds > 0 &&
    (video.duration_seconds ?? 0) > 0;
  const resumePercent = resuming
    ? Math.min(
        100,
        Math.round(
          (video.resume_position_seconds / (video.duration_seconds ?? 1)) * 100,
        ),
      )
    : 0;

  // The card is one big open-the-video target: the thumbnail and title
  // buttons only cover part of it, leaving the eyebrow, the lifecycle row and
  // the gaps between them dead space under a pointer cursor. Two exceptions —
  // a click that landed on a real control belongs to that control (and the
  // title/thumbnail buttons must not open twice by bubbling here), and a click
  // that ends a text selection is a selection, not a tap.
  function handleCardClick(e: MouseEvent<HTMLElement>) {
    if ((e.target as HTMLElement).closest('button, a, [role="button"]')) return;
    if (window.getSelection()?.toString()) return;
    onOpen(video.id);
  }

  return (
    <article className="card video-card" onClick={handleCardClick}>
      <div className="thumb">
        <button
          type="button"
          className="thumb-btn"
          style={{
            position: "absolute",
            inset: 0,
            display: "block",
            width: "100%",
            height: "100%",
            padding: 0,
            border: "none",
            background: "none",
            cursor: "pointer",
          }}
          onClick={() => onOpen(video.id)}
          aria-label={`Open ${video.title}`}
        >
          {video.has_thumbnail ? (
            <img
              className="fill"
              src={thumbnailUrl(video.id)}
              alt=""
              loading="lazy"
            />
          ) : (
            <div className={`fill ${gradientClassFor(video.id)}`} />
          )}
          {isNew ? <span className="tag new">Unwatched</span> : null}
          <span className="dur">{formatDuration(video.duration_seconds)}</span>
          {resuming ? (
            <div className="resume">
              <i style={{ width: `${resumePercent}%` }} />
            </div>
          ) : null}
        </button>

        {/* Siblings of the thumbnail button (not nested inside it) — a
            <button> containing role="button" spans is invalid
            interactive-in-interactive markup. Real <button>s, absolutely
            positioned within the same .thumb wrapper, keep the exact same
            look while staying valid. */}
        <div className="acts">
          <button
            type="button"
            className={`iconbtn${video.favorite ? " on" : ""}`}
            aria-label={
              video.favorite ? "Remove from favorites" : "Add to favorites"
            }
            onClick={(e) => {
              e.stopPropagation();
              onToggleFavorite(video.id);
            }}
          >
            <Icon name={video.favorite ? "starFilled" : "star"} size="16px" />
          </button>
          <button
            type="button"
            className={`iconbtn${video.watched ? " on" : ""}`}
            aria-label={video.watched ? "Mark unwatched" : "Mark watched"}
            onClick={(e) => {
              e.stopPropagation();
              onToggleWatched(video.id);
            }}
          >
            <Icon name="check" size="16px" />
          </button>
        </div>
      </div>

      <div className="by">
        {onOpenChannel && video.channel_id ? (
          <button
            type="button"
            className="chan-link"
            onClick={() => onOpenChannel(video.channel_id)}
          >
            {video.channel_name || video.channel_id}
          </button>
        ) : (
          <span className="chan-name">
            {video.channel_name || video.channel_id}
          </span>
        )}
        {/* Added date first, because it is what the grid's default order ranks
            by — an eyebrow leading with the air date would make that order
            look broken. The two ages use different helpers on purpose: the
            primary gets formatAgo's full words, the secondary formatAge's
            abbreviations, so three parts still fit the narrowest column. */}
        {video.downloaded_at ? (
          <>
            <span className="dot">·</span>
            added {formatAgo(video.downloaded_at)}
          </>
        ) : null}
        {video.published_at ? (
          <>
            <span className="dot">·</span>
            aired {formatAge(video.published_at)}
          </>
        ) : null}
      </div>
      <h3>
        <button
          type="button"
          className="title-btn"
          onClick={() => onOpen(video.id)}
        >
          {video.title}
        </button>
      </h3>
      <Lifecycle
        video={video}
        retentionDays={retentionDays}
        onRedownload={onRedownload}
      />
    </article>
  );
}

function Lifecycle({
  video,
  retentionDays,
  onRedownload,
}: {
  video: Video;
  retentionDays: number;
  onRedownload?: (id: string) => void;
}) {
  if (video.status === "error" || video.status === "tombstoned") {
    return (
      <div className="card-foot">
        <div className={`life ${video.status === "error" ? "err" : "tomb"}`}>
          <span className="led" />
          {video.status === "error"
            ? "Download failed"
            : "Removed to save space · summary kept"}
        </div>
        {onRedownload && (
          <Button
            type="button"
            variant="tinted"
            small
            onClick={() => onRedownload(video.id)}
          >
            <Icon name="refresh" size="15px" /> Re-download
          </Button>
        )}
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
      </div>
    );
  }
  if (video.watched) {
    const expiresIn = Math.max(0, retentionDays - daysSince(video.watched_at));
    return (
      <div className="life expiring">
        {expiresIn === 0
          ? "Expires soon"
          : `Expires in ${expiresIn} day${expiresIn === 1 ? "" : "s"}`}
        <span className="decay" />
      </div>
    );
  }
  // Nothing to say about a fresh video's lifecycle that the thumbnail does
  // not already say (NEW tag, resume bar), so the row carries the category
  // pill instead. With no category there is nothing left to render at all —
  // an empty .life would still eat a 10px `.card` flex gap, hence null.
  const category = categoryMeta(video.category);
  if (!category) return null;
  return (
    <div className="life fresh">
      <span className="metapill">
        <span className="dotc" style={{ background: category.color }} />
        {category.label}
      </span>
    </div>
  );
}

// categoryMeta resolves the classifier's label/color for a video, or null
// when it is uncategorized or carries a category this build does not know.
function categoryMeta(category: string) {
  if (!category || category === UNCATEGORIZED) return null;
  return CATEGORY_BY_ID[category] ?? null;
}
