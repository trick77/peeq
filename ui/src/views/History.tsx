import { useEffect, useMemo, useState } from "react";
import { listActivity, type ActivityEvent } from "../api";
import { DOT } from "../sep";
import {
  AgendaNode,
  kindOf,
  leadCap,
  parseUTC,
  relTime,
  subjectNode,
} from "./agenda";

// History — the durable log of what peeq's workers actually did, newest first.
// A pure record: nothing here is actionable, so the page carries no buttons.
// Scrolling down goes strictly further back in time.
//
// It is the past half of the old Activity page. The future half moved to Up
// next, where it sits beside the running work it was always describing —
// Activity's one filter row only ever half-applied to its projection anyway
// ("Problems only" had to blank the whole section, because nothing scheduled
// has failed yet).
//
// The log is fetched here, but the LIVE events are not: App owns the single
// session SSE subscription and passes the newest ones down. The hub is
// memoryless — a client only sees events published after it subscribed — so a
// subscription owned by this view would lose every event that arrived while the
// user was on another page.

// eventDetail joins an event's summary and detail into one lead-capitalized
// line; the kind is shown by the icon, not repeated in words.
function eventDetail(e: ActivityEvent): string {
  const text = [e.summary, e.detail].filter(Boolean).join(DOT);
  return text ? leadCap(text) : "";
}

// The log renders a fixed window of the newest rows; beyond it a "+N earlier"
// edge hints at what is hidden rather than paging.
const HISTORY_LIMIT = 10;

const FILTERS: { id: string; label: string }[] = [
  { id: "all", label: "All" },
  { id: "scan", label: "Scans" },
  { id: "download", label: "Downloads" },
  { id: "summary", label: "Summaries" },
  { id: "retention", label: "Cleanup" },
  { id: "problems", label: "Problems only" },
];

export function History({
  live,
  onOpenChannel,
}: {
  /** Newest activity events pushed over SSE, appended by App. */
  live: ActivityEvent[];
  /** Opens a channel's page. Optional so a test can render without navigation. */
  onOpenChannel?: (id: string) => void;
}) {
  const [past, setPast] = useState<ActivityEvent[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [retainedMax, setRetainedMax] = useState(0);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState("all");
  // now is captured once per render pass for the relative-time labels; it does
  // not tick, which is fine for a log.
  const now = Date.now();

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
        setRetainedMax(page.retained_max);
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

  const matches = useMemo(
    () => (kind: string, outcome?: string) => {
      if (filter === "all") return true;
      if (filter === "problems")
        return outcome === "fail" || outcome === "warn";
      return kind === filter;
    },
    [filter],
  );

  const filtered = useMemo(
    () => past.filter((e) => matches(e.kind, e.outcome)),
    [past, matches],
  );
  const shown = useMemo(() => filtered.slice(0, HISTORY_LIMIT), [filtered]);
  const moreHistory = filtered.length - shown.length;

  if (error) {
    return <div className="errline">{error}</div>;
  }
  if (!loaded) {
    return <p className="agenda-empty">Loading…</p>;
  }

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
        {/* The retention ceiling ends the chip row rather than sitting under
            the log: it explains why the page stops where it does, which is
            worth knowing before you scroll to the bottom and wonder. */}
        {retainedMax > 0 ? (
          <span className="chips-note">
            keeps the last <span className="num">{retainedMax}</span> entries
          </span>
        ) : null}
      </div>

      {shown.length === 0 ? (
        <p className="agenda-empty">
          {filter === "all"
            ? "Nothing yet — this fills in as peeq scans channels, downloads videos, and tidies up."
            : "Nothing matching that filter yet."}
        </p>
      ) : (
        <>
          <div className="agenda">
            {shown.map((e) => {
              const k = kindOf(e.kind);
              const detail = eventDetail(e);
              return (
                <div key={e.id} className={`ag-row ${e.outcome}`}>
                  <AgendaNode kind={e.kind} />
                  <div className="ag-body">
                    <div className="ag-subject">
                      {subjectNode(
                        e.kind,
                        e.subject_id,
                        e.subject || k.label,
                        onOpenChannel,
                      )}
                    </div>
                    {detail ? <div className="ag-detail">{detail}</div> : null}
                  </div>
                  <span className="ag-when" title={e.at}>
                    {relTime(parseUTC(e.at), now)}
                  </span>
                </div>
              );
            })}
          </div>

          {/* Older rows hidden by the cap sit at the BOTTOM — in a newest-first
              feed, the hidden ones are the older ones. */}
          {moreHistory > 0 || hasMore ? (
            <div className="ag-edge">
              {moreHistory > 0 ? `+${moreHistory} earlier` : "earlier activity"}
            </div>
          ) : null}
        </>
      )}
    </>
  );
}
