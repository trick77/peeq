import { formatAgo } from "../../format";
import { ShareChip } from "../../components/ShareControl";
import type { ShareStatus } from "../../api/share";
import type { Video } from "../../api/types";
import { ChannelLink } from "../../components/ChannelLink";

// MetaHeader is the byline, title and share chip above the action row.
//
// Presentational, with one exception that is not: onOpenChannel is optional,
// and it is passed straight through to ChannelLink, which renders plain text
// when it is absent. The Player is reachable from places that have nowhere to
// navigate to.
export function MetaHeader({
  video,
  shareStatus,
  onOpenChannel,
}: {
  video: Video;
  shareStatus: ShareStatus;
  onOpenChannel?: (channelId: string) => void;
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
          published_at is unknown for some live streams and premieres. */}
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
      </div>
      <div className="playtitle">
        <h1>{video.title}</h1>
        <ShareChip status={shareStatus} />
      </div>
    </>
  );
}
