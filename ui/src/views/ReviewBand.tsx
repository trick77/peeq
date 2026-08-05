import type { Channel } from "../api/types";
import { Button } from "../ui";
import { toDate } from "../format";

// ReviewBand — the "needs review" surface for dormant subscriptions (no new
// video in 6+ months). It sits ABOVE the main channel list but BELOW the
// add-channel form, so the page's primary action never moves just because
// channels need review. Renders nothing when there is nothing to review:
// an empty band, or a "0 channels need review" header, would be a permanent
// fixture on a page that is otherwise quiet.
//
// Rows are flush (hairline separators via .band .channel-row in index.css),
// not cards nested inside the .band card — a nested card insets its buttons
// an extra 15px and breaks the shared right edge every action button on
// this page aligns on. That was a design-review finding; do not reintroduce
// cards-in-a-card here.
export function ReviewBand({
  channels,
  onKeep,
  onKeepAll,
  onUnsubscribe,
}: {
  channels: Channel[];
  onKeep: (c: Channel) => void;
  onKeepAll: () => void;
  onUnsubscribe: (c: Channel) => void;
}) {
  if (channels.length === 0) return null;

  return (
    <div className="band">
      <div className="band-head">
        <h3>
          {channels.length} channel{channels.length === 1 ? "" : "s"} need
          {channels.length === 1 ? "s" : ""} review
        </h3>
        <span className="why">No new videos in over 6 months</span>
        <span className="spacer" />
        <Button type="button" variant="secondary" small onClick={onKeepAll}>
          Dismiss all
        </Button>
      </div>
      <div className="channel-list">
        {channels.map((c) => (
          <div key={c.id} className="channel-row">
            <div className="channel-info">
              <div className="nm">{c.name}</div>
              <div className="by">
                <span className="life warn">
                  <span className="led" />
                  {c.last_video_at
                    ? `Last video ${toDate(c.last_video_at).toLocaleDateString()}`
                    : "No video ever recorded"}
                </span>
                <span className="dot">·</span>
                <span>{c.downloaded_count} archived</span>
              </div>
            </div>
            <div className="channel-actions">
              <Button
                type="button"
                variant="secondary"
                small
                onClick={() => onKeep(c)}
              >
                Keep subscribed
              </Button>
              <Button
                type="button"
                variant="dangerQuiet"
                small
                onClick={() => onUnsubscribe(c)}
              >
                Unsubscribe
              </Button>
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
