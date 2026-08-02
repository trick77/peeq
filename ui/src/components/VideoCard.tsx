import type { MouseEvent } from "react";
import { Icon } from "../icons";
import { Button } from "../ui";
import { ThumbFill } from "./ThumbFill";
import { ChannelLink } from "./ChannelLink";
import type { Video } from "../api/types";
import {
  daysSince,
  formatAgo,
  formatDuration,
  shortWatchLink,
  videoLabel,
  watchURL,
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
    !video.watched &&
    video.resume_position_seconds === 0 &&
    // A tombstoned video counts: watched-ness and whether the file is still
    // here are two different facts, and hiding the dot on a swept video made an
    // unwatched one indistinguishable from a watched one. Videos still in the
    // pipeline (new/queued/downloading) stay dotless — there is nothing to have
    // watched yet.
    (video.has_media || video.status === "tombstoned");
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

  // The category pill used to own a row of its own under the title, which left
  // a band of dead space on every card whose title ran short of two lines. It
  // now sits in the thumbnail's empty bottom-left corner, opposite the runtime.
  // The text rows were the wrong home for it: the eyebrow already carries three
  // items, and at the narrowest card peeq draws (228px, two columns with the
  // rail still open) a fourth one truncates the channel name to a stub. The
  // thumbnail corner is the same width on every card, so nothing competes.
  //
  // The condition is otherwise unchanged from when Lifecycle rendered it: a
  // failed, kept or watched video shows no category, because for those the
  // lifecycle row still has something to say. Tombstoned is NOT in that list:
  // losing the file costs a video none of its facts, so a swept video shows its
  // category on exactly the terms every other video does.
  const thumbCategory =
    video.status !== "error" && !video.favorite && !video.watched
      ? categoryMeta(video.category)
      : null;

  // A video added by URL is enqueued before its metadata is known, so its card
  // carries no title until the download worker's preflight resolves one. The
  // title slot says what is happening — the same wording Up next uses, from the
  // same helper — and the id moves down to the byline as a link to YouTube. On
  // a card whose download ended in an error no title is coming at all, so the
  // placeholder there stops implying that one is on its way.
  const label = videoLabel(
    video.title,
    video.status === "error" ? "failed" : "fetching",
  );
  // The accessible name of the open button stays the id when there is no title:
  // "Open Reading details from YouTube" names an action rather than a video,
  // and a grid of untitled cards would announce every tile identically.
  const openLabel = video.title?.trim() || video.id;
  // The heading's own name has one extra constraint the thumbnail's does not:
  // the heading SHOWS text, and an accessible name that does not contain the
  // visible text breaks speech control (WCAG 2.5.3) — "click Details
  // unavailable" would match nothing. So it leads with the words on screen and
  // appends the id, which is what still tells two untitled tiles apart.
  const titleLabel = label.placeholder
    ? `${label.text} — ${video.id}`
    : undefined;

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
          aria-label={`Open ${openLabel}`}
        >
          <ThumbFill
            id={video.id}
            hasThumbnail={video.has_thumbnail}
            version={video.thumbnail_version}
          />
          {/* Dot only: the word "Unwatched" was the loudest thing on the
              thumbnail for a fact the glowing dot already carries. The pill
              shape and the label stay for anyone not reading it visually. */}
          {isNew ? (
            <span
              className="tag new"
              role="img"
              aria-label="Unwatched"
              title="Unwatched"
            />
          ) : null}
          {/* The one thing a tombstone actually costs, stated where the state
              of the media belongs — on the poster, beside the runtime — so the
              rows below keep saying what they say on every other card.
              Literally beside it, in one bottom-right row: the poster's other
              three corners are taken (the unwatched dot top-left, the category
              pill bottom-left) and the top-right one belongs to the
              favorite/watched buttons in .acts, where a chip would sit
              underneath them. */}
          <span className="thumb-br">
            {/* "Removed", not "Deleted". The retention setting this is the
                outcome of already argues the point (Settings.tsx): "delete"
                reads as the whole video going away, and what goes is the file —
                the summary, transcript, chapters and poster all stay, and the
                video keeps turning up in search. Saying "Deleted" here undid
                that on the one screen where the consequence is visible. It is
                also the verb the setting is written in, "Remove file after N
                days", so the card reports the outcome back in the words the
                setting was chosen in. Same word on the search card and the
                player stage. */}
            {video.status === "tombstoned" ? (
              <span className="tag gone">Removed</span>
            ) : null}
            <span className="dur">
              {formatDuration(video.duration_seconds)}
            </span>
          </span>
          {/* Decorative inside the button: the button's aria-label already
              names the video, so the pill adds no accessible text. */}
          {thumbCategory ? (
            <span className="metapill oncover">
              <span
                className="dotc"
                style={{ background: thumbCategory.color }}
              />
              {thumbCategory.label}
            </span>
          ) : null}
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
        {/* With no channel resolved yet the byline would be empty, so the id
            takes the slot it will later give up — as a link out to YouTube,
            which is the one thing that can still be checked about this card. */}
        {label.placeholder && !video.channel_id && !video.channel_name ? (
          <a
            className="ag-id"
            href={watchURL(video.id)}
            target="_blank"
            rel="noreferrer"
            title="Open on YouTube"
            onClick={(e) => e.stopPropagation()}
          >
            {shortWatchLink(video.id)}
          </a>
        ) : (
          <ChannelLink
            channelId={video.channel_id}
            name={video.channel_name}
            onOpenChannel={onOpenChannel}
          />
        )}
        {/* When the video aired, and nothing else. The date it entered the
            archive used to lead this line — the grid's default order ranks by
            it — but it is not a fact anyone reads a card for, and it was the
            reason the line needed two date helpers. With one date left it gets
            formatAgo's full words: formatAge's abbreviations existed only so
            three parts would fit the narrowest column. Same wording as the
            Player's eyebrow and the Inbox card, so a video reads identically
            wherever it appears. */}
        {video.published_at ? (
          <>
            <span className="dot">·</span>
            aired {formatAgo(video.published_at)}
          </>
        ) : null}
      </div>
      <h3 className={label.placeholder ? "placeholder" : undefined}>
        <button
          type="button"
          className="title-btn"
          onClick={() => onOpen(video.id)}
          // Only while the title is a placeholder: every untitled card would
          // otherwise be announced by the same sentence, and the id is the one
          // thing that tells them apart. With a real title the text IS the name.
          aria-label={titleLabel}
        >
          {label.text}
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
  if (video.status === "error") {
    return (
      <div className="card-foot">
        <div className="life err">
          <span className="led" />
          Download failed
        </div>
        <Redownload video={video} onRedownload={onRedownload} />
      </div>
    );
  }
  // A tombstone is a lifecycle state, not a watch state: the card says "Removed"
  // on the poster and everything under it reads as it would on any other video.
  // The one thing not carried over is the expiry countdown a watched video
  // shows — there is nothing left to expire — so a swept video's row holds
  // nothing but the way back.
  //
  // That half can be absent too — a list that wires no onRedownload handler
  // (the channel Archive tab) — and an empty .card-foot still eats the .card
  // flex gap, so that case renders nothing at all, exactly as the fresh-video
  // case at the bottom does.
  if (video.status === "tombstoned") {
    if (!onRedownload) return null;
    return (
      <div className="card-foot">
        <Redownload video={video} onRedownload={onRedownload} />
      </div>
    );
  }
  // A favorite renders no lifecycle row at all. It used to say "Kept forever"
  // under a star; the lit star on the thumbnail is that same fact, and saying
  // it twice on one card made the word the redundant half. The early return is
  // load-bearing rather than a formality: fall through and a WATCHED favorite
  // would pick up the expiry countdown below and count down to a sweep that
  // will never come for it.
  if (video.favorite) {
    return null;
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
  // Nothing to say about a fresh video's lifecycle that the thumbnail does not
  // already say (unwatched dot, resume bar), and the category pill it used to
  // carry now sits on the thumbnail, so there is nothing left to render —
  // an empty .life would still eat a 10px `.card` flex gap, hence null.
  return null;
}

// Redownload is the way back from a failed download or a tombstone — the only
// place in the app that offers it, which is why notInFlight keeps both states in
// the Library grid. Renders nothing when the list did not wire a handler.
function Redownload({
  video,
  onRedownload,
}: {
  video: Video;
  onRedownload?: (id: string) => void;
}) {
  if (!onRedownload) return null;
  return (
    <Button
      type="button"
      variant="tinted"
      small
      onClick={() => onRedownload(video.id)}
    >
      <Icon name="refresh" size="15px" /> Re-download
    </Button>
  );
}

// categoryMeta resolves the classifier's label/color for a video, or null
// when it is uncategorized or carries a category this build does not know.
function categoryMeta(category: string) {
  if (!category || category === UNCATEGORIZED) return null;
  return CATEGORY_BY_ID[category] ?? null;
}
