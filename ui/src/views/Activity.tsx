import { useEffect, useMemo, useState } from "react";
import {
  listActivity,
  listUpcoming,
  type ActivityEvent,
  type Job,
  type SummaryJob,
  type UpcomingItem,
} from "../api";
import { Icon, type IconName } from "../icons";
import { summaryPhaseLabel } from "../format";
import { DOT } from "../sep";

// Activity — two sections, each with one consistent order (a single folded
// timeline through a *now* marker reversed direction at the seam, which read as
// broken). "Recent activity" (top): what actually happened (the durable log
// from /api/activity), newest first — a plain feed, and the page's lead, since
// "what has peeq been up to" is the question this view is opened to answer.
// "Up next" (bottom): running items at the front, then queued/scheduled work
// soonest-first (the live projection from /api/activity/upcoming plus the
// running items App holds). The whole view is read-only — nothing in either
// section is actionable — so it deliberately carries no buttons.
//
// Three disjoint states never render twice: TERMINAL work comes from the event
// log (Recent activity), RUNNING work from App's live jobs/summaries (top of Up
// next), PENDING work from the upcoming projection (rest of Up next).

// parseUTC reads the backend's "2006-01-02 15:04:05" UTC text into a Date.
function parseUTC(at: string): Date {
  return new Date(at.replace(" ", "T") + "Z");
}

// relTime renders a compact relative label against now. Coarse on purpose — the
// agenda is about sequence, not exact clock times. Future and past are worded
// separately so a scheduled task never reads as "ago": a scan due in 40 minutes
// is "in 40m", and one whose instant just passed but hasn't been claimed yet is
// "soon", never "1m ago".
function relTime(date: Date, now: number): string {
  const secs = Math.round((date.getTime() - now) / 1000);
  const a = Math.abs(secs);
  const mag =
    a < 3600
      ? `${Math.max(1, Math.round(a / 60))}m`
      : a < 86400
        ? `${Math.round(a / 3600)}h`
        : `${Math.round(a / 86400)}d`;
  if (secs >= 0) return a < 60 ? "soon" : `in ${mag}`;
  return a < 45 ? "just now" : `${mag} ago`;
}

// plannedWhen labels a FUTURE (upcoming) row. It never says "ago": an item with
// no instant is "up next", one still ahead is "in 40m", and one whose scheduled
// instant has already passed — an overdue task the worker hasn't reached yet
// (e.g. because YouTube is paused) — is "soon", not "1h ago". Only the past log
// uses relTime's "ago" wording.
function plannedWhen(atStr: string | undefined, now: number): string {
  if (!atStr) return "up next";
  const secs = Math.round((parseUTC(atStr).getTime() - now) / 1000);
  return secs < 60 ? "soon" : relTime(parseUTC(atStr), now);
}

// leadCap uppercases a leading lowercase ASCII letter so every detail line's
// first word reads as a capital, without mangling a number ("3 new") or a term
// that is already cased ("512 MB").
function leadCap(s: string): string {
  const c = s.charCodeAt(0);
  return c >= 97 && c <= 122 ? s[0].toUpperCase() + s.slice(1) : s;
}

// KIND maps an event/projection kind to its rail icon and display label. The
// icon's shape carries the kind so the text never has to name it; the label is
// the fallback subject for a kindless event (retention, yt-dlp).
const KIND: Record<string, { icon: IconName; label: string }> = {
  scan: { icon: "search", label: "Scan" },
  channel_meta: { icon: "refresh", label: "Metadata" },
  download: { icon: "download", label: "Download" },
  summary: { icon: "alignLeft", label: "Summary" },
  retention: { icon: "trash", label: "Cleanup" },
  ytdlp: { icon: "settings", label: "yt-dlp" },
  access: { icon: "warning", label: "Access" },
};
function kindOf(k: string): { icon: IconName; label: string } {
  return KIND[k] ?? { icon: "clock", label: k };
}

// CHANNEL_KINDS are the agenda rows whose subject is a channel, and so the only
// ones whose name links anywhere. A download or summary row names a video, which
// the agenda does not link — it is a log of work, not a video browser.
const CHANNEL_KINDS = new Set(["scan", "channel_meta"]);

// subjectNode renders a row's subject, as a link to the channel page when the
// row is about a channel and we know its id. Both halves of the agenda use it:
// a linked name above the "now" marker and dead text below it would be exactly
// the kind of inconsistency the agenda is supposed to avoid.
function subjectNode(
  kind: string,
  subjectID: string | undefined,
  text: string,
  onOpenChannel?: (id: string) => void,
) {
  if (!subjectID || !onOpenChannel || !CHANNEL_KINDS.has(kind)) return text;
  const id = subjectID;
  return (
    <button type="button" className="ag-link" onClick={() => onOpenChannel(id)}>
      {text}
    </button>
  );
}

// eventDetail joins an event's summary and detail into one lead-capitalized
// line; the kind is shown by the icon, not repeated in words.
function eventDetail(e: ActivityEvent): string {
  const text = [e.summary, e.detail].filter(Boolean).join(DOT);
  return text ? leadCap(text) : "";
}

// The agenda is a fixed window: the newest HISTORY_LIMIT log rows and the
// nearest PLANNED_LIMIT scheduled rows (max 20 total). Beyond these, a "+N" edge
// hints at what's hidden rather than paging.
const HISTORY_LIMIT = 10;
const PLANNED_LIMIT = 10;

const FILTERS: { id: string; label: string }[] = [
  { id: "all", label: "All" },
  { id: "scan", label: "Scans" },
  { id: "download", label: "Downloads" },
  { id: "summary", label: "Summaries" },
  { id: "retention", label: "Cleanup" },
  { id: "problems", label: "Problems only" },
];

export function Activity({
  live,
  jobs,
  progressByJobId,
  summaries,
  summaryPhaseByVideoId,
  onOpenChannel,
}: {
  /** Newest activity events pushed over SSE, appended by App. */
  live: ActivityEvent[];
  jobs: Job[];
  progressByJobId?: Record<
    number,
    { percent: number; speed: string; eta: string }
  >;
  summaries: SummaryJob[];
  summaryPhaseByVideoId?: Record<string, string>;
  /** Opens a channel's page. Optional so a test can render without navigation. */
  onOpenChannel?: (id: string) => void;
}) {
  const [past, setPast] = useState<ActivityEvent[]>([]);
  const [upcoming, setUpcoming] = useState<UpcomingItem[]>([]);
  const [truncated, setTruncated] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState("all");
  // now is captured once per render pass for the relative-time labels and the
  // marker; it does not tick, which is fine for a log.
  const now = Date.now();

  // Load the past log once on mount. The future projection is owned entirely by
  // the effect below (keyed on the live state), so it is NOT fetched here — that
  // avoided a redundant second /api/activity/upcoming on every open.
  useEffect(() => {
    let active = true;
    listActivity()
      .then((page) => {
        if (!active) return;
        // MERGE, not replace: a live "activity" event can arrive between the
        // server building this snapshot and the fetch resolving. The live effect
        // prepends it to `past`; replacing here would clobber it (and its ref
        // wouldn't change, so the live effect won't re-run). Keep any such
        // live-only rows on top, deduped by id.
        setPast((prev) => {
          const ids = new Set(page.events.map((e) => e.id));
          const liveOnly = prev.filter((e) => !ids.has(e.id));
          return [...liveOnly, ...page.events];
        });
        setHasMore(page.has_more);
        setLoaded(true);
      })
      .catch((e: Error) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
    };
  }, []);

  // Live-append: prepend any SSE event newer than what we already show. Keyed on
  // id so a reconnect that replays an event can't double it.
  useEffect(() => {
    if (live.length === 0) return;
    setPast((prev) => {
      const seen = new Set(prev.map((e) => e.id));
      const fresh = live.filter((e) => !seen.has(e.id));
      if (fresh.length === 0) return prev;
      // live is oldest→newest; newest goes on top.
      return [...fresh.reverse(), ...prev];
    });
  }, [live]);

  // Keep the future projection in step with the live now-marker. The upcoming
  // list is a server snapshot filtered to pending-only, but App's jobs/summaries
  // keep moving under it: a pending download the projection shows above the line
  // becomes "running" (rendered at the marker) and then terminal (logged below),
  // so without a refresh the same item renders twice — and a scan whose time has
  // passed lingers above the line with a past label. Refetch whenever the live
  // state that could invalidate the snapshot changes (a new event, or the
  // jobs/summaries sets shifting), so the halves never disagree. Runs its first
  // fetch when `loaded` flips true (the initial projection load) and is the only
  // place upcoming is fetched, so there is no duplicate request.
  useEffect(() => {
    if (!loaded) return;
    let active = true;
    listUpcoming()
      .then((up) => {
        if (!active) return;
        setUpcoming(up.items);
        setTruncated(up.truncated);
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, [loaded, jobs, summaries, live]);

  const matches = useMemo(
    () => (kind: string, outcome?: string) => {
      if (filter === "all") return true;
      if (filter === "problems")
        return outcome === "fail" || outcome === "warn";
      return kind === filter;
    },
    [filter],
  );

  // History (top): newest first, capped to the newest HISTORY_LIMIT.
  // pastFiltered keeps the full filtered length so we can hint how many earlier
  // rows the cap hides.
  const pastFiltered = useMemo(
    () => past.filter((e) => matches(e.kind, e.outcome)),
    [past, matches],
  );
  const shownPast = useMemo(
    () => pastFiltered.slice(0, HISTORY_LIMIT),
    [pastFiltered],
  );
  // Planned (bottom, below the now marker): soonest first — the projection is
  // already ascending — capped to the nearest PLANNED_LIMIT. "Problems only"
  // hides the whole future half (nothing scheduled has failed yet).
  const upcomingFiltered = useMemo(() => {
    if (filter === "problems") return [];
    return upcoming.filter((i) => matches(i.kind));
  }, [upcoming, matches, filter]);
  const shownUpcoming = useMemo(
    () => upcomingFiltered.slice(0, PLANNED_LIMIT),
    [upcomingFiltered],
  );
  // Overflow hints (the timeline is a fixed 10+10 window). Earlier history beyond
  // the cap, and further-out scheduled work (our slice overflow plus the
  // server's own projection cap), each get a "+N" edge instead of a pager.
  const moreHistory = pastFiltered.length - shownPast.length;
  const morePlanned =
    upcomingFiltered.length - shownUpcoming.length + truncated;

  // Running items in "Up next", from App's live state (never the projection).
  const running = jobs.filter((j) => j.state === "running");
  const runningSummaries = summaries.filter((s) => s.state === "running");
  const runningCount = running.length + runningSummaries.length;
  const nothingAtNow = runningCount === 0;
  // "Problems only" is a view of the log's failures/warnings — the whole live
  // section ("Up next": queued work and running in-progress work) is hidden,
  // since healthy in-progress work is not a problem.
  const showLive = filter !== "problems";
  // "Up next" renders only when something is running or queued.
  const hasUpNext = runningCount > 0 || shownUpcoming.length > 0;
  // True queued total for the header — the full filtered projection plus the
  // server's own cap, not the capped-to-10 slice (which would undercount and
  // fail to reconcile with the "+N more scheduled" edge below).
  const queuedCount = upcomingFiltered.length + truncated;
  // Header count, e.g. "2 running · 4 queued"; each side omitted when zero.
  const upNextCount = [
    runningCount > 0 ? `${runningCount} running` : "",
    queuedCount > 0 ? `${queuedCount} queued` : "",
  ]
    .filter(Boolean)
    .join(DOT);

  if (error) {
    return <div className="errline">{error}</div>;
  }
  if (!loaded) {
    return <p className="agenda-empty">Loading…</p>;
  }

  const empty =
    shownPast.length === 0 &&
    (!showLive || (shownUpcoming.length === 0 && nothingAtNow));

  return (
    <>
      <div className="chips">
        {FILTERS.map((f) => (
          <button
            key={f.id}
            type="button"
            className={`chip${filter === f.id ? " on" : ""}`}
            onClick={() => setFilter(f.id)}
          >
            {f.label}
          </button>
        ))}
      </div>

      {empty ? (
        <p className="agenda-empty">
          Nothing yet — this fills in as peeq scans channels, downloads videos,
          and tidies up.
        </p>
      ) : (
        <>
          {/* RECENT ACTIVITY — the durable log, newest first. A plain feed:
              scrolling down goes strictly further back in time. It leads the
              page: what already happened is the answer to "what has peeq been
              doing", and the projection below is the secondary question. */}
          {shownPast.length > 0 ? (
            <section className="ag-section">
              <div className="ag-sec-head">
                <h2 className="ag-sec-title">Recent activity</h2>
                <span className="ag-sec-note">newest first</span>
              </div>

              <div className="agenda">
                {shownPast.map((e) => {
                  const k = kindOf(e.kind);
                  const detail = eventDetail(e);
                  return (
                    <div key={e.id} className={`ag-row ${e.outcome}`}>
                      <span className="ag-node">
                        <Icon name={k.icon} size="16px" />
                      </span>
                      <div className="ag-body">
                        <div className="ag-subject">
                          {subjectNode(
                            e.kind,
                            e.subject_id,
                            e.subject || k.label,
                            onOpenChannel,
                          )}
                        </div>
                        {detail ? (
                          <div className="ag-detail">{detail}</div>
                        ) : null}
                      </div>
                      <span className="ag-when" title={e.at}>
                        {relTime(parseUTC(e.at), now)}
                      </span>
                    </div>
                  );
                })}
              </div>

              {/* Older rows hidden by the cap sit at the BOTTOM — in a
                  newest-first feed, the hidden ones are the older ones. */}
              {moreHistory > 0 || hasMore ? (
                <div className="ag-edge">
                  {moreHistory > 0
                    ? `+${moreHistory} earlier`
                    : "earlier activity"}
                </div>
              ) : null}
            </section>
          ) : null}

          {/* UP NEXT — running work first, then queued/scheduled soonest-first.
              A projection, not a log, so it lives in its own section and never
              claims "ago". Hidden entirely under "Problems only" (healthy
              in-progress work is not a problem) and when nothing is live.
              Sits below the log; `.ag-section + .ag-section` spaces whichever
              of the two renders second, so either one alone is still flush. */}
          {showLive && hasUpNext ? (
            <section className="ag-section">
              <div className="ag-sec-head">
                {/* The pulse marks live in-flight work; when only queued items
                    are pending (nothing running) it would falsely read as
                    active, so it shows only when something is actually running. */}
                {runningCount > 0 ? <span className="ag-live" /> : null}
                <h2 className="ag-sec-title">Up next</h2>
                {upNextCount ? (
                  <span className="ag-sec-count">{upNextCount}</span>
                ) : null}
              </div>

              <div className="agenda up">
                {/* RUNNING — from App's live state, never the projection */}
                {running.map((j) => {
                  const p = progressByJobId?.[j.job_id];
                  return (
                    <div key={`run-${j.job_id}`} className="ag-row running">
                      <span className="ag-node">
                        <Icon name="download" size="16px" />
                      </span>
                      <div className="ag-body">
                        <div className="ag-subject">
                          {j.title || j.video_id}
                        </div>
                        {p ? (
                          <div className="ag-progress">
                            <i style={{ width: `${p.percent}%` }} />
                          </div>
                        ) : null}
                        <div className="ag-detail">
                          Downloading
                          {p ? `${DOT}${Math.round(p.percent)}%` : ""}
                          {p?.speed ? `${DOT}${p.speed}` : ""}
                          {p?.eta ? `${DOT}${p.eta} left` : ""}
                        </div>
                      </div>
                      <span className="ag-when">now</span>
                    </div>
                  );
                })}
                {runningSummaries.map((s) => (
                  <div key={`runs-${s.id}`} className="ag-row running">
                    <span className="ag-node">
                      <Icon name="alignLeft" size="16px" />
                    </span>
                    <div className="ag-body">
                      <div className="ag-subject">{s.title || s.video_id}</div>
                      <div className="ag-detail">
                        {summaryPhaseLabel(summaryPhaseByVideoId?.[s.video_id])}
                      </div>
                    </div>
                    <span className="ag-when">now</span>
                  </div>
                ))}

                {/* QUEUED — dimmed, faint rail, soonest first */}
                {shownUpcoming.map((item, i) => {
                  const k = kindOf(item.kind);
                  return (
                    <div key={`up-${i}`} className="ag-row planned">
                      <span className="ag-node">
                        <Icon name={k.icon} size="16px" />
                      </span>
                      <div className="ag-body">
                        <div className="ag-subject">
                          {subjectNode(
                            item.kind,
                            item.subject_id,
                            item.subject || k.label,
                            onOpenChannel,
                          )}
                        </div>
                        {item.summary ? (
                          <div className="ag-detail">
                            {leadCap(item.summary)}
                          </div>
                        ) : null}
                      </div>
                      <span className="ag-when">
                        {plannedWhen(item.at, now)}
                      </span>
                    </div>
                  );
                })}
              </div>

              {/* Further scheduled work beyond the cap (our slice overflow plus
                  the server's projection cap). Outside .agenda so it has no rail. */}
              {morePlanned > 0 ? (
                <div className="ag-edge">+{morePlanned} more scheduled</div>
              ) : null}
            </section>
          ) : null}
        </>
      )}
    </>
  );
}
