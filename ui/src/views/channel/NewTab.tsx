import { useEffect, useState } from "react";
import { Button } from "../../ui";
import { Icon } from "../../icons";
import { listPending, downloadPending, ignorePending } from "../../api/pending";
import { scanChannel } from "../../api/channels";
import { formatDuration } from "../../format";
import type { ChannelDetail, PendingItem } from "../../api/types";

// scheduleLine renders the "last checked / next check" sentence shown in
// both the populated and the empty state. next_scan_at in the past means the
// scheduler simply has not reached this channel yet.
function scheduleLine(detail: ChannelDetail): string {
  const parts: string[] = [];
  if (detail.last_scanned_at) {
    parts.push(`Checked ${new Date(detail.last_scanned_at + "Z").toLocaleString()}`);
  } else {
    parts.push("Never checked");
  }
  if (detail.next_scan_at) {
    const next = new Date(detail.next_scan_at + "Z").getTime();
    parts.push(next <= Date.now() ? "next check due now" : `next check ${new Date(next).toLocaleString()}`);
  }
  return parts.join(" · ");
}

export function NewTab({ detail, onChanged }: { detail: ChannelDetail; onChanged: () => void }) {
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
      // The scheduler polls for due channels rather than being told to run,
      // so a successful call means "queued", never "done".
      setNotice(
        res.status === "blocked"
          ? (res.reason ?? "peeq cannot check this channel right now.")
          : "Checking soon — peeq will look for new videos on its next pass.",
      );
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

  const checkNow = (
    <Button type="button" variant="secondary" busy={scanning} onClick={handleScan}>
      <Icon name="clock" size="16px" /> Check now
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
          <div style={{ marginTop: 14 }}>{checkNow}</div>
        </div>
      ) : (
        <>
          <div className="listbar">
            <span style={{ fontSize: "var(--text-label)", color: "var(--color-faint)" }}>
              {scheduleLine(detail)}
            </span>
            <span style={{ marginLeft: "auto", display: "flex", gap: 8 }}>{checkNow}</span>
          </div>
          <div className="chan-plist">
            {items.map((item) => (
              <div key={item.video_id} className="chan-prow">
                <img className="chan-pthumb" src={item.thumbnail_url} alt="" loading="lazy" />
                <div className="chan-pt">
                  <div className="ti">{item.title}</div>
                  <div className="sub">{formatDuration(item.duration_seconds)}</div>
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
