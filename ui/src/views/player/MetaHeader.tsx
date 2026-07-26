import { formatAgo } from "../../format";
import { ShareChip } from "../../components/ShareControl";
import type { ShareStatus } from "../../api/share";
import type { Video } from "../../api/types";

// MetaHeader is the byline, title and share chip above the action row.
//
// Presentational, with one exception that is not: onOpenChannel is optional,
// and when it is absent the channel renders as plain text rather than a link.
// The Player is reachable from places that have nowhere to navigate to.
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

          Both ages spelled out via formatAgo: the card abbreviates the
          second one only because its column is narrow, and this one is
          not. "aired" is conditional because published_at is unknown for
          some live streams and premieres; "added" is conditional because
          a row can be listed without ever having finished downloading. */}
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
        {video.published_at ? (
          <>
            <span className="dot">·</span>
            aired {formatAgo(video.published_at)}
          </>
        ) : null}
        {video.downloaded_at ? (
          <>
            <span className="dot">·</span>
            added {formatAgo(video.downloaded_at)}
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
