import { useState, type FormEvent } from "react";
import { Icon } from "../icons";
import { Button } from "../ui";
import {
  addDownload,
  CookieRequiredError,
  InvalidUrlError,
} from "../api/downloads";
import { addChannel } from "../api/channels";
import { isChannelURL } from "../youtube";

// Add — the paste-a-URL view. The mockup shows a live metadata preview
// before the user confirms; Task 14's backend (POST /api/downloads) has no
// separate "peek at metadata" endpoint, so this queues the download
// directly on submit and shows the resulting queue entry as confirmation
// instead of a pre-download preview. Task 13 adds channel-URL routing: a
// pasted channel link tracks the channel (POST /api/channels) instead of
// queuing a video download — subscribing is left to the Channels view.
export function Add({ onQueued }: { onQueued: (videoId: string) => void }) {
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [queued, setQueued] = useState<{
    title?: string;
    channel_name?: string;
  } | null>(null);
  const [tracked, setTracked] = useState<{ name: string } | null>(null);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const trimmed = url.trim();
    if (!trimmed || busy) return;
    setBusy(true);
    setError(null);
    setQueued(null);
    setTracked(null);
    try {
      if (isChannelURL(trimmed)) {
        const channel = await addChannel(trimmed, false);
        setTracked({ name: channel.name });
        setUrl("");
      } else {
        const job = await addDownload(trimmed);
        setQueued({ title: job.title, channel_name: job.channel_name });
        onQueued(job.video_id);
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
        <Button type="submit" busy={busy} disabled={!url.trim()}>
          {!busy && <Icon name="download" size="18px" />}
          {busy
            ? "Adding"
            : isChannelURL(url)
              ? "Track channel"
              : "Download now"}
        </Button>
      </form>

      <div className="hint">
        <span className="led" />
        Downloads queue immediately using the format preset from Settings —
        subtitles &amp; a summary are included automatically once later phases
        add them. A channel link tracks the channel instead, downloading nothing
        — you can also add channels from the Channels page.
      </div>

      {error ? <div className="errline">{error}</div> : null}

      {queued ? (
        <div className="preview">
          <div>
            <div className="pt g4" />
          </div>
          <div>
            <h2>{queued.title || "Queued"}</h2>
            <div className="by">
              {queued.channel_name || "Added to the download queue"}
            </div>
            <p style={{ margin: 0, fontSize: 13, color: "var(--color-muted)" }}>
              Watch progress in the download dock, or open the video from the
              Library once it's done.
            </p>
          </div>
        </div>
      ) : null}

      {tracked ? (
        <div className="preview">
          <div>
            <div className="pt g4" />
          </div>
          <div>
            <h2>Tracked {tracked.name}</h2>
            <div className="by">
              Not subscribed — new uploads won't auto-download yet.
            </div>
            <p style={{ margin: 0, fontSize: 13, color: "var(--color-muted)" }}>
              Subscribe or set an autodownload format from the Channels page.
            </p>
          </div>
        </div>
      ) : null}
    </div>
  );
}
