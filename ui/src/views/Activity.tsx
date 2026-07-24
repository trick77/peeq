import { useCallback, useEffect, useMemo, useState } from "react";
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
import { Button } from "../ui";

// Activity — one agenda through a *now* marker. Above the marker: what is
// scheduled or queued (the live projection from /api/activity/upcoming, plus
// the running items App already holds). Below: what actually happened (the
// durable log from /api/activity). It is a pure log — nothing here is
// actionable — so it deliberately carries no buttons except "load older".
//
// Three disjoint states never render twice: PENDING work comes from the
// upcoming projection (above now), RUNNING work from App's live jobs/summaries
// (at now), TERMINAL work from the event log (below now).

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

// eventDetail joins an event's summary and detail into one lead-capitalized
// line; the kind is shown by the icon, not repeated in words.
function eventDetail(e: ActivityEvent): string {
  const text = [e.summary, e.detail].filter(Boolean).join(" · ");
  return text ? leadCap(text) : "";
}

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
}) {
  const [past, setPast] = useState<ActivityEvent[]>([]);
  const [upcoming, setUpcoming] = useState<UpcomingItem[]>([]);
  const [truncated, setTruncated] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
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

  const loadOlder = useCallback(() => {
    if (past.length === 0) return;
    setLoadingMore(true);
    listActivity(past[past.length - 1].id)
      .then((page) => {
        setPast((prev) => [...prev, ...page.events]);
        setHasMore(page.has_more);
      })
      .catch((e: Error) => setError(e.message))
      .finally(() => setLoadingMore(false));
  }, [past]);

  const matches = useCallback(
    (kind: string, outcome?: string) => {
      if (filter === "all") return true;
      if (filter === "problems")
        return outcome === "fail" || outcome === "warn";
      return kind === filter;
    },
    [filter],
  );

  const shownPast = useMemo(
    () => past.filter((e) => matches(e.kind, e.outcome)),
    [past, matches],
  );
  // Future block: soonest nearest the now line (bottom), so reverse the
  // ascending projection. "Problems only" hides the whole future half (nothing
  // scheduled has failed yet).
  const shownUpcoming = useMemo(() => {
    if (filter === "problems") return [];
    return [...upcoming].filter((i) => matches(i.kind)).reverse();
  }, [upcoming, matches, filter]);

  // Running items at the now marker, from App's live state (never the projection).
  const running = jobs.filter((j) => j.state === "running");
  const runningSummaries = summaries.filter((s) => s.state === "running");
  const nothingAtNow = running.length === 0 && runningSummaries.length === 0;
  // "Problems only" is a view of the log's failures/warnings — the whole live
  // half (future, the now marker, and running in-progress work) is hidden, since
  // healthy in-progress work is not a problem.
  const showLive = filter !== "problems";

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
          {/* Top edge lives outside .agenda so it doesn't sit on the rail. */}
          {truncated > 0 && filter !== "problems" ? (
            <div className="ag-edge top">+{truncated} more scheduled</div>
          ) : null}

          <div className="agenda">
            {showLive ? (
              <>
                {/* FUTURE — dimmed, faint rail, soonest nearest the now line */}
                {shownUpcoming.map((item, i) => {
                  const k = kindOf(item.kind);
                  return (
                    <div key={`up-${i}`} className="ag-row planned">
                      <span className="ag-node">
                        <Icon name={k.icon} size="16px" />
                      </span>
                      <div className="ag-body">
                        <div className="ag-subject">
                          {item.subject || k.label}
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

                {/* NOW — a row like any other, so the rail runs straight through */}
                <div className="ag-row now">
                  <span className="ag-node">
                    <span className="ag-pulse" />
                  </span>
                  <div className="ag-nowbar">
                    <span className="ag-nowlbl">now</span>
                    <span className="ag-rule" />
                  </div>
                </div>

                {/* RUNNING — on the now node, from App's live state */}
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
                          Downloading{p ? ` · ${Math.round(p.percent)}%` : ""}
                          {p?.speed ? ` · ${p.speed}` : ""}
                          {p?.eta ? ` · ${p.eta} left` : ""}
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
              </>
            ) : null}

            {/* PAST — solid rail, colour by outcome */}
            {shownPast.map((e) => {
              const k = kindOf(e.kind);
              const detail = eventDetail(e);
              return (
                <div key={e.id} className={`ag-row ${e.outcome}`}>
                  <span className="ag-node">
                    <Icon name={k.icon} size="16px" />
                  </span>
                  <div className="ag-body">
                    <div className="ag-subject">{e.subject || k.label}</div>
                    {detail ? <div className="ag-detail">{detail}</div> : null}
                  </div>
                  <span className="ag-when" title={e.at}>
                    {relTime(parseUTC(e.at), now)}
                  </span>
                </div>
              );
            })}
          </div>

          {/* Bottom edge — also outside .agenda. */}
          {hasMore ? (
            <div className="ag-edge">
              <Button
                type="button"
                variant="secondary"
                small
                busy={loadingMore}
                onClick={loadOlder}
              >
                Load older
              </Button>
            </div>
          ) : shownPast.length > 0 ? (
            <div className="ag-edge">— oldest kept —</div>
          ) : null}
        </>
      )}
    </>
  );
}
