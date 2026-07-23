import { Button } from "../ui";
import type { Job, SummaryJob } from "../api/types";

// Queue — everything peeq is working on right now, in two lanes: downloads and
// summaries. This is the machine-state page the Library shed in PR 1 (a video
// in flight is no longer something you browse; it is something you watch
// progress on here). Both lanes are read-only views of state App already owns —
// the download jobs + their live progress SSE, and the summary jobs + their
// live phase SSE — so this component fetches nothing itself.
//
// A download can be cancelled (queued or mid-flight); a summary cannot — it is
// short, unattended, and retried on its own, so there is no button for it.

// phaseLabel turns a summary job's live phase (or its stored state) into a word
// for the row. The phase comes from the "summary" SSE event and advances
// summarizing → embedding without a reload.
function phaseLabel(phase: string | undefined, state: string): string {
  switch (phase) {
    case "summarizing":
      return "Summarizing";
    case "embedding":
      return "Embedding";
    default:
      return state === "running" ? "Summarizing" : "Waiting";
  }
}

export function Queue({
  jobs,
  progressByJobId,
  summaries,
  summaryPhaseByVideoId,
  onCancel,
}: {
  jobs: Job[];
  progressByJobId?: Record<
    number,
    { percent: number; speed: string; eta: string }
  >;
  summaries: SummaryJob[];
  summaryPhaseByVideoId?: Record<string, string>;
  onCancel: (jobId: number) => void;
}) {
  // In-flight downloads only. A terminal job (done/error) is not queue work:
  // errors are retried from the Library card, done ones are in the Library.
  const downloading = jobs.filter(
    (j) => j.state === "pending" || j.state === "running",
  );

  if (downloading.length === 0 && summaries.length === 0) {
    return (
      <p className="queue-empty">
        Nothing in the queue — approve something on Decide and it will show up
        here.
      </p>
    );
  }

  return (
    <>
      {downloading.length > 0 ? (
        <section className="queue-lane">
          <h2>Downloading</h2>
          {downloading.map((j) => {
            const p = progressByJobId?.[j.job_id];
            const running = j.state === "running";
            return (
              <div key={j.job_id} className="qrow">
                <div className="qmeta">
                  <div className="qtitle">{j.title || j.video_id}</div>
                  {j.channel_name ? (
                    <div className="qsub">{j.channel_name}</div>
                  ) : null}
                </div>
                {running && p ? (
                  <div className="qbar">
                    <i style={{ width: `${p.percent}%` }} />
                  </div>
                ) : null}
                <span className="qstate">
                  {running
                    ? p
                      ? `${Math.round(p.percent)}%${p.eta ? ` · ${p.eta}` : ""}`
                      : "Downloading"
                    : "Queued"}
                </span>
                <Button
                  type="button"
                  variant="dangerQuiet"
                  small
                  onClick={() => onCancel(j.job_id)}
                >
                  Cancel
                </Button>
              </div>
            );
          })}
        </section>
      ) : null}

      {summaries.length > 0 ? (
        <section className="queue-lane">
          <h2>Being summarized</h2>
          {summaries.map((s) => (
            <div key={s.id} className="qrow">
              <div className="qmeta">
                <div className="qtitle">{s.title || s.video_id}</div>
                {s.channel_name ? (
                  <div className="qsub">{s.channel_name}</div>
                ) : null}
              </div>
              <span className="qstate">
                {phaseLabel(summaryPhaseByVideoId?.[s.video_id], s.state)}
              </span>
            </div>
          ))}
        </section>
      ) : null}
    </>
  );
}
