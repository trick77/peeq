import { useEffect, useState } from "react";
import { Button } from "../ui";
import type { DownloadsStatus } from "../api/downloads";

// DownloadStatusBanner shows why the download queue is stalled, so a paused
// queue is diagnosable at a glance instead of looking silently broken. Renders
// nothing when the queue is healthy. Low disk takes precedence over the cookie
// pause (a full disk blocks downloads regardless of cookie state). The
// YouTube kill-switch pause (youtube_paused) outranks both and is checked
// first, since it is a deliberate all-activity stop the user asked for.
export function DownloadStatusBanner({
  status,
  onFixCookie,
  onResume,
}: {
  status: DownloadsStatus;
  onFixCookie: () => void;
  // Awaited: the button is held down until the kill-switch has answered, and
  // a rejection is shown here rather than dropped on the floor.
  onResume: () => Promise<void>;
}) {
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(false);
  // A failed resume belongs to the pause it was pressed against. When the
  // status moves on — the reason changes, or the pause clears and later
  // re-trips — the message goes with it.
  useEffect(() => {
    setFailed(false);
  }, [status.youtube_paused, status.youtube_pause_reason]);
  if (status.youtube_paused) {
    const auto = status.youtube_pause_reason !== "";
    const resume = () => {
      setBusy(true);
      setFailed(false);
      onResume()
        .catch(() => setFailed(true))
        .finally(() => setBusy(false));
    };
    return (
      <div className="errline" role="status">
        <span className="msg">
          <b>YouTube activity is paused.</b>{" "}
          {auto
            ? status.youtube_pause_reason
            : "You paused all downloads and channel scans."}
          {failed ? " Couldn’t resume. Try again." : null}
        </span>
        <Button type="button" onClick={resume} disabled={busy}>
          Resume
        </Button>
      </div>
    );
  }
  if (status.low_disk) {
    return (
      <div className="errline" role="status">
        Downloads paused — low disk space. Free up space to resume.
      </div>
    );
  }
  if (status.paused) {
    return (
      <div className="errline" role="status">
        Downloads paused — re-paste your YouTube cookie in{" "}
        <button
          type="button"
          onClick={onFixCookie}
          style={{
            background: "none",
            border: "none",
            padding: 0,
            color: "inherit",
            textDecoration: "underline",
            cursor: "pointer",
            font: "inherit",
          }}
        >
          Settings
        </button>
        .
      </div>
    );
  }
  return null;
}
