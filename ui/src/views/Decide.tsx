import { useEffect, useMemo, useState } from "react";
import { Icon } from "../icons";
import { listPending, downloadPending, ignorePending } from "../api/pending";
import type { PendingItem } from "../api/types";
import { formatDuration } from "../format";
import { Button } from "../ui";

// Decide — new uploads awaiting the user's keep/ignore call. This was the
// "Pending" page; the name changed with the Decide/Queue/Activity split (a
// decision to make, not a machine state), but the API is still /api/pending —
// only the UI vocabulary moved, so the channel_videos.state enum and its
// handlers stay untouched.
//
// Reuses Library's `.grid`/`.card`/`.thumb`/`.dur` visual language, but the
// thumbnail is the remote `thumbnail_url` (no local media exists yet — an item
// here has never been downloaded), and the two actions are Download now /
// Ignore rather than favorite/watched.
export function Decide({
  onCountChange,
  onOpenChannel,
  onQueued,
}: {
  onCountChange?: (n: number) => void;
  onOpenChannel?: (id: string) => void;
  // onQueued — fired after a video is queued for download, so App can seed the
  // queue poll and the item shows on Queue right away (mirrors the Add view).
  onQueued?: () => void;
} = {}) {
  const [items, setItems] = useState<PendingItem[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  // channel filter: "all" or a specific channel_id. For the common case of a
  // channel dumping a week of uploads at once, this narrows the grid (and the
  // Download-all action) to one channel.
  const [channel, setChannel] = useState<string>("all");
  // Bulk state: bulkBusy while the Download-all loop runs; confirmBulk is the
  // inline two-step guard for large batches (a 40-video download is not a
  // click to fire by accident).
  const [bulkBusy, setBulkBusy] = useState(false);
  const [confirmBulk, setConfirmBulk] = useState(false);

  function load() {
    setError(null);
    listPending()
      .then((list) => {
        setItems(list);
        onCountChange?.(list.length);
      })
      .catch((e: Error) => setError(e.message));
  }

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // The distinct channels present, in first-seen order, so the chips are
  // stable and don't reshuffle as the grid drains.
  const channels = useMemo(() => {
    const seen = new Map<string, string>();
    for (const it of items) {
      if (it.channel_id && !seen.has(it.channel_id)) {
        seen.set(it.channel_id, it.channel_name || it.channel_id);
      }
    }
    return Array.from(seen, ([id, name]) => ({ id, name }));
  }, [items]);

  const visible = useMemo(
    () =>
      channel === "all" ? items : items.filter((i) => i.channel_id === channel),
    [items, channel],
  );

  // If the active channel filter empties out (its last item was downloaded or
  // ignored), fall back to "all" so the user isn't left staring at a blank
  // grid with a filter still applied.
  useEffect(() => {
    if (channel !== "all" && !items.some((i) => i.channel_id === channel)) {
      setChannel("all");
    }
  }, [items, channel]);

  function remove(videoID: string) {
    setItems((prev) => {
      const next = prev.filter((i) => i.video_id !== videoID);
      onCountChange?.(next.length);
      return next;
    });
  }

  async function handleDownload(item: PendingItem) {
    setBusyId(item.video_id);
    try {
      await downloadPending(item.video_id);
      remove(item.video_id);
      onQueued?.();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusyId(null);
    }
  }

  async function handleIgnore(item: PendingItem) {
    setBusyId(item.video_id);
    try {
      await ignorePending(item.video_id);
      remove(item.video_id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusyId(null);
    }
  }

  // Download all — queues every currently-visible item. There is no bulk
  // endpoint; it loops the existing single-item /api/pending/{id}/download so
  // the backend contract stays exactly one video per call. Sequential (not
  // Promise.all) so a mid-batch failure stops cleanly with the rest still on
  // the page, rather than firing 40 requests at once. Confirms first for a
  // large batch via the inline two-step below.
  async function handleDownloadAll() {
    const batch = visible;
    if (batch.length > 10 && !confirmBulk) {
      setConfirmBulk(true);
      return;
    }
    setConfirmBulk(false);
    setBulkBusy(true);
    setError(null);
    let queuedAny = false;
    try {
      for (const item of batch) {
        await downloadPending(item.video_id);
        remove(item.video_id);
        queuedAny = true;
      }
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBulkBusy(false);
      // Seed the queue once for the whole batch (not per item) if anything was
      // actually queued — even when the batch stopped early on a failure.
      if (queuedAny) onQueued?.();
    }
  }

  const bulkLabel = confirmBulk
    ? `Download ${visible.length} — confirm`
    : "Download all";

  return (
    <>
      {error ? <div className="errline">{error}</div> : null}

      {items.length > 0 ? (
        <div
          style={{
            display: "flex",
            alignItems: "center",
            gap: 12,
            marginBottom: 14,
            flexWrap: "wrap",
          }}
        >
          {channels.length > 1 ? (
            <div className="catchips" style={{ margin: 0, flex: 1 }}>
              <button
                type="button"
                className={`catchip${channel === "all" ? " on" : ""}`}
                onClick={() => setChannel("all")}
              >
                All channels <span className="n">{items.length}</span>
              </button>
              {channels.map((c) => (
                <button
                  key={c.id}
                  type="button"
                  className={`catchip${channel === c.id ? " on" : ""}`}
                  onClick={() => setChannel(c.id)}
                >
                  {c.name}{" "}
                  <span className="n">
                    {items.filter((i) => i.channel_id === c.id).length}
                  </span>
                </button>
              ))}
            </div>
          ) : (
            <div style={{ flex: 1 }} />
          )}
          {visible.length > 1 ? (
            <Button
              type="button"
              variant={confirmBulk ? "primary" : "secondary"}
              busy={bulkBusy}
              onClick={handleDownloadAll}
              onBlur={() => setConfirmBulk(false)}
            >
              <Icon name="download" size="16px" />
              {bulkLabel}
            </Button>
          ) : null}
        </div>
      ) : null}

      <div className="grid">
        {visible.map((item) => (
          <article key={item.video_id} className="card">
            <div className="thumb">
              <img
                className="fill"
                src={item.thumbnail_url}
                alt=""
                loading="lazy"
              />
              <span className="dur">
                {formatDuration(item.duration_seconds)}
              </span>
            </div>
            <h3>{item.title}</h3>
            <div className="by">
              {onOpenChannel && item.channel_id ? (
                <button
                  type="button"
                  className="chan-link"
                  onClick={() => onOpenChannel(item.channel_id)}
                >
                  {item.channel_name || item.channel_id}
                </button>
              ) : (
                item.channel_name || item.channel_id
              )}
            </div>
            <div
              className="acts-row"
              style={{ display: "flex", gap: 8, marginTop: 8 }}
            >
              <Button
                type="button"
                variant="secondary"
                disabled={busyId === item.video_id || bulkBusy}
                onClick={() => handleDownload(item)}
              >
                <Icon name="download" size="16px" />
                Download now
              </Button>
              <Button
                type="button"
                variant="dangerQuiet"
                disabled={busyId === item.video_id || bulkBusy}
                onClick={() => handleIgnore(item)}
              >
                <Icon name="trash" size="16px" />
                Ignore
              </Button>
            </div>
          </article>
        ))}
      </div>
      {items.length === 0 && !error ? (
        <p style={{ color: "var(--color-faint)" }}>Nothing to decide.</p>
      ) : null}
    </>
  );
}
