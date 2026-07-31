import { useEffect, useState } from "react";
import { Button } from "../../ui";
import { Icon } from "../../icons";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { ChannelDeleteWarning } from "../../components/ChannelDeleteWarning";
import { FormatPicker } from "../../components/FormatPicker";
import {
  updateChannel,
  scanChannel,
  deleteChannel,
  subscribeChannel,
  unsubscribeChannel,
} from "../../api/channels";
import { getSettings } from "../../api/settings";
import { presetLabel } from "../../formatPresets";
import { isScanQueued, scanNotice, scheduleLine } from "./schedule";
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
  const [busy, setBusy] = useState(false);
  const [scanning, setScanning] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  // The global format preset, so the override list can mark which one
  // "use the global setting" actually resolves to. A failed fetch leaves it
  // "", which badges no row — better than badging the wrong one.
  const [globalPreset, setGlobalPreset] = useState("");

  useEffect(() => {
    getSettings()
      .then((s) => setGlobalPreset(s.format_preset))
      .catch(() => setGlobalPreset(""));
  }, []);

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
      setNotice(scanNotice(res));
      onChanged();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setScanning(false);
    }
  }

  async function handleDelete() {
    setBusy(true);
    try {
      await deleteChannel(detail.id);
      onDeleted();
    } catch (e) {
      setError((e as Error).message);
      setBusy(false);
      setConfirmDelete(false);
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
              Peeq checks this channel for new uploads on a schedule.
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
                <span className="lab">Format override</span>
                <div className="hint">
                  {presetLabel(globalPreset) ? (
                    <>
                      Leave on the global setting to follow{" "}
                      <b>{presetLabel(globalPreset)}</b>.
                    </>
                  ) : (
                    "Leave on the global setting to follow your Settings choice."
                  )}
                </div>
              </div>
              <FormatPicker
                value={detail.format_override ?? ""}
                globalPreset={globalPreset}
                disabled={busy}
                onPick={(value) =>
                  run(() =>
                    updateChannel(detail.id, { format_override: value }),
                  )
                }
              />
            </div>

            <div className="chan-srow">
              <div>
                <div className="lab">Scanning for new videos</div>
                <div className="hint">
                  {scheduleLine(detail, "Last scanned")}
                </div>
              </div>
              <Button
                type="button"
                variant="secondary"
                busy={scanning}
                onClick={handleScan}
                title={
                  isScanQueued(detail)
                    ? "Waiting for the next scan pass — press to scan again"
                    : undefined
                }
              >
                {isScanQueued(detail) ? "Queued" : "Scan now"}
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
          onClick={() => setConfirmDelete(true)}
        >
          <Icon name="trash" size="16px" /> Delete channel and its{" "}
          {detail.archived_count} videos
        </Button>
      </div>

      {/* Same modal + wording as the Channels list delete: it is the same
          irreversible action, so it must not warn differently. */}
      <ConfirmDialog
        open={confirmDelete}
        title="Delete channel?"
        confirmLabel="Delete channel"
        busy={busy}
        onConfirm={handleDelete}
        onCancel={() => setConfirmDelete(false)}
      >
        <ChannelDeleteWarning
          name={detail.name}
          count={detail.archived_count}
        />
      </ConfirmDialog>
    </>
  );
}
