import { Button } from "../ui";
import { summaryPhaseInfo, SUMMARY_PHASE_COUNT } from "../format";
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

// phaseState turns a summary job's live phase (or its stored state) into the
// label + 1-based step the row's meter shows. A pending job that hasn't started
// reads "Waiting" (step 0, no meter); a running one shows its live phase —
// summarizing → classifying → embedding → keypoints — from the "summary" SSE
// event, advancing without a reload.
function phaseState(
  phase: string | undefined,
  state: string,
): { label: string; step: number } {
  if (state !== "running" && !phase) return { label: "Waiting", step: 0 };
  // The final "summary done" event carries an empty-string phase (and a stored
  // status of "done"); treat that as the pipeline completing — show the last
  // stage filled rather than snapping the meter back to step 1 for the frame
  // before the finished job is pruned from the list. An *absent* phase
  // (undefined, before the first event) is a fresh job and stays step 1.
  if (phase === "" || phase === "done") {
    return { label: "Key points", step: SUMMARY_PHASE_COUNT };
  }
  return summaryPhaseInfo(phase);
}

// ChannelSub renders the row's channel line — a link to the channel when we
// know its id (and have a handler), plain text otherwise. Mirrors the
// chan-link/chan-name convention VideoCard and Player use; a video added
// individually may carry no channel id, in which case it stays plain text.
function ChannelSub({
  name,
  id,
  onOpen,
}: {
  name?: string;
  id?: string;
  onOpen?: (channelId: string) => void;
}) {
  const text = name || id;
  if (!text) return null;
  return (
    <div className="qsub">
      {onOpen && id ? (
        <button type="button" className="chan-link" onClick={() => onOpen(id)}>
          {text}
        </button>
      ) : (
        text
      )}
    </div>
  );
}

export function Queue({
  jobs,
  progressByJobId,
  summaries,
  summaryPhaseByVideoId,
  onCancel,
  onOpenChannel,
}: {
  jobs: Job[];
  progressByJobId?: Record<
    number,
    { percent: number; speed: string; eta: string }
  >;
  summaries: SummaryJob[];
  summaryPhaseByVideoId?: Record<string, string>;
  onCancel: (jobId: number) => void;
  onOpenChannel?: (channelId: string) => void;
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
                  <ChannelSub
                    name={j.channel_name}
                    id={j.channel_id}
                    onOpen={onOpenChannel}
                  />
                </div>
                {running && p ? (
                  <div className="qbar">
                    <i style={{ width: `${p.percent}%` }} />
                  </div>
                ) : null}
                <span className="qstate">
                  {running
                    ? p
                      ? `${Math.round(p.percent)}%${p.speed ? ` · ${p.speed}` : ""}${p.eta ? ` · ${p.eta} left` : ""}`
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
          {summaries.map((s) => {
            const ps = phaseState(summaryPhaseByVideoId?.[s.video_id], s.state);
            const live = ps.step > 0;
            return (
              <div key={s.id} className="qrow">
                <div className="qmeta">
                  <div className="qtitle">{s.title || s.video_id}</div>
                  <ChannelSub
                    name={s.channel_name}
                    id={s.channel_id}
                    onOpen={onOpenChannel}
                  />
                </div>
                <div className="qphase">
                  <span className="qphase-word">
                    {ps.label}
                    {live ? (
                      <span className="qphase-frac">
                        {ps.step}/{SUMMARY_PHASE_COUNT}
                      </span>
                    ) : null}
                  </span>
                  <div className="qphase-dots" aria-hidden="true">
                    {Array.from({ length: SUMMARY_PHASE_COUNT }, (_, i) => {
                      const n = i + 1;
                      const cls =
                        n < ps.step
                          ? "qdot done"
                          : n === ps.step
                            ? "qdot active"
                            : "qdot";
                      return <i key={n} className={cls} />;
                    })}
                  </div>
                </div>
              </div>
            );
          })}
        </section>
      ) : null}
    </>
  );
}
