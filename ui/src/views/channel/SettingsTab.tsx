import { useState } from "react";
import { Button } from "../../ui";
import { Icon } from "../../icons";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { ChannelDeleteWarning } from "../../components/ChannelDeleteWarning";
import { FormatPicker } from "../../components/FormatPicker";
import { StorageSection } from "./StorageSection";
import {
  updateChannel,
  scanChannel,
  deleteChannel,
  subscribeChannel,
  unsubscribeChannel,
  getKeepReadsImpact,
  type KeepReadsImpact,
} from "../../api/channels";
import { useSettings } from "../../settingsStore";
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
  const [confirmKeepOff, setConfirmKeepOff] = useState<KeepReadsImpact | null>(
    null,
  );
  // The global format preset, so the override list can mark which one
  // "use the global setting" actually resolves to. A failed fetch leaves it
  // "", which badges no row — better than badging the wrong one.
  const { settings } = useSettings();
  const globalPreset = settings?.format_preset ?? "";

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

  // On saves at once. Off takes away what the switch kept so far, so it asks
  // first with the real counts, unless there is nothing to take.
  async function toggleKeepReads() {
    if (!detail.keep_reads) {
      await run(() => updateChannel(detail.id, { keep_reads: true }));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const impact = await getKeepReadsImpact(detail.id);
      if (impact.deleted + impact.ignored + impact.inbox === 0) {
        await updateChannel(detail.id, { keep_reads: false });
        onChanged();
      } else {
        setConfirmKeepOff(impact);
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function confirmKeepReadsOff() {
    await run(() => updateChannel(detail.id, { keep_reads: false }));
    setConfirmKeepOff(null);
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

        {/* What survives a video's file, for every video of the channel —
            downloaded ones as much as the inbox reads peeq only summarized.
            That is why it is outside the subscribed block and never disabled:
            a channel that is not subscribed, or reads nothing, still has
            deleted videos to keep or forget. */}
        <div className="chan-srow">
          <div>
            <label className="lab" htmlFor="chan-keepreads">
              Keep in Search after the file is gone
            </label>
            <div className="hint">
              Off: deleting or ignoring a video removes everything peeq knows
              about it. On: only the file goes; it stays searchable.
            </div>
          </div>
          <input
            id="chan-keepreads"
            type="checkbox"
            disabled={busy}
            checked={detail.keep_reads}
            onChange={toggleKeepReads}
          />
        </div>

        <StorageSection channelId={detail.id} />
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

      <ConfirmDialog
        open={confirmKeepOff !== null}
        title="Remove kept videos from Search?"
        confirmLabel={keepOffLabel(confirmKeepOff)}
        busy={busy}
        onConfirm={confirmKeepReadsOff}
        onCancel={() => setConfirmKeepOff(null)}
      >
        {confirmKeepOff ? <KeepOffWarning impact={confirmKeepOff} /> : null}
      </ConfirmDialog>

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

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`;
}

// The removed count is deleted + ignored: inbox videos are not removed, they
// only leave Search.
function keepOffLabel(impact: KeepReadsImpact | null): string {
  if (!impact) return "Remove";
  const removed = impact.deleted + impact.ignored;
  return removed > 0
    ? `Remove ${plural(removed, "video", "videos")}`
    : "Remove from Search";
}

function KeepOffWarning({ impact }: { impact: KeepReadsImpact }) {
  const parts: string[] = [];
  if (impact.deleted > 0) parts.push(`${impact.deleted} deleted`);
  if (impact.ignored > 0) parts.push(`${impact.ignored} ignored`);
  const removed = impact.deleted + impact.ignored;
  return (
    <>
      {removed > 0 ? (
        <p>
          {parts.join(" and ")} {removed === 1 ? "video" : "videos"} from this
          channel will be permanently removed: transcripts, summaries and their
          search entries. They will no longer appear in Search or as Ask
          sources.
        </p>
      ) : null}
      {impact.inbox > 0 ? (
        <p>
          {plural(impact.inbox, "video", "videos")} in the New tab keep their
          summary but leave Search.
        </p>
      ) : null}
      <p>
        This can't be undone. Turning the switch back on only applies to videos
        from now on.
      </p>
    </>
  );
}
