import { useState, type FormEvent } from "react";
import { useTransient } from "../hooks/useTransient";
import { Icon } from "../icons";
import { Button } from "../ui";
import {
  addDownload,
  CookieRequiredError,
  InvalidUrlError,
} from "../api/downloads";
import { addChannel } from "../api/channels";
import { isChannelURL } from "../youtube";

// Add — the paste-a-URL view. The backend (POST /api/downloads) has no
// separate "peek at metadata" endpoint, so a pasted video link queues the
// download straight into Up next; the confirmation is a single line that
// fades on its own — no preview box, since the metadata isn't known yet.
// A pasted channel link adds the channel (POST /api/channels) instead of
// queuing a download — subscribing is left to the Channels view.
export function Add({
  onQueued,
  onPendingChanged,
}: {
  onQueued: (videoId: string) => void;
  // A pasted URL can be a video the inbox is still holding; queuing it moves
  // that row out, so the rail's count is re-read.
  onPendingChanged?: () => void;
}) {
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The confirmation line under the form. It is up for a few seconds, then
  // fades: `leaving` runs the exit animation for its last beat, and a fast
  // second submit restarts the countdown rather than stacking.
  const {
    value: confirm,
    leaving,
    show: showConfirm,
    clear: clearConfirm,
  } = useTransient<string>(4300, { fadeMs: 300 });

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const trimmed = url.trim();
    if (!trimmed || busy) return;
    setBusy(true);
    setError(null);
    clearConfirm();
    try {
      if (isChannelURL(trimmed)) {
        const channel = await addChannel(trimmed, false);
        showConfirm(
          `Added ${channel.name} — new uploads won't auto-download yet`,
        );
        setUrl("");
      } else {
        const job = await addDownload(trimmed);
        onQueued(job.video_id);
        onPendingChanged?.();
        showConfirm("Sent to Up next");
        setUrl("");
      }
    } catch (err) {
      if (err instanceof CookieRequiredError) {
        setError(
          "No YouTube cookie configured yet. Paste one on the Settings page first.",
        );
      } else if (err instanceof InvalidUrlError) {
        setError(
          err.message ||
            "That link isn't a single downloadable video (playlists and live streams aren't supported).",
        );
      } else {
        setError((err as Error).message ?? "Failed to add.");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="addwrap">
      <form className="paste" onSubmit={handleSubmit}>
        <label className="field">
          <Icon
            name="link"
            size="18px"
            style={{ color: "var(--color-faint)" }}
          />
          <input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="Paste a video or channel link"
            spellCheck={false}
            aria-label="Video or channel URL"
          />
        </label>
        <Button type="submit" small busy={busy} disabled={!url.trim()}>
          {busy ? "Adding" : isChannelURL(url) ? "Add channel" : "Add"}
          {!busy && !isChannelURL(url) && (
            <span className="addkbd" aria-hidden="true">
              ↵
            </span>
          )}
        </Button>
      </form>

      <div className="hint">
        Goes straight to <strong>Up next</strong> and starts downloading using
        the format preset from Settings. A channel link adds the channel
        instead, downloading nothing — you can also add channels from the
        Channels page.
      </div>

      {error ? <div className="errline">{error}</div> : null}

      {confirm ? (
        <div className={`addok${leaving ? " leaving" : ""}`} role="status">
          <Icon name="check" size="15px" />
          <span>{confirm}</span>
        </div>
      ) : null}
    </div>
  );
}
