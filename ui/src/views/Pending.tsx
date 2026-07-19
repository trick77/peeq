import { useEffect, useState } from "react";
import { Icon } from "../icons";
import { listPending, downloadPending, ignorePending } from "../api/pending";
import type { PendingItem } from "../api/types";
import { formatDuration } from "../format";

// Pending — the channel_videos ledger's "New & pending" grid: reuses
// Library's `.grid`/`.card`/`.thumb`/`.dur` visual language, but the
// thumbnail is the remote `thumbnail_url` (no local media exists yet — an
// item here has never been downloaded), and the two actions are Download
// now / Ignore rather than favorite/watched.
export function Pending({ onCountChange }: { onCountChange?: (n: number) => void } = {}) {
  const [items, setItems] = useState<PendingItem[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);

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

  async function handleDownload(item: PendingItem) {
    setBusyId(item.video_id);
    try {
      await downloadPending(item.video_id);
      setItems((prev) => {
        const next = prev.filter((i) => i.video_id !== item.video_id);
        onCountChange?.(next.length);
        return next;
      });
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
      setItems((prev) => {
        const next = prev.filter((i) => i.video_id !== item.video_id);
        onCountChange?.(next.length);
        return next;
      });
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusyId(null);
    }
  }

  return (
    <>
      {error ? <div className="errline">{error}</div> : null}
      <div className="grid">
        {items.map((item) => (
          <article key={item.video_id} className="card">
            <div className="thumb">
              <img className="fill" src={item.thumbnail_url} alt="" loading="lazy" />
              <span className="dur">{formatDuration(item.duration_seconds)}</span>
            </div>
            <h3>{item.title}</h3>
            <div className="by">{item.channel_name || item.channel_id}</div>
            <div className="acts-row" style={{ display: "flex", gap: 8, marginTop: 8 }}>
              <button
                type="button"
                className="abtn"
                disabled={busyId === item.video_id}
                onClick={() => handleDownload(item)}
              >
                <Icon name="download" size="16px" />
                Download now
              </button>
              <button
                type="button"
                className="abtn danger"
                disabled={busyId === item.video_id}
                onClick={() => handleIgnore(item)}
              >
                <Icon name="trash" size="16px" />
                Ignore
              </button>
            </div>
          </article>
        ))}
      </div>
      {items.length === 0 && !error ? <p style={{ color: "var(--color-faint)" }}>Nothing pending.</p> : null}
    </>
  );
}
