import { useCallback, useEffect, useMemo, useState } from "react";
import { Button } from "../ui";
import {
  listUpcoming,
  skipScheduledMeta,
  skipScheduledScan,
  type UpcomingItem,
} from "../api";
import { SearchField } from "../components/SearchField";
import {
  shortWatchLink,
  summaryPhaseInfo,
  SUMMARY_PHASE_COUNT,
  videoLabel,
  watchURL,
} from "../format";
import { Icon } from "../icons";
import type { Job, SummaryJob } from "../api/types";
import { DOT } from "../sep";
import {
  clockOf,
  kindOf,
  leadCap,
  parseUTC,
  plannedWhen,
  subjectNode,
} from "./agenda";

// UpNext — everything peeq is about to do, in the order it will do it. It
// absorbs the old Queue page and the projection half of the old Activity page,
// which between them split one question ("what is peeq doing") across two pages
// in two visual languages.
//
// THE SAME ROW AS HISTORY, READ FORWARDS. Up next and History describe the same
// work either side of the moment it happens, so they are now the same timeline:
// the clock gutter, the kind icon on a ringed node, the connector joining a
// group, the two-line body whose detail opens with the kind word, and the
// relative label on the right. Only the tense differs — a gutter clock is when
// something IS DUE rather than when it happened, the right column says
// "downloading" or "in 40m" rather than "45m ago", and a running row's node
// ring lights accent instead of green. Before this the two pages used two
// unrelated layouts for one subject.
//
// TWO LANES, STILL. Downloads run one at a time on the download worker so
// YouTube doesn't block us; summaries run on a separate worker against the LLM
// and never touch yt-dlp. So a download and a summary routinely run at the same
// moment — at most two jobs, one per lane. The lanes are no longer two headed
// sections: what is RUNNING groups under "Now" and what is queued under
// "Queued", with the node icon and the kind word saying which lane each row
// belongs to. Grouping by state rather than by kind is what lets the page share
// History's day-group structure, and it answers the question the page is opened
// for — what is happening now — in one glance rather than two.
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

// FILTERS narrows the page to one kind of work. Deliberately the same chip row,
// vocabulary and default ("All") as History, so the two halves of "what is peeq
// doing" are filtered the same way rather than each inventing its own controls.
//
// Two of History's chips are absent on purpose. "Cleanup" has nothing to match:
// retention is a sweep peeq runs on its own, with no scheduled instant to
// project. "Problems only" has nothing either — nothing here has happened yet,
// so nothing can have failed, which is the same reason the old Activity page
// had to blank its whole projection under that filter.
const FILTERS: { id: string; label: string }[] = [
  { id: "all", label: "All" },
  { id: "download", label: "Downloads" },
  { id: "summary", label: "Summaries" },
  { id: "scan", label: "Scans" },
  { id: "channel_meta", label: "Metadata" },
];

// The kinds the schedule can ever contain: the projection is peeq's own timed
// housekeeping, which is scans and metadata refreshes and nothing else.
const SCHED_KINDS = new Set(["scan", "channel_meta"]);

// matches is the search box's filter. Client-side, and honestly so: unlike
// History's log, everything Up next can show is already in memory — two short
// job lists and a capped projection — so there is nothing off-screen for a
// server query to reach. It matches the same fields the rows display: the
// title/subject, the channel name, and — on a scheduled row — the line naming
// the work, which is passed in as the string the row actually renders rather
// than as the raw summary, so display and search cannot drift apart.
function matches(search: string, ...fields: (string | undefined)[]): boolean {
  if (!search) return true;
  const q = search.toLowerCase();
  return fields.some((f) => (f ?? "").toLowerCase().includes(q));
}

// planOf is the line a scheduled row puts under its subject: the worker's own
// wording for the work ("channel scan"), capitalised, or the kind's label when
// the worker sent none — an older backend, or a kind added there before it is
// given a phrase. ONE function so the search predicate filters on the exact
// string the row displays; computing it twice is how a row ends up showing
// "Metadata" that a search for "metadata" refuses to find.
function planOf(item: UpcomingItem): string {
  return leadCap(item.summary?.trim() || "") || kindOf(item.kind).label;
}

export function UpNext({
  jobs,
  progressByJobId,
  summaries,
  summaryPhaseByVideoId,
  search = "",
  onSearchChange,
  onCancel,
  onOpenChannel,
  onOpenVideo,
  stalled,
}: {
  jobs: Job[];
  progressByJobId?: Record<
    number,
    { percent: number; speed: string; eta: string }
  >;
  summaries: SummaryJob[];
  summaryPhaseByVideoId?: Record<string, string>;
  /**
   * The search box's text. Owned by App so it survives navigating away and
   * back, the same as the Library's and the Channels list's.
   */
  search?: string;
  onSearchChange?: (value: string) => void;
  onCancel: (jobId: number) => void;
  onOpenChannel?: (channelId: string) => void;
  /** Opens a video in the player, as History's rows do. Optional for tests. */
  onOpenVideo?: (videoId: string) => void;
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
  const q = search.trim();
  // Starts at "all" on every mount rather than persisting: a filter remembered
  // from last time would answer a narrower question without saying so.
  const [filter, setFilter] = useState("all");

  // Which sections the current filter admits. The lanes are kinds too, so
  // "Downloads" hides the summary lane and the whole schedule rather than just
  // dimming them.
  const showDownloads = filter === "all" || filter === "download";
  const showSummaries = filter === "all" || filter === "summary";
  // Whether the schedule is part of the current view at all. Only scans and
  // metadata refreshes are ever projected, so under "Downloads" or "Summaries"
  // the schedule is not merely empty — it is out of scope, and neither its
  // loading state nor its failure is this page's news to report.
  const showSched = filter === "all" || SCHED_KINDS.has(filter);

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
    () =>
      upcoming.filter(
        (i) => i.at && !i.approx && (filter === "all" || filter === i.kind),
      ),
    [upcoming, filter],
  );

  // The two search predicates, named once and used by both the rows and the chip
  // counts below. A count that searched different fields than the row it counts
  // is the drift this guards against.
  const jobMatches = (j: {
    title?: string;
    video_id?: string;
    channel_name?: string;
  }) => matches(q, j.title, j.video_id, j.channel_name);
  const schedMatches = (i: UpcomingItem) => matches(q, i.subject, planOf(i));

  const grouped = useMemo(() => {
    const by = new Map<string, UpcomingItem[]>();
    for (const item of scheduled) {
      // Subject AND plan line, because the row shows both: the channel name on
      // top and the work itself ("Channel scan") beneath. The rule is that a
      // search box may only find what the page can show — which cuts both
      // ways, so a field the row displays has to be searchable, including the
      // kind label the plan line falls back to. While the summary was off the
      // row this matched the subject alone.
      if (!matches(q, item.subject, planOf(item))) continue;
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
  }, [scheduled, now, q]);

  // The lanes, split by state rather than by kind, and narrowed by BOTH
  // controls: the kind chips decide which lanes are on the page at all, the
  // search box decides which of their rows survive. Downloads lead summaries
  // within each group — the download worker is the one holding the YouTube
  // gate, so it is the row you look for first.
  const liveRows = [
    ...(showDownloads ? running : [])
      .filter(jobMatches)
      .map((j) => ({ kind: "download" as const, job: j })),
    ...(showSummaries ? runningSummaries : [])
      .filter(jobMatches)
      .map((s) => ({ kind: "summary" as const, job: s })),
  ];
  const queuedRows = [
    ...(showDownloads ? waiting : [])
      .filter(jobMatches)
      .map((j) => ({ kind: "download" as const, job: j })),
    ...(showSummaries ? waitingSummaries : [])
      .filter(jobMatches)
      .map((s) => ({ kind: "summary" as const, job: s })),
  ];

  // What each chip would show if you clicked it: the two lanes it admits plus the
  // schedule rows of its kind, all under the current search. Counted from the same
  // arrays and the same predicates the sections render from, so a number can never
  // disagree with the rows beneath it. "All" is the sum of the rest, since every
  // row belongs to exactly one kind.
  const countFor = (f: string) => {
    const lanes =
      (f === "all" || f === "download"
        ? [...running, ...waiting].filter(jobMatches).length
        : 0) +
      (f === "all" || f === "summary"
        ? [...runningSummaries, ...waitingSummaries].filter(jobMatches).length
        : 0);
    const sched = upcoming.filter(
      (i) =>
        i.at && !i.approx && (f === "all" || f === i.kind) && schedMatches(i),
    ).length;
    return lanes + sched;
  };

  // ChannelBit is the channel name inside a detail line — a link when we know
  // the channel's id, plain text otherwise. A video added individually may
  // carry no channel id.
  function channelBit(name?: string, id?: string) {
    const text = name || id;
    if (!text) return null;
    return (
      <>
        {DOT}
        {onOpenChannel && id ? (
          <button
            type="button"
            className="ag-link"
            onClick={() => onOpenChannel(id)}
          >
            {text}
          </button>
        ) : (
          text
        )}
      </>
    );
  }

  // Emptiness is measured through the CHIPS but around the SEARCH BOX. With
  // "Downloads" selected, a summary in the other lane is not something this page
  // is currently showing, so it must not count as "there is work here". A search
  // that matches nothing is the opposite case: the work exists, you just filtered
  // past it, and answering that with "subscribe to a channel" would be a lie —
  // so the query is left out here and answered separately below.
  const laneEmpty =
    (!showDownloads || (running.length === 0 && waiting.length === 0)) &&
    (!showSummaries || summaries.length === 0);
  const nothingToShow = laneEmpty && scheduled.length === 0;

  // The toolbar renders on EVERY branch below, including the empty ones. An
  // early return without it would strand anyone who filtered or searched their
  // way to nothing: the page would say "nothing" with no control left to undo
  // it.
  const header = (
    <>
      {/* Same toolbar as every other list page: search leads, chips beneath.
          The search is client-side, over work already wholly in memory. */}
      <div className="listbar">
        <SearchField
          value={search}
          onChange={(v) => onSearchChange?.(v)}
          placeholder="Search up next"
          label="Search up next"
        />
      </div>
      <div className="chips">
        {FILTERS.map((f) => (
          <button
            key={f.id}
            type="button"
            className={`chip${filter === f.id ? " on" : ""}`}
            onClick={() => setFilter(f.id)}
          >
            {f.label} <span className="n">{countFor(f.id)}</span>
          </button>
        ))}
      </div>
    </>
  );

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
  //
  // Both of those are claims about the SCHEDULE, so they are only made by a
  // filter the schedule is in: under "Downloads" with nothing downloading, the
  // answer is "nothing of that kind", not "Loading…" and not "couldn't load the
  // schedule" — the schedule holds no downloads either way.
  if (nothingToShow && showSched && !schedLoaded) {
    return (
      <>
        {header}
        <p className="un-empty">Loading…</p>
      </>
    );
  }
  if (nothingToShow && showSched && schedFailed) {
    return (
      <>
        {header}
        <div className="errline">Couldn’t load the schedule.</div>
      </>
    );
  }
  if (nothingToShow) {
    return (
      <>
        {header}
        <p className="un-empty">
          {filter !== "all"
            ? "Nothing of that kind is queued or scheduled."
            : stalled === "youtube"
              ? "Nothing is running — Peeq is paused. Resume it above and queued work starts again."
              : stalled === "disk"
                ? "Nothing is running — the disk is full. Free up space and downloads start again."
                : stalled === "cookie"
                  ? "Nothing is running — YouTube needs a fresh cookie. Replace it in Settings and downloads start again."
                  : "Nothing scheduled yet — subscribe to a channel and Peeq will start checking it for you."}
        </p>
      </>
    );
  }

  // There IS work on this page; the search box is what emptied it. Naming the
  // chip too when both are narrowing, or the message points at the wrong
  // control.
  const nothingMatches =
    q !== "" &&
    liveRows.length === 0 &&
    queuedRows.length === 0 &&
    grouped.length === 0;

  return (
    <>
      {header}

      {/* Above the timeline rather than on the row that failed: the row it
          belongs to may have moved or gone by the time the call comes back, and
          a notice that moves with it would be easy to miss. */}
      {skipError ? (
        <div className="ag-edge ag-edge-err" role="status">
          {skipError}
        </div>
      ) : null}

      {nothingMatches ? (
        <p className="un-empty">
          {filter === "all"
            ? `Nothing queued or scheduled matches “${q}”.`
            : `Nothing of that kind matches “${q}”.`}
        </p>
      ) : (
        <div className="agenda">
          {liveRows.length > 0 ? (
            <div className="ag-day">
              <div className="ag-daysep">
                <span>Now</span>
                <i />
              </div>
              {liveRows.map((row) =>
                row.kind === "download" ? (
                  <DownloadRow
                    key={`d${row.job.job_id}`}
                    job={row.job}
                    progress={progressByJobId?.[row.job.job_id]}
                    live
                    onCancel={onCancel}
                    onOpenVideo={onOpenVideo}
                    channelBit={channelBit}
                    stalled={stalled}
                  />
                ) : (
                  <SummaryRow
                    key={`s${row.job.id}`}
                    job={row.job}
                    phase={summaryPhaseByVideoId?.[row.job.video_id]}
                    live
                    onOpenVideo={onOpenVideo}
                    channelBit={channelBit}
                  />
                ),
              )}
            </div>
          ) : null}

          {queuedRows.length > 0 ? (
            <div className="ag-day">
              <div className="ag-daysep">
                <span>Queued</span>
                <i />
              </div>
              {queuedRows.map((row) =>
                row.kind === "download" ? (
                  <DownloadRow
                    key={`d${row.job.job_id}`}
                    job={row.job}
                    progress={undefined}
                    live={false}
                    onCancel={onCancel}
                    onOpenVideo={onOpenVideo}
                    channelBit={channelBit}
                    stalled={stalled}
                  />
                ) : (
                  <SummaryRow
                    key={`s${row.job.id}`}
                    job={row.job}
                    phase={summaryPhaseByVideoId?.[row.job.video_id]}
                    live={false}
                    onOpenVideo={onOpenVideo}
                    channelBit={channelBit}
                  />
                ),
              )}
            </div>
          ) : null}

          {/* THE SCHEDULE — peeq's own timed housekeeping, grouped by how far
              off it is. Unlike the lanes above, these rows have an actual
              instant, so the gutter carries their wall clock exactly as
              History's does and the right column carries how far off it is.

              Skip is the one action a scheduled row carries: it drops this
              occurrence only, and the next one happens on its normal schedule.
              It shares the fourth column with the relative label rather than
              taking a fifth, so these rows keep History's geometry — the same
              column a download's Cancel uses. A skipped row stays put showing
              Undo rather than vanishing, so a row hit by accident can be put
              back. */}
          {grouped.map((g) => (
            <div key={g.bucket} className="ag-day">
              <div className="ag-daysep">
                <span>{g.bucket}</span>
                <i />
              </div>
              {g.items.map((item) => {
                const k = kindOf(item.kind);
                // Keyed by kind + subject rather than by index: the undo sits on
                // one specific row, and the list reorders under it every time
                // the projection is refetched.
                const key = rowKey(item);
                const skip = skipped[key];
                const skippable = Boolean(
                  SKIPPABLE[item.kind] && item.subject_id,
                );
                // A row is "done being skipped" once the call has returned an
                // instant to restore; until then it is in flight and shows
                // neither a moved time nor an undo it cannot yet perform.
                const isSkipped = Boolean(
                  skip && !skip.busy && skip.previousAt,
                );
                return (
                  <div
                    key={key}
                    className={`ag-row planned${isSkipped ? " skipped" : ""}`}
                  >
                    <span className="ag-clock">
                      {clockOf(item.at as string)}
                    </span>
                    {/* Labelled, unlike History's nodes. Both rows now name
                        the kind in words below, so on both pages the glyph is
                        strictly a repeat — but History's node is a coloured
                        outcome ring a sighted reader reads as meaning, and
                        this one isn't, so the label is the cheaper of the two
                        inconsistencies to keep. */}
                    <span className="ag-node">
                      <Icon name={k.icon} size="12px" label={k.label} />
                    </span>
                    {/* Two lines, as History has: the subject, then what is
                        actually going to happen to it. This row was briefly one
                        line on the theory that the glyph said it — but a glyph
                        names a kind, not a deed, and "Veritasium · in 40m" with
                        a magnifying glass beside it does not tell you peeq is
                        about to look for new videos. The wording is the
                        worker's own ("channel scan", "metadata refresh"), never
                        a second vocabulary here that could drift from it —
                        the same rule History's detailParts follows. */}
                    <div className="ag-body">
                      <div className="ag-subject">
                        {subjectNode(
                          item.kind,
                          item.subject_id,
                          item.subject || k.label,
                          onOpenChannel,
                          onOpenVideo,
                        )}
                      </div>
                      {/* .ag-plan drops it to the same grey as the relative
                          label across from it — see the rule in index.css. */}
                      <div className="ag-detail ag-plan">
                        <span className="ag-kind">{planOf(item)}</span>
                      </div>
                    </div>
                    {/* Both live in column four and cross-fade. Rendered
                        together rather than swapped, so the button stays in the
                        tab order at rest — a control that only exists on hover
                        cannot be reached by keyboard. */}
                    <span className="ag-when">
                      {isSkipped ? "Skipped" : plannedWhen(item.at, now)}
                    </span>
                    {skippable ? (
                      isSkipped ? (
                        <Button
                          type="button"
                          className="ag-skip"
                          variant="ghost"
                          small
                          onClick={() => undoSkip(item, skip.previousAt)}
                        >
                          Undo
                        </Button>
                      ) : (
                        <Button
                          type="button"
                          className="ag-skip"
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
            </div>
          ))}
        </div>
      )}

      {/* The server's count is how many items the merge dropped across every
          kind at once, so it can only be told honestly with BOTH narrowing
          controls off. Under "Scans" it would silently include the dropped
          metadata refreshes; under a search it would count work the page is
          hiding. */}
      {truncated > 0 && q === "" && filter === "all" ? (
        <div className="ag-edge">+{truncated} more scheduled</div>
      ) : null}
    </>
  );
}

// VideoIdLink stands in for the channel name on a row that has neither a title
// nor a channel yet: the YouTube id, one size down, linking to the video on
// YouTube. It is the row's receipt — the only way to check that the link that
// was pasted is the video that was queued — so it opens in a new tab rather
// than taking the reader off peeq.
function VideoIdLink({ id }: { id: string }) {
  if (!id) return null;
  return (
    <>
      {DOT}
      <a
        className="ag-id"
        href={watchURL(id)}
        target="_blank"
        rel="noreferrer"
        // The row is inside a page where the subject line is normally the link;
        // spell out what this one does, since "youtu.be/aircAruvnKk" read aloud
        // is not a destination.
        title="Open on YouTube"
      >
        {shortWatchLink(id)}
      </a>
    </>
  );
}

// DownloadRow is one download, running or queued. The gutter answers WHEN, on
// every row: yt-dlp's ETA where it has one, else the bare tense — "now" for
// what is running, "then" for what is waiting. It is never left blank. History
// leans on this column to place a row in its day, and a gutter that came up
// empty on half the rows is the one thing that would stop the two pages reading
// as the same component.
function DownloadRow({
  job,
  progress,
  live,
  onCancel,
  onOpenVideo,
  channelBit,
  stalled,
}: {
  job: Job;
  progress?: { percent: number; speed: string; eta: string };
  live: boolean;
  onCancel: (jobId: number) => void;
  onOpenVideo?: (videoId: string) => void;
  channelBit: (name?: string, id?: string) => React.ReactNode;
  stalled?: "youtube" | "disk" | "cookie";
}) {
  // A row whose video has no title yet is a video that was added by URL and
  // hasn't reached the front of the queue. Which placeholder it gets depends on
  // whether the queue is moving at all: with YouTube work stopped, "reading
  // details" would be a claim about work nobody is doing.
  const label = videoLabel(job.title, stalled ? "stalled" : "fetching");
  return (
    <div className={`ag-row${live ? " live" : ""}`}>
      <span className="ag-clock">{live ? progress?.eta || "now" : "then"}</span>
      <span className="ag-node">
        <Icon name="download" size="12px" />
      </span>
      <div className="ag-body">
        <div
          className={`ag-subject${label.placeholder ? " placeholder" : ""}${label.pending ? " pending" : ""}`}
        >
          {/* A placeholder is not a link: there is nothing in the player to
              open yet, and the id below is the affordance that IS useful. */}
          {onOpenVideo && job.video_id && !label.placeholder ? (
            <button
              type="button"
              className="ag-link"
              onClick={() => onOpenVideo(job.video_id)}
            >
              {label.text}
            </button>
          ) : (
            label.text
          )}
        </div>
        <div className="ag-detail">
          <span className="ag-kind">{live ? "Downloading" : "Download"}</span>
          {label.placeholder ? (
            <VideoIdLink id={job.video_id} />
          ) : (
            channelBit(job.channel_name, job.channel_id)
          )}
        </div>
        {live ? (
          <>
            {/* A download's progress is continuous, so it gets a filling bar.
                With no progress event yet the bar is a grey stub rather than a
                spinner — the detail line below says what it is waiting on. */}
            <div className={`un-bar${progress ? "" : " stub"}`}>
              <i style={{ width: `${progress ? progress.percent : 0}%` }} />
            </div>
            <div className="ag-detail">
              {progress ? (
                <>
                  <span className="num">{Math.round(progress.percent)}%</span>
                  {progress.speed ? (
                    <>
                      {DOT}
                      <span className="num">{progress.speed}</span>
                    </>
                  ) : null}
                </>
              ) : label.placeholder ? (
                // The subject line already says peeq is talking to YouTube;
                // repeating it here would spend the row's second line saying
                // nothing new.
                "Starting the download"
              ) : (
                "Contacting YouTube"
              )}
            </div>
          </>
        ) : null}
      </div>
      <Button
        type="button"
        variant="dangerQuiet"
        small
        onClick={() => onCancel(job.job_id)}
      >
        Cancel
      </Button>
    </div>
  );
}

// SummaryRow is one summary, running or queued. The LLM offers no ETA, so the
// gutter carries the bare tense — "now" or "then" — and the right column
// carries the step it has reached.
function SummaryRow({
  job,
  phase,
  live,
  onOpenVideo,
  channelBit,
}: {
  job: SummaryJob;
  phase?: string;
  live: boolean;
  onOpenVideo?: (videoId: string) => void;
  channelBit: (name?: string, id?: string) => React.ReactNode;
}) {
  const ps = phaseState(phase, job.state);
  const started = ps.step > 0;
  // Reached through the same helper as the download row, though it should never
  // fire here: a summary exists because a download finished, and the metadata
  // preflight runs long before that. If it ever does, both lanes say the same
  // thing rather than one of them falling back to a bare id.
  const label = videoLabel(job.title);
  return (
    <div className={`ag-row${live ? " live" : ""}`}>
      <span className="ag-clock">{live ? "now" : "then"}</span>
      <span className="ag-node">
        <Icon name="alignLeft" size="12px" />
      </span>
      <div className="ag-body">
        <div
          className={`ag-subject${label.placeholder ? " placeholder" : ""}${label.pending ? " pending" : ""}`}
        >
          {onOpenVideo && job.video_id && !label.placeholder ? (
            <button
              type="button"
              className="ag-link"
              onClick={() => onOpenVideo(job.video_id)}
            >
              {label.text}
            </button>
          ) : (
            label.text
          )}
        </div>
        <div className="ag-detail">
          <span className="ag-kind">{live ? "Summarising" : "Summary"}</span>
          {label.placeholder ? (
            <VideoIdLink id={job.video_id} />
          ) : (
            channelBit(job.channel_name, job.channel_id)
          )}
        </div>
        {live ? (
          <>
            {/* A summary knows four discrete steps rather than a percentage, so
                its bar is segmented — same slot and height as the download bar,
                a different shape because it is a different kind of fact. */}
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
            <div className="ag-detail">{ps.label}</div>
          </>
        ) : null}
      </div>
      {/* Waiting rows read "waiting", never a rank. With two lanes running
          independently a number would invite a comparison that doesn't exist —
          a waiting summary does not hold up a download — and row order already
          carries position. */}
      <span className="ag-when">
        {started ? (
          <>
            step <span className="num">{ps.step}</span> of{" "}
            <span className="num">{SUMMARY_PHASE_COUNT}</span>
          </>
        ) : (
          "waiting"
        )}
      </span>
    </div>
  );
}
