import { formatAgo } from "../../format";
import { ShareChip } from "../../components/ShareControl";
import { CategoryPicker } from "../../components/CategoryPicker";
import type { ShareStatus } from "../../api/share";
import type { Video } from "../../api/types";
import { ChannelLink } from "../../components/ChannelLink";

// MetaHeader is the byline, title and share chip above the action row.
//
// Presentational, with one exception that is not: onOpenChannel is optional,
// and it is passed straight through to ChannelLink, which renders plain text
// when it is absent. The Player is reachable from places that have nowhere to
// navigate to.
//
// onPickCategory is NOT optional, deliberately. The category is the one fact on
// this line that is always correctable — the Player is the only place it can be
// — so a header with no way to set it would be a state the app never wants.
export function MetaHeader({
  video,
  shareStatus,
  onOpenChannel,
  onPickCategory,
}: {
  video: Video;
  shareStatus: ShareStatus;
  onOpenChannel?: (channelId: string) => void;
  onPickCategory: (category: string) => void;
}) {
  return (
    <>
      {/* Who made it and when, above the title — the same eyebrow a
          library card carries, so a video reads the same way in the grid
          and on the page it opens.

          When the video aired, spelled out via formatAgo, and nothing
          else: the date it entered the archive was also on this line and
          is gone from every eyebrow above a title — it is a fact about the
          archive, not about the video. "aired" stays conditional because
          published_at is unknown for some live streams and premieres.

          The category pill ends the line, unconditionally — unlike "aired" it
          is never absent, because an unclassified video still renders the pill
          reading "Uncategorized" (see CategoryPicker). It had a line of its own between the
          title and the actions, which said what it is not (a verb, like the
          toolbar below) at the cost of a whole row for one chip. The eyebrow is
          where it belonged: this line is already the video's facts — who made
          it, when it aired — and the category is a third one. It is drawn at
          the line's own size rather than the control size it wore beside 40px
          buttons, so it reads as part of the sentence and not as a stray
          button parked on it. */}
      <div className="by">
        <ChannelLink
          channelId={video.channel_id}
          name={video.channel_name}
          onOpenChannel={onOpenChannel}
        />
        {video.published_at ? (
          <>
            <span className="dot">·</span>
            aired {formatAgo(video.published_at)}
          </>
        ) : null}
        <span className="dot">·</span>
        <CategoryPicker category={video.category} onPick={onPickCategory} />
      </div>
      <div className="playtitle">
        <h1>{video.title}</h1>
        <ShareChip status={shareStatus} />
      </div>
    </>
  );
}
