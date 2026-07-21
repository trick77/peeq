import type { Job } from "../api/types";

// DownloadDock — the rail-foot "downloading" module from the mockup's
// `.dock` block. Shows the lead (first) active job's progress; the queue
// count next to "Downloading" reflects how many jobs are still in flight.
// Task 14 wires this to live SSE progress; here it just renders whatever
// state is handed to it.
export function DownloadDock({
  jobs,
  progressByJobId,
}: {
  jobs: Job[];
  progressByJobId?: Record<
    number,
    { percent: number; speed: string; eta: string }
  >;
}) {
  const active = jobs.filter(
    (j) => j.state === "pending" || j.state === "running",
  );
  if (active.length === 0) {
    return (
      <div className="dock">
        <div className="dock-top">
          <span>Downloading</span>
        </div>
        <div className="dock-empty">Nothing queued</div>
      </div>
    );
  }

  const lead = active[0];
  const progress = progressByJobId?.[lead.job_id];
  const percent = progress?.percent ?? 0;

  return (
    <div className="dock">
      <div className="dock-top">
        <span>Downloading</span>
        <b>{active.length} queued</b>
      </div>
      <div className="dock-item">
        <div className="dock-thumb" />
        <div className="dock-meta">
          <div className="dock-title">{lead.title ?? lead.video_id}</div>
          <div className="dock-sub">
            {Math.round(percent)}%
            {progress?.speed ? ` · ${progress.speed}` : ""}
            {progress?.eta ? ` · ${progress.eta}` : ""}
          </div>
        </div>
      </div>
      <div className="dock-bar">
        <i style={{ width: `${percent}%` }} />
      </div>
    </div>
  );
}
