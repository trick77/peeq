import type { Job } from "../api/types";
import type { DownloadsStatus } from "../api/downloads";

// StatusPanel — the rail-foot module that replaced DownloadDock.
//
// The dock reported one number, "N queued", which was the download queue. That
// read as a contradiction next to a Pending page saying "Nothing pending": two
// words for two different stages of the same pipeline, neither aware of the
// other. The panel names every stage instead, so the counts can never appear to
// disagree.
//
// Two rules the whole component turns on:
//
//   - A zero is absence, not news. A row with nothing in it is not rendered at
//     all, rather than shown as "0". Otherwise an idle peeq reads as a wall of
//     zeroes and the one number that matters gets lost among them.
//   - Only the row that needs a person is accented. "To decide" is a request;
//     downloading and queued are peeq working on its own, which is information.
//     The accent is what makes the first one legible at a glance, so spending
//     it on the other two would cost exactly what it buys.
//
// It deliberately does NOT explain a stall. DownloadStatusBanner already does
// that, with the buttons to act on it. The panel only carries the state word,
// because the banner sits at the top of the page and scrolls away while the
// rail does not — so the two are answering different questions, not repeating
// one answer.
export function StatusPanel({
  jobs,
  progressByJobId,
  pendingCount = 0,
  summarizingCount = 0,
  status,
  onOpenPending,
  onOpenQueue,
}: {
  jobs: Job[];
  /** Live per-job percent/speed/eta from the download SSE feed. */
  progressByJobId?: Record<
    number,
    { percent: number; speed: string; eta: string }
  >;
  /** Uploads waiting on a manual download/ignore decision. */
  pendingCount?: number;
  /** Summary+embedding jobs in flight — the Queue's second lane. */
  summarizingCount?: number;
  /** Why the queue may be stalled; see DownloadsStatus. */
  status?: DownloadsStatus;
  /** Opens the decisions page. Omitted in tests that only assert rendering. */
  onOpenPending?: () => void;
  /** Opens the Queue page. Omitted in tests that only assert rendering. */
  onOpenQueue?: () => void;
}) {
  const running = jobs.filter((j) => j.state === "running");
  const queued = jobs.filter((j) => j.state === "pending");
  // youtube_paused is the global kill-switch and outranks the two
  // download-only stalls, matching DownloadStatusBanner's own precedence.
  const stalled = Boolean(
    status?.youtube_paused || status?.low_disk || status?.paused,
  );
  // "busy" spans every self-driven stage the panel can show — downloads AND
  // summaries — so an idle download queue with a summary still running reads
  // as "working", not "idle", and the "Nothing waiting" line stays hidden.
  const busy = running.length > 0 || queued.length > 0 || summarizingCount > 0;

  const lead = running[0] ?? queued[0];
  const progress = lead ? progressByJobId?.[lead.job_id] : undefined;
  const percent = progress?.percent ?? 0;

  return (
    <div className="dock">
      <div className="dock-top">
        <span>Status</span>
        <b className={stalled ? "stalled" : busy ? "busy" : "idle"}>
          {stalled ? "paused" : busy ? "working" : "idle"}
        </b>
      </div>

      {pendingCount > 0 ? (
        <button
          type="button"
          className="srow hot"
          onClick={onOpenPending}
          disabled={!onOpenPending}
        >
          <i />
          To decide
          <b>{pendingCount}</b>
        </button>
      ) : null}
      {running.length > 0 ? (
        <button
          type="button"
          className="srow run"
          onClick={onOpenQueue}
          disabled={!onOpenQueue}
        >
          <i />
          Downloading
          <b>{running.length}</b>
        </button>
      ) : null}
      {queued.length > 0 ? (
        <button
          type="button"
          className="srow"
          onClick={onOpenQueue}
          disabled={!onOpenQueue}
        >
          <i />
          Queued
          <b>{queued.length}</b>
        </button>
      ) : null}
      {summarizingCount > 0 ? (
        <button
          type="button"
          className="srow"
          onClick={onOpenQueue}
          disabled={!onOpenQueue}
        >
          <i />
          Summarizing
          <b>{summarizingCount}</b>
        </button>
      ) : null}

      {/* Every row can be absent at once — nothing to decide, nothing in
          flight — and a panel showing only the word "idle" looks broken
          rather than calm. One line says the same thing on purpose. */}
      {pendingCount === 0 && !busy ? (
        <div className="dock-empty">Nothing waiting</div>
      ) : null}

      {running.length > 0 ? (
        <>
          <div className="dock-bar">
            <i style={{ width: `${percent}%` }} />
          </div>
          <div className="dock-sub">
            {lead?.title ?? lead?.video_id}
            {progress ? ` · ${Math.round(percent)}%` : ""}
            {progress?.eta ? ` · ${progress.eta}` : ""}
          </div>
        </>
      ) : null}
    </div>
  );
}
