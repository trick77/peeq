import { useEffect, useState } from "react";
import { Button } from "../../ui";
import { Icon } from "../../icons";
import { listPending, downloadPending, ignorePending } from "../../api/pending";
import { scanChannel } from "../../api/channels";
import { formatAgo, formatDuration } from "../../format";
import { isScanQueued, scanNotice, scheduleLine } from "./schedule";
import type { ChannelDetail, PendingItem } from "../../api/types";
import { DOT } from "../../sep";

export function NewTab({
  detail,
  onChanged,
}: {
  detail: ChannelDetail;
  onChanged: () => void;
}) {
  const [items, setItems] = useState<PendingItem[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [scanning, setScanning] = useState(false);

  function load() {
    setError(null);
    listPending(detail.id)
      .then(setItems)
      .catch((e: Error) => setError(e.message));
  }

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [detail.id]);

  async function handleScan() {
    setScanning(true);
    setNotice(null);
    setError(null);
    try {
      const res = await scanChannel(detail.id);
      // The scheduler polls for due channels rather than being told to run, so a
      // successful call means "queued", never "done" — scanNotice is worded that
      // way, and onChanged refetches so the button settles into its Queued state
      // from the server's own next_scan_at rather than a local flag.
      setNotice(scanNotice(res));
      onChanged();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setScanning(false);
    }
  }

  async function decide(item: PendingItem, keep: boolean) {
    setBusyId(item.video_id);
    try {
      if (keep) await downloadPending(item.video_id);
      else await ignorePending(item.video_id);
      setItems((prev) => prev.filter((i) => i.video_id !== item.video_id));
      onChanged();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusyId(null);
    }
  }

  // Queued comes from the server's schedule, not from having just clicked: a
  // reload, or a second tab, shows the same state. It parks there until the scan
  // lands and pushes next_scan_at back into the future — no spinner, because
  // nothing is running yet; the channel is in line.
  //
  // Deliberately still clickable while queued. An overdue next_scan_at also
  // describes a channel the loop CANNOT reach — a dead cookie or the kill-switch
  // parks the loop, and next_scan_at then sits in the past indefinitely.
  // Disabling would leave the user staring at "Queued" forever with no way to
  // find out why; pressing again is idempotent on the server and surfaces the
  // blocked reason that explains it.
  const queued = isScanQueued(detail);
  const scanNow = (
    <Button
      type="button"
      variant="secondary"
      busy={scanning}
      onClick={handleScan}
      title={
        queued
          ? "Waiting for the next scan pass — press to scan again"
          : undefined
      }
    >
      <Icon name="clock" size="16px" /> {queued ? "Queued" : "Scan now"}
    </Button>
  );

  return (
    <>
      {error ? <div className="errline">{error}</div> : null}
      {notice ? <div className="hint">{notice}</div> : null}

      {items.length === 0 ? (
        <div className="chan-empty">
          <div className="big">Nothing new</div>
          <div className="sub">{scheduleLine(detail)}</div>
          <div style={{ marginTop: 14 }}>
            {detail.subscribed ? (
              scanNow
            ) : (
              <span className="hint">
                Subscribe to this channel to scan for new videos.
              </span>
            )}
          </div>
        </div>
      ) : (
        <>
          <div className="listbar">
            <span
              style={{
                fontSize: "var(--text-label)",
                color: "var(--color-faint)",
              }}
            >
              {scheduleLine(detail)}
            </span>
            {detail.subscribed ? (
              <span style={{ marginLeft: "auto", display: "flex", gap: 8 }}>
                {scanNow}
              </span>
            ) : null}
          </div>
          <div className="chan-plist">
            {items.map((item) => (
              <div key={item.video_id} className="chan-prow">
                <img
                  className="chan-pthumb"
                  src={item.thumbnail_url}
                  alt=""
                  loading="lazy"
                />
                <div className="chan-pt">
                  {/* Same rows as the Inbox, so the same rule: a pending
                      video has no local copy yet, and its title links to the
                      original on YouTube in a new tab. Falls back to a watch
                      URL built from the video id when the ledger entry has no
                      url — the id IS the YouTube id. */}
                  <div className="ti">
                    <a
                      href={
                        item.url ||
                        `https://www.youtube.com/watch?v=${item.video_id}`
                      }
                      target="_blank"
                      rel="noopener noreferrer"
                      title={`Open "${item.title}" on YouTube`}
                    >
                      {item.title}
                    </a>
                  </div>
                  {/* This tab is a dense row list, not the Inbox's card
                      grid, so the date joins the duration on the sub line
                      rather than a card eyebrow — but it is the same
                      approximate publish date, worded the same way, and is
                      dropped rather than faked when unknown. */}
                  <div className="sub">
                    {formatDuration(item.duration_seconds)}
                    {item.published_at
                      ? `${DOT}aired ${formatAgo(item.published_at)}`
                      : ""}
                  </div>
                </div>
                <div style={{ display: "flex", gap: 6 }}>
                  <Button
                    type="button"
                    variant="secondary"
                    disabled={busyId === item.video_id}
                    onClick={() => decide(item, true)}
                  >
                    <Icon name="download" size="16px" /> Add
                  </Button>
                  <Button
                    type="button"
                    variant="dangerQuiet"
                    disabled={busyId === item.video_id}
                    onClick={() => decide(item, false)}
                  >
                    Ignore
                  </Button>
                </div>
              </div>
            ))}
          </div>
        </>
      )}
    </>
  );
}
