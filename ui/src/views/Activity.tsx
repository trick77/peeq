import { useCallback, useEffect, useMemo, useState } from "react";
import {
  listActivity,
  listUpcoming,
  type ActivityEvent,
  type Job,
  type SummaryJob,
  type UpcomingItem,
} from "../api";
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

// relTime renders a compact "3m ago" / "in 2h" against now. Coarse on purpose —
// the agenda is about sequence, not exact clock times.
function relTime(date: Date, now: number): string {
  const secs = Math.round((date.getTime() - now) / 1000);
  const past = secs < 0;
  const a = Math.abs(secs);
  let out: string;
  if (a < 60) out = "just now";
  else if (a < 3600) out = `${Math.round(a / 60)}m`;
  else if (a < 86400) out = `${Math.round(a / 3600)}h`;
  else out = `${Math.round(a / 86400)}d`;
  if (out === "just now") return out;
  return past ? `${out} ago` : `in ${out}`;
}

const KIND_LABEL: Record<string, string> = {
  scan: "Scan",
  channel_meta: "Metadata",
  download: "Download",
  summary: "Summary",
  retention: "Cleanup",
  ytdlp: "yt-dlp",
  access: "Access",
};

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

  useEffect(() => {
    let active = true;
    Promise.all([listActivity(), listUpcoming()])
      .then(([page, up]) => {
        if (!active) return;
        setPast(page.events);
        setHasMore(page.has_more);
        setUpcoming(up.items);
        setTruncated(up.truncated);
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

  if (error) {
    return <div className="errline">{error}</div>;
  }
  if (!loaded) {
    return <p className="agenda-empty">Loading…</p>;
  }

  const empty =
    shownPast.length === 0 && shownUpcoming.length === 0 && nothingAtNow;

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
        <div className="agenda">
          {/* FUTURE (above now) */}
          {truncated > 0 && filter !== "problems" ? (
            <div className="edge">+{truncated} more scheduled</div>
          ) : null}
          {shownUpcoming.map((item, i) => (
            <div key={`up-${i}`} className="ag planned">
              <i className="dot planned" />
              <div className="ag-body">
                <div className="ag-line">
                  <span className="ag-kind">
                    {KIND_LABEL[item.kind] ?? item.kind}
                  </span>
                  {item.subject ? (
                    <span className="ag-subject">{item.subject}</span>
                  ) : null}
                </div>
                {item.summary ? (
                  <div className="ag-sub">planned · {item.summary}</div>
                ) : null}
              </div>
              <span className="ag-when">
                {item.at ? relTime(parseUTC(item.at), now) : "next"}
              </span>
            </div>
          ))}

          {/* NOW */}
          <div className="nowline">
            <span className="mark">now</span>
          </div>
          {running.map((j) => {
            const p = progressByJobId?.[j.job_id];
            return (
              <div key={`run-${j.job_id}`} className="ag running">
                <i className="dot running" />
                <div className="ag-body">
                  <div className="ag-line">
                    <span className="ag-kind">Download</span>
                    <span className="ag-subject">{j.title || j.video_id}</span>
                  </div>
                  <div className="ag-sub">
                    downloading{p ? ` · ${Math.round(p.percent)}%` : ""}
                    {p?.eta ? ` · ${p.eta}` : ""}
                  </div>
                </div>
                <span className="ag-when">now</span>
              </div>
            );
          })}
          {runningSummaries.map((s) => (
            <div key={`runs-${s.id}`} className="ag running">
              <i className="dot running" />
              <div className="ag-body">
                <div className="ag-line">
                  <span className="ag-kind">Summary</span>
                  <span className="ag-subject">{s.title || s.video_id}</span>
                </div>
                <div className="ag-sub">
                  {summaryPhaseByVideoId?.[s.video_id] === "embedding"
                    ? "embedding"
                    : "summarizing"}
                </div>
              </div>
              <span className="ag-when">now</span>
            </div>
          ))}

          {/* PAST (below now) */}
          {shownPast.map((e) => (
            <div key={e.id} className="ag">
              <i className={`dot ${e.outcome}`} />
              <div className="ag-body">
                <div className="ag-line">
                  <span className="ag-kind">
                    {KIND_LABEL[e.kind] ?? e.kind}
                  </span>
                  {e.subject ? (
                    <span className="ag-subject">{e.subject}</span>
                  ) : null}
                </div>
                <div className="ag-sub">
                  {e.summary}
                  {e.detail ? ` · ${e.detail}` : ""}
                </div>
              </div>
              <span className="ag-when" title={e.at}>
                {relTime(parseUTC(e.at), now)}
              </span>
            </div>
          ))}

          {hasMore ? (
            <div className="edge">
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
            <div className="edge">— oldest kept —</div>
          ) : null}
        </div>
      )}
    </>
  );
}
