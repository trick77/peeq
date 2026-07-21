import { useState } from "react";
import { Button, controlClass } from "../../ui";
import { Icon } from "../../icons";
import {
  updateChannel,
  scanChannel,
  deleteChannel,
  subscribeChannel,
  unsubscribeChannel,
} from "../../api/channels";
import type { ChannelDetail } from "../../api/types";

export function SettingsTab({
  detail,
  onChanged,
  onDeleted,
}: {
  detail: ChannelDetail;
  onChanged: () => void;
  onDeleted: () => void;
}) {
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [format, setFormat] = useState(detail.format_override ?? "");
  const [busy, setBusy] = useState(false);
  const [scanning, setScanning] = useState(false);

  async function run(fn: () => Promise<unknown>) {
    setBusy(true);
    setError(null);
    try {
      await fn();
      onChanged();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function handleScan() {
    setScanning(true);
    setNotice(null);
    setError(null);
    try {
      const res = await scanChannel(detail.id);
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

  async function handleDelete() {
    const ok = window.confirm(
      `Delete ${detail.name} and its ${detail.archived_count} videos? This removes the files from disk, including any you kept forever. This cannot be undone.`,
    );
    if (!ok) return;
    setBusy(true);
    try {
      await deleteChannel(detail.id);
      onDeleted();
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
    }
  }

  return (
    <>
      {error ? <div className="errline">{error}</div> : null}
      {notice ? <div className="hint">{notice}</div> : null}

      <div className="chan-settings">
        <div className="chan-srow">
          <div>
            <div className="lab">Subscribed</div>
            <div className="hint">
              peeq checks this channel for new uploads on a schedule.
            </div>
          </div>
          <Button
            type="button"
            variant={detail.subscribed ? "gold" : "secondary"}
            busy={busy}
            onClick={() =>
              run(() =>
                detail.subscribed
                  ? unsubscribeChannel(detail.id)
                  : subscribeChannel(detail.id),
              )
            }
          >
            <Icon
              name={detail.subscribed ? "starFilled" : "star"}
              size="16px"
            />
            {detail.subscribed ? "Subscribed" : "Subscribe"}
          </Button>
        </div>

        {detail.subscribed ? (
          <>
            <div className="chan-srow">
              <div>
                <label className="lab" htmlFor="chan-autoadd">
                  Add new videos automatically
                </label>
                <div className="hint">
                  New uploads download without asking. Off means they wait in
                  the New tab.
                </div>
              </div>
              <input
                id="chan-autoadd"
                type="checkbox"
                disabled={busy}
                checked={detail.autodownload}
                onChange={() =>
                  run(() =>
                    updateChannel(detail.id, {
                      autodownload: !detail.autodownload,
                    }),
                  )
                }
              />
            </div>

            <div className="chan-srow">
              <div>
                <label className="lab" htmlFor="chan-format">
                  Format override
                </label>
                <div className="hint">
                  Leave empty to use your global format setting.
                </div>
              </div>
              <input
                id="chan-format"
                className={controlClass}
                style={{ maxWidth: 220 }}
                type="text"
                value={format}
                onChange={(e) => setFormat(e.target.value)}
                onBlur={() => {
                  if (format !== (detail.format_override ?? "")) {
                    run(() =>
                      updateChannel(detail.id, { format_override: format }),
                    );
                  }
                }}
                placeholder="Use the default"
              />
            </div>

            <div className="chan-srow">
              <div>
                <div className="lab">Checking for new videos</div>
                <div className="hint">
                  {detail.last_scanned_at
                    ? `Last checked ${new Date(detail.last_scanned_at + "Z").toLocaleString()}`
                    : "Never checked"}
                  {detail.next_scan_at
                    ? ` · next check ${new Date(detail.next_scan_at + "Z").toLocaleString()}`
                    : ""}
                </div>
              </div>
              <Button
                type="button"
                variant="secondary"
                busy={scanning}
                onClick={handleScan}
              >
                Check now
              </Button>
            </div>
          </>
        ) : null}
      </div>

      <div className="chan-danger">
        <Button
          type="button"
          variant="dangerQuiet"
          busy={busy}
          onClick={handleDelete}
        >
          <Icon name="trash" size="16px" /> Delete channel and its{" "}
          {detail.archived_count} videos
        </Button>
      </div>
    </>
  );
}
