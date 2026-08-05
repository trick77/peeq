import { Icon } from "../icons";
import { Button } from "../ui";
import type { AutoUnsubscribedChannel } from "../api/types";
import { toDate } from "../format";

// reasonLabel maps the store's `reason` code to the sentence shown next to
// the tomb-life dot. Only "deleted" is produced today (channels.ReasonDeleted),
// but a readable fallback keeps this from silently showing a raw code if a
// new reason is ever added server-side.
function reasonLabel(reason: string): string {
  return reason === "deleted"
    ? "Channel deleted on YouTube"
    : "Channel is gone from YouTube";
}

// AutoUnsubscribedSection — a collapsed-by-default <details> listing every
// channel peeq unsubscribed on its own (three consecutive "deleted" scans).
// Collapsed by default because, unlike the review band, nothing here needs
// the user's attention right now — it is a log to check, not a queue to
// clear.
//
// Every row states archived videos were kept: unsubscribing only ever
// deletes the `subscriptions` row, never the channel's scan history or
// downloaded media, so re-subscribe genuinely restores what was there. An
// automatic action the user can't trust is worse than no automation.
export function AutoUnsubscribedSection({
  channels,
  onResubscribe,
}: {
  channels: AutoUnsubscribedChannel[];
  onResubscribe: (c: AutoUnsubscribedChannel) => void;
}) {
  if (channels.length === 0) return null;

  return (
    <details className="tomb-sect">
      <summary>
        <Icon
          name="chevronRight"
          size="16px"
          style={{ transition: "transform .15s" }}
        />
        Auto-unsubscribed{" "}
        <span className="metapill dead">{channels.length}</span>
      </summary>
      <div className="tomb-body">
        {channels.map((c) => (
          <div key={c.id} className="tomb-row">
            <div className="channel-info" style={{ flex: 1 }}>
              <div className="nm">{c.name}</div>
              <div className="by">
                <span className="life tomb">
                  <span className="led" />
                  {reasonLabel(c.reason)}
                </span>
                <span className="dot">·</span>
                <span>Unsubscribed {toDate(c.at).toLocaleDateString()}</span>
              </div>
              <div className="by" style={{ color: "var(--color-faint)" }}>
                Archived videos were kept — nothing was deleted.
              </div>
            </div>
            <Button
              type="button"
              variant="primary"
              small
              onClick={() => onResubscribe(c)}
            >
              Re-subscribe
            </Button>
          </div>
        ))}
      </div>
    </details>
  );
}
