import { useState, type FormEvent } from "react";
import { Icon } from "../icons";
import { addDownload, CookieRequiredError, InvalidUrlError } from "../api/downloads";

// Add — the paste-a-URL view. The mockup shows a live metadata preview
// before the user confirms; Task 14's backend (POST /api/downloads) has no
// separate "peek at metadata" endpoint, so this queues the download
// directly on submit and shows the resulting queue entry as confirmation
// instead of a pre-download preview.
export function Add({ onQueued }: { onQueued: (videoId: string) => void }) {
  const [url, setUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [queued, setQueued] = useState<{ title?: string; channel_name?: string } | null>(null);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (!url.trim() || busy) return;
    setBusy(true);
    setError(null);
    setQueued(null);
    try {
      const job = await addDownload(url.trim());
      setQueued({ title: job.title, channel_name: job.channel_name });
      onQueued(job.video_id);
      setUrl("");
    } catch (err) {
      if (err instanceof CookieRequiredError) {
        setError("No YouTube cookie configured yet. Paste one on the Settings page before adding a video.");
      } else if (err instanceof InvalidUrlError) {
        setError(err.message || "That link isn't a single downloadable video (playlists and live streams aren't supported).");
      } else {
        setError((err as Error).message ?? "Failed to add download.");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="addwrap">
      <form className="paste" onSubmit={handleSubmit}>
        <label className="field">
          <Icon name="link" size="18px" style={{ color: "var(--color-faint)" }} />
          <input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://www.youtube.com/watch?v=..."
            spellCheck={false}
            aria-label="Video URL"
          />
        </label>
        <button className="btn primary" type="submit" disabled={busy || !url.trim()}>
          <Icon name="download" size="18px" />
          {busy ? "Adding…" : "Download now"}
        </button>
      </form>

      <div className="hint">
        <span className="led" />
        Downloads queue immediately using the format preset from Settings — subtitles &amp; a summary are included
        automatically once later phases add them.
      </div>

      {error ? <div className="errline">{error}</div> : null}

      {queued ? (
        <div className="preview">
          <div>
            <div className="pt g4" />
          </div>
          <div>
            <h2>{queued.title || "Queued"}</h2>
            <div className="by">{queued.channel_name || "Added to the download queue"}</div>
            <p style={{ margin: 0, fontSize: 13, color: "var(--color-muted)" }}>
              Watch progress in the download dock, or open the video from the Library once it's done.
            </p>
          </div>
        </div>
      ) : null}
    </div>
  );
}
