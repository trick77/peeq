import { useCallback, useEffect, useMemo, useState } from "react";
import { Button } from "../ui";
import {
  listUpcoming,
  skipScheduledMeta,
  skipScheduledScan,
  type UpcomingItem,
} from "../api";
import { summaryPhaseInfo, SUMMARY_PHASE_COUNT } from "../format";
import type { Job, SummaryJob } from "../api/types";
import { DOT } from "../sep";
import { kindOf, leadCap, parseUTC, plannedWhen, subjectNode } from "./agenda";

// UpNext — everything peeq is about to do, in the order it will do it. It
// absorbs the old Queue page and the projection half of the old Activity page,
// which between them split one question ("what is peeq doing") across two pages
// in two visual languages.
//
// TWO LANES, NOT ONE. Downloads run one at a time on the download worker so
// YouTube doesn't block us; summaries run on a separate worker against the LLM
// and never touch yt-dlp. So a download and a summary routinely run at the same
// moment — at most two jobs, one per lane — and the page is built on that. Each
// lane leads with its running job and lists its own waiting work beneath.
//
// The lanes are headed by KIND ("Downloading", "Summarising"); the schedule
// below is headed by TIME ("Within the hour", "Later today"). That is
// deliberate: the lanes are about what is happening, the schedule about when.
//
// Both lanes read from state App already owns — the download jobs plus their
// live progress SSE, and the summary jobs plus their live phase SSE — so this
// component fetches nothing for them. Only the schedule is fetched here, and it
// is a plain GET rather than a subscription, so App's rule that it owns every
// SSE stream still holds.

// phaseState turns a summary job's live phase (or its stored state) into the
// label + 1-based step its bar shows. A pending job that hasn't started reads
// "Waiting" (step 0, empty bar); a running one shows its live phase —
// summarizing → classifying → embedding → keypoints — from the "summary" SSE
// event, advancing without a reload.
function phaseState(
  phase: string | undefined,
  state: string,
): { label: string; step: number } {
  if (state !== "running" && !phase) return { label: "Waiting", step: 0 };
  // The final "summary done" event carries an empty-string phase (and a stored
  // status of "done"); treat that as the pipeline completing — show the last
  // stage filled rather than snapping the bar back to step 1 for the frame
  // before the finished job is pruned from the list. An *absent* phase
  // (undefined, before the first event) is a fresh job and stays step 1.
  if (phase === "" || phase === "done") {
    return { label: "Key points", step: SUMMARY_PHASE_COUNT };
  }
  return summaryPhaseInfo(phase);
}

// ChannelSub renders a row's channel line — a link to the channel when we know
// its id (and have a handler), plain text otherwise. Mirrors the
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
    <div className="un-sub">
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

// BUCKETS group the timed schedule by how far off it is. The boundaries are
// coarse because the schedule answers "roughly when", not "at which second" —
// and because every instant in it is a plan the workers can miss (a scan waits
// on the cookie gate and the kill-switch).
const HOUR = 3600_000;
const DAY = 86400_000;
function bucketOf(at: string, now: number): string {
  const delta = parseUTC(at).getTime() - now;
  if (delta < HOUR) return "Within the hour";
  if (delta < DAY) return "Later today";
  if (delta < 7 * DAY) return "This week";
  return "Later";
}
const BUCKET_ORDER = ["Within the hour", "Later today", "This week", "Later"];

// SKIPPABLE are the scheduled kinds a row can be skipped on, mapped to the call
// that skips one. Both endpoints double as their own undo when handed an
// instant, so one function per kind covers both directions.
//
// The retention sweep and the yt-dlp version check are deliberately absent: they
// are in-memory tickers with no persisted next-run state, so there is no
// schedule to push out and nothing a skip could write. They do not appear in the
// projection either, so no row is left without an action.
const SKIPPABLE: Record<
  string,
  (id: string, at?: string) => Promise<{ previous_at: string }>
> = {
  scan: skipScheduledScan,
  channel_meta: skipScheduledMeta,
};

// rowKey identifies a scheduled occurrence across refetches. kind + subject_id
// is unique per row — one scan and one metadata refresh per channel — and
// unlike the array index it survives the list reordering under it, which is
// what an undo affordance sitting on a specific row needs.
function rowKey(item: UpcomingItem): string {
  return `${item.kind}:${item.subject_id ?? ""}`;
}

export function UpNext({
  jobs,
  progressByJobId,
  summaries,
  summaryPhaseByVideoId,
  onCancel,
  onOpenChannel,
  stalled,
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
  /**
   * Why YouTube work is stopped, if it is. Only the empty-state wording uses
   * it — the banner above the page is what explains the stall — but silence has
   * to name its cause, or "nothing is running" reads as "nothing is wrong"
   * while the queue is frozen. The cause matters because each one has a
   * different way out, and only the kill-switch has a Resume button to point at.
   */
  stalled?: "youtube" | "disk" | "cookie";
}) {
  const [upcoming, setUpcoming] = useState<UpcomingItem[]>([]);
  const [truncated, setTruncated] = useState(0);
  // Whether the schedule fetch has SETTLED at least once, and whether the last
  // attempt failed. Both matter to the empty state below: an empty `upcoming`
  // means "nothing scheduled" only once we have actually asked. Set-once, never
  // reset on a refetch — the effect re-runs on every lane transition, and a
  // resetting flag would flash the loading line for the whole length of a
  // download.
  const [schedLoaded, setSchedLoaded] = useState(false);
  const [schedFailed, setSchedFailed] = useState(false);
  // scheduleNonce forces a schedule refetch for a reason the lanes cannot
  // signal. The effect below keys on lane transitions because that is what makes
  // the projection stale on its own; skipping a row changes the schedule
  // directly, and nothing in the lanes moves, so without this the skipped row
  // would sit there until a download happened to start or finish.
  const [scheduleNonce, setScheduleNonce] = useState(0);
  // skipped holds the rows this client has just skipped, keyed by rowKey, each
  // with the instant to restore. A skipped row stays in place showing an Undo
  // rather than vanishing: the issue's own objection to a skip with no undo is
  // that it is a trap on a row you might have clicked by accident, and a row
  // that disappears takes its undo with it.
  //
  // Local rather than a toast because peeq has no shared toast component — the
  // Channels page reaches for an inline notice for the same reason. Keeping it
  // on the row also means the undo is next to the thing it undoes.
  const [skipped, setSkipped] = useState<
    Record<string, { previousAt: string; busy: boolean }>
  >({});
  const [skipError, setSkipError] = useState<string | null>(null);
  // now is captured once per render pass for the relative labels; it does not
  // tick, which is fine for a schedule measured in minutes and hours.
  const now = Date.now();

  // In-flight downloads only. A terminal job (done/error) is not upcoming work:
  // errors are retried from the Library card, done ones are in the Library.
  const running = jobs.filter((j) => j.state === "running");
  const waiting = jobs.filter((j) => j.state === "pending");
  const runningSummaries = summaries.filter((s) => s.state === "running");
  const waitingSummaries = summaries.filter((s) => s.state !== "running");

  // What the schedule actually depends on: which jobs exist and what state each
  // is in. Keyed on this rather than on the `jobs`/`summaries` arrays, because
  // App's 3-second poll calls setJobs with a FRESH array every tick while
  // either lane has work — so an identity-keyed effect would refetch the
  // schedule every 3 seconds for the whole length of a download, for a
  // projection that only changes when a job starts, finishes or is cancelled.
  const laneSignature = useMemo(
    () =>
      [
        ...jobs.map((j) => `d${j.job_id}:${j.state}`),
        ...summaries.map((s) => `s${s.id}:${s.state}`),
      ].join(","),
    [jobs, summaries],
  );

  // Keep the schedule in step with the lanes. The projection is a server
  // snapshot of peeq's own timed work, but it goes stale as the lanes move: a
  // scan whose instant has passed lingers with a past label until something
  // refetches. A lane transition is exactly the signal that something happened.
  //
  // scheduleNonce is the second trigger, for the case the lanes cannot see: this
  // page now WRITES to the schedule (skip, and undo), and neither moves a job.
  useEffect(() => {
    let active = true;
    listUpcoming()
      .then((up) => {
        if (!active) return;
        setUpcoming(up.items);
        setTruncated(up.truncated);
        setSchedFailed(false);
        setSchedLoaded(true);
      })
      .catch(() => {
        if (!active) return;
        setSchedFailed(true);
        setSchedLoaded(true);
      });
    return () => {
      active = false;
    };
  }, [laneSignature, scheduleNonce]);

  // skipRow pushes one scheduled occurrence out and remembers where it was, so
  // the row can offer an undo. The refetch is deliberately NOT awaited into the
  // same state update: the server decides the new instant, and re-reading the
  // projection is what proves the skip landed rather than assuming it did.
  const skipRow = useCallback((item: UpcomingItem) => {
    const call = SKIPPABLE[item.kind];
    if (!call || !item.subject_id) return;
    const key = rowKey(item);
    const id = item.subject_id;
    setSkipError(null);
    setSkipped((s) => ({ ...s, [key]: { previousAt: "", busy: true } }));
    call(id)
      .then((res) => {
        setSkipped((s) => ({
          ...s,
          [key]: { previousAt: res.previous_at, busy: false },
        }));
        setScheduleNonce((n) => n + 1);
      })
      .catch(() => {
        // Drop the row back to its normal state: nothing moved, so offering an
        // undo would be offering to restore a skip that never happened.
        setSkipped((s) => {
          const next = { ...s };
          delete next[key];
          return next;
        });
        setSkipError("That could not be skipped. Nothing was changed.");
      });
  }, []);

  // undoSkip hands the previous instant straight back to the same endpoint.
  const undoSkip = useCallback((item: UpcomingItem, previousAt: string) => {
    const call = SKIPPABLE[item.kind];
    if (!call || !item.subject_id) return;
    const key = rowKey(item);
    const id = item.subject_id;
    setSkipError(null);
    setSkipped((s) => ({ ...s, [key]: { previousAt, busy: true } }));
    call(id, previousAt)
      .then(() => {
        setSkipped((s) => {
          const next = { ...s };
          delete next[key];
          return next;
        });
        setScheduleNonce((n) => n + 1);
      })
      .catch(() => {
        // Keep the undo on screen so it can be tried again — the skip itself
        // did happen, so dropping the affordance would strand the user with no
        // way back.
        setSkipped((s) => ({ ...s, [key]: { previousAt, busy: false } }));
        setSkipError("That could not be undone. The item is still skipped.");
      });
  }, []);

  // The schedule renders peeq's own timed housekeeping only. Queued downloads
  // and summaries are the lanes' business — they are rendered above with live
  // progress this projection has never had, so a row here would be a duplicate.
  // The endpoint stopped emitting them, and this guard keeps the page honest
  // against an older backend still serving them.
  const scheduled = useMemo(
    () => upcoming.filter((i) => i.at && !i.approx),
    [upcoming],
  );

  const grouped = useMemo(() => {
    const by = new Map<string, UpcomingItem[]>();
    for (const item of scheduled) {
      const b = bucketOf(item.at as string, now);
      const list = by.get(b);
      if (list) list.push(item);
      else by.set(b, [item]);
    }
    return BUCKET_ORDER.filter((b) => by.has(b)).map((b) => ({
      bucket: b,
      items: by.get(b) as UpcomingItem[],
    }));
    // now is a render-scoped constant; recomputing per render is the point.
  }, [scheduled, now]);

  const nothingInFlight =
    running.length === 0 && waiting.length === 0 && summaries.length === 0;

  // Every empty state names what happens next rather than just saying "nothing
  // here" — and when work is stopped it names the way out, which differs by
  // cause: only the kill-switch has a Resume button above, a full disk needs
  // space freed, and a dead cookie needs replacing in Settings. Pointing at a
  // Resume button that isn't there would be worse than saying nothing.
  //
  // With nothing stalled, an empty page also means nothing is scheduled, and
  // with no subscribed channel there is nothing for peeq to be scheduled to do.
  //
  // None of that can be claimed before the schedule fetch has settled: it
  // starts empty, so an idle queue would otherwise paint "nothing scheduled" to
  // someone with channels subscribed for the frame before the fetch lands — and
  // if the fetch fails the page has no schedule to speak for, so it says that
  // rather than inventing "subscribe to a channel".
  if (nothingInFlight && !schedLoaded) {
    return <p className="un-empty">Loading…</p>;
  }
  if (nothingInFlight && schedFailed && scheduled.length === 0) {
    return <div className="errline">Couldn’t load the schedule.</div>;
  }
  if (nothingInFlight && scheduled.length === 0) {
    return (
      <p className="un-empty">
        {stalled === "youtube"
          ? "Nothing is running — peeq is paused. Resume it above and queued work starts again."
          : stalled === "disk"
            ? "Nothing is running — the disk is full. Free up space and downloads start again."
            : stalled === "cookie"
              ? "Nothing is running — YouTube needs a fresh cookie. Replace it in Settings and downloads start again."
              : "Nothing scheduled yet — subscribe to a channel and peeq will start checking it for you."}
      </p>
    );
  }

  return (
    <>
      {running.length > 0 || waiting.length > 0 ? (
        <section className="un-lane">
          <h2>Downloading</h2>
          {running.map((j) => {
            const p = progressByJobId?.[j.job_id];
            return (
              <div key={j.job_id} className="un-row hero">
                {/* The lead column answers WHEN, and only that. An ETA when
                    yt-dlp has one; "starting" only while there is no progress
                    at all, which is the one moment the honest answer is "it
                    hasn't begun". Once bytes are moving without an ETA yet the
                    column stays empty rather than saying "starting" over a bar
                    at 47% — the bar and the detail line already say it is under
                    way, and a wrong word beats no word only if it is right. */}
                <span className="un-lead">
                  {p?.eta ? (
                    <span className="num">{p.eta}</span>
                  ) : p ? null : (
                    "starting"
                  )}
                </span>
                <div className="un-body">
                  <div className="un-title">{j.title || j.video_id}</div>
                  <ChannelSub
                    name={j.channel_name}
                    id={j.channel_id}
                    onOpen={onOpenChannel}
                  />
                  {/* A download's progress is continuous, so it gets a filling
                      bar. With no progress event yet the bar is a grey stub
                      rather than a spinner — the lead column already says
                      "starting". */}
                  <div className={`un-bar${p ? "" : " stub"}`}>
                    <i style={{ width: `${p ? p.percent : 0}%` }} />
                  </div>
                  <div className="un-detail">
                    {p ? (
                      <>
                        <span className="num">{Math.round(p.percent)}%</span>
                        {p.speed ? (
                          <>
                            {DOT}
                            <span className="num">{p.speed}</span>
                          </>
                        ) : null}
                      </>
                    ) : (
                      "Contacting YouTube"
                    )}
                  </div>
                </div>
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
          {/* Waiting rows read "then", never a rank. With two lanes running
              independently a number would invite a comparison that doesn't
              exist — a waiting summary does not hold up a download — and row
              order already carries position. */}
          {waiting.map((j) => (
            <div key={j.job_id} className="un-row">
              <span className="un-lead">then</span>
              <div className="un-body">
                <div className="un-title">{j.title || j.video_id}</div>
                <ChannelSub
                  name={j.channel_name}
                  id={j.channel_id}
                  onOpen={onOpenChannel}
                />
              </div>
              <Button
                type="button"
                variant="dangerQuiet"
                small
                onClick={() => onCancel(j.job_id)}
              >
                Cancel
              </Button>
            </div>
          ))}
        </section>
      ) : null}

      {summaries.length > 0 ? (
        <section className="un-lane">
          <h2>Summarising</h2>
          {[...runningSummaries, ...waitingSummaries].map((s) => {
            const ps = phaseState(summaryPhaseByVideoId?.[s.video_id], s.state);
            const live = ps.step > 0;
            return (
              <div
                key={s.id}
                className={`un-row${s.state === "running" ? " hero" : ""}`}
              >
                <span className="un-lead">
                  {live ? (
                    <>
                      step <span className="num">{ps.step}</span> of{" "}
                      <span className="num">{SUMMARY_PHASE_COUNT}</span>
                    </>
                  ) : (
                    "then"
                  )}
                </span>
                <div className="un-body">
                  <div className="un-title">{s.title || s.video_id}</div>
                  <ChannelSub
                    name={s.channel_name}
                    id={s.channel_id}
                    onOpen={onOpenChannel}
                  />
                  {/* A summary knows four discrete steps rather than a
                      percentage, so its bar is segmented — same slot and height
                      as the download bar, a different shape because it is a
                      different kind of fact. */}
                  <div className="un-steps" aria-hidden="true">
                    {Array.from({ length: SUMMARY_PHASE_COUNT }, (_, i) => {
                      const n = i + 1;
                      const cls =
                        n < ps.step
                          ? "un-step done"
                          : n === ps.step
                            ? "un-step active"
                            : "un-step";
                      return <i key={n} className={cls} />;
                    })}
                  </div>
                  <div className="un-detail">{ps.label}</div>
                </div>
              </div>
            );
          })}
        </section>
      ) : null}

      {skipError ? (
        <div className="un-edge un-skip-error" role="status">
          {skipError}
        </div>
      ) : null}

      {/* THE SCHEDULE — peeq's own timed housekeeping, grouped by how far off
          it is. Skip is the one action a scheduled row carries: it drops this
          occurrence only, and the next one happens on its normal schedule.
          A skipped row stays put showing Undo rather than vanishing, so a row
          hit by accident can be put back. */}
      {grouped.map((g) => (
        <section key={g.bucket} className="un-sched">
          <h2>{g.bucket}</h2>
          {g.items.map((item) => {
            const k = kindOf(item.kind);
            const key = rowKey(item);
            const skip = skipped[key];
            const skippable = Boolean(SKIPPABLE[item.kind] && item.subject_id);
            // A row is "done being skipped" once the call has returned an
            // instant to restore; until then it is in flight and shows neither
            // the old lead nor an undo it cannot yet perform.
            const isSkipped = Boolean(skip && !skip.busy && skip.previousAt);
            return (
              <div
                key={key}
                className={`un-row planned${isSkipped ? " skipped" : ""}`}
              >
                <span className="un-lead">
                  {isSkipped ? "Skipped" : plannedWhen(item.at, now)}
                </span>
                <div className="un-body">
                  <div className="un-title">
                    {subjectNode(
                      item.kind,
                      item.subject_id,
                      item.subject || k.label,
                      onOpenChannel,
                    )}
                  </div>
                  {item.summary ? (
                    <div className="un-detail">
                      {isSkipped
                        ? `${leadCap(item.summary)}${DOT}not this time`
                        : leadCap(item.summary)}
                    </div>
                  ) : null}
                </div>
                {skippable ? (
                  isSkipped ? (
                    <Button
                      type="button"
                      className="un-skip"
                      variant="ghost"
                      small
                      onClick={() => undoSkip(item, skip.previousAt)}
                    >
                      Undo
                    </Button>
                  ) : (
                    <Button
                      type="button"
                      className="un-skip"
                      variant="ghost"
                      small
                      disabled={Boolean(skip?.busy)}
                      onClick={() => skipRow(item)}
                      aria-label={`Skip ${item.summary || k.label} for ${item.subject || k.label}`}
                    >
                      Skip
                    </Button>
                  )
                ) : null}
              </div>
            );
          })}
        </section>
      ))}

      {truncated > 0 ? (
        <div className="un-edge">+{truncated} more scheduled</div>
      ) : null}
    </>
  );
}
