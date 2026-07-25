import { useEffect, useMemo, useState } from "react";
import { listActivity, type ActivityEvent } from "../api";
import { Icon } from "../icons";
import { DOT } from "../sep";
import { kindOf, leadCap, parseUTC, relTime, subjectNode } from "./agenda";

// History — the durable log of what peeq's workers actually did, newest first.
// A pure record: nothing here is actionable, so the page carries no buttons.
// Scrolling down goes strictly further back in time.
//
// It is the past half of the old Activity page. The future half moved to Up
// next, where it sits beside the running work it was always describing --
// Activity's one filter row only ever half-applied to its projection anyway
// ("Problems only" had to blank the whole section, because nothing scheduled
// has failed yet).
//
// A TIMELINE, not a log table. Four things carry the structure:
//   - a clock gutter, so rows are placed in the day rather than measured from
//     now. Mono, because these numerals are a column that must align.
//   - day separators, which replace the running relative-time column the old
//     agenda used. "13:48" under "Today" beats "28 min ago" for anything you
//     are reading to reconstruct a sequence.
//   - the outcome on the node ring, not only in the text colour. A failure
//     should be findable by scanning the rail, not by reading every line.
//   - the kind leading the detail line as a past-tense word.
// The relative time stays on the right, where it answers "how long ago" for the
// newest rows without competing with the clock.
//
// The log is fetched here, but the LIVE events are not: App owns the single
// session SSE subscription and passes the newest ones down. The hub is
// memoryless -- a client only sees events published after it subscribed -- so a
// subscription owned by this view would lose every event that arrived while the
// user was on another page.

const PAGE_SIZE = 20;

const FILTERS: { id: string; label: string }[] = [
  { id: "all", label: "All" },
  { id: "scan", label: "Scans" },
  { id: "download", label: "Downloads" },
  { id: "summary", label: "Summaries" },
  { id: "retention", label: "Cleanup" },
  { id: "problems", label: "Problems only" },
];

// PAST names what a kind DID, for the word that leads the detail line. Only
// reached when the event's own summary can't lead (see detailParts) -- the
// workers already write "downloaded", "summary failed", "metadata refresh
// failed", and a second hardcoded vocabulary here would be free to contradict
// them the moment one of those strings changes.
const PAST: Record<string, string> = {
  scan: "Scanned",
  channel_meta: "Metadata refreshed",
  download: "Downloaded",
  summary: "Summarized",
  retention: "Cleanup",
  ytdlp: "Maintenance",
  access: "Access",
};

// detailParts splits an event into the leading kind word and the rest.
//
// The workers write summaries that already read as the past-tense verb the
// timeline wants -- "downloaded", "download failed", "summarized", "metadata
// refresh failed" -- so the summary leads whenever it can. It can't when it
// opens with a number ("3 new", "12 older videos skipped"): a count is not a
// verb, so the kind supplies one and the count moves into the rest, giving
// "Scanned - 3 new - streams tab missing".
function detailParts(e: ActivityEvent): { lead: string; rest: string } {
  const summary = e.summary?.trim() ?? "";
  const startsWithWord = /^[a-zA-Z]/.test(summary);
  if (summary && startsWithWord) {
    return { lead: leadCap(summary), rest: e.detail ?? "" };
  }
  const lead = PAST[e.kind] ?? kindOf(e.kind).label;
  return { lead, rest: [summary, e.detail].filter(Boolean).join(DOT) };
}

// clockOf renders the event's wall-clock time in the viewer's zone. The gutter
// is about placing a row within its day, so it deliberately drops the date --
// the day separator above already carries it.
function clockOf(at: string): string {
  const d = parseUTC(at);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

// dayKeyOfDate is the local calendar day a Date falls on, as a sortable key.
// Read in local time, not UTC, so a 01:00 event doesn't get filed under the
// previous day for anyone east of Greenwich.
function dayKeyOfDate(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

// dayKeyOf is the same for an event's backend timestamp.
function dayKeyOf(at: string): string {
  return dayKeyOfDate(parseUTC(at));
}

const WEEKDAY = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
const MONTH = [
  "Jan",
  "Feb",
  "Mar",
  "Apr",
  "May",
  "Jun",
  "Jul",
  "Aug",
  "Sep",
  "Oct",
  "Nov",
  "Dec",
];

// dayLabel names a separator. The two days anyone reads by name get it; older
// ones get a date, and yesterday gets both so the transition is never a guess.
function dayLabel(key: string, now: number): string {
  // Keyed straight off the Date. Round-tripping through toISOString() looked
  // harmless but produced a string already ending in "Z", which parseUTC then
  // appended a second "Z" to — an Invalid Date, so both keys read
  // "NaN-NaN-NaN", never matched, and the two labels anyone actually reads
  // never appeared.
  const today = dayKeyOfDate(new Date(now));
  const yesterday = dayKeyOfDate(new Date(now - 86400_000));
  const [y, m, d] = key.split("-").map(Number);
  const date = new Date(y, m - 1, d);
  const stamp = `${WEEKDAY[date.getDay()]} ${d} ${MONTH[m - 1]}`;
  if (key === today) return "Today";
  if (key === yesterday) return `Yesterday${DOT}${stamp}`;
  return stamp;
}

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
  const [loadingMore, setLoadingMore] = useState(false);
  // Two error slots on purpose. `error` means the page never loaded and there
  // is nothing to show; `moreError` means paging backwards failed while a good
  // log is already on screen, which must not blank it.
  const [error, setError] = useState<string | null>(null);
  const [moreError, setMoreError] = useState<string | null>(null);
  const [filter, setFilter] = useState("all");
  // now is captured once per render pass for the relative-time labels and the
  // day names; it does not tick, which is fine for a log.
  const now = Date.now();

  useEffect(() => {
    let active = true;
    listActivity(undefined, PAGE_SIZE)
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

  // Page back from the oldest row on screen. Keyset, not offset, so a live
  // event arriving mid-read can't shift the window and duplicate a row.
  function loadMore() {
    const oldest = past[past.length - 1];
    if (!oldest || loadingMore) return;
    setLoadingMore(true);
    listActivity(oldest.id, PAGE_SIZE)
      .then((page) => {
        setPast((prev) => {
          const seen = new Set(prev.map((e) => e.id));
          return [...prev, ...page.events.filter((e) => !seen.has(e.id))];
        });
        setHasMore(page.has_more);
      })
      // Deliberately NOT setError: that renders instead of the whole page, so a
      // transient failure paging backwards would throw away the log already on
      // screen. The rows you have are still good; only the fetch for older ones
      // failed, and the control stays where it is to be tried again.
      .catch((e: Error) => setMoreError(e.message))
      .finally(() => setLoadingMore(false));
  }

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

  // Group into days, preserving the newest-first order the server sent.
  const days = useMemo(() => {
    const out: { key: string; events: ActivityEvent[] }[] = [];
    for (const e of filtered) {
      const key = dayKeyOf(e.at);
      const last = out[out.length - 1];
      if (last && last.key === key) last.events.push(e);
      else out.push({ key, events: [e] });
    }
    return out;
  }, [filtered]);

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

      {days.length === 0 ? (
        <p className="agenda-empty">
          {filter === "all"
            ? "Nothing yet — this fills in as peeq scans channels, downloads videos, and tidies up."
            : "Nothing matching that filter yet."}
        </p>
      ) : (
        <>
          <div className="agenda">
            {days.map((day) => (
              <div key={day.key} className="ag-day">
                <div className="ag-daysep">
                  <span>{dayLabel(day.key, now)}</span>
                  <i />
                </div>
                {day.events.map((e) => {
                  const k = kindOf(e.kind);
                  const { lead, rest } = detailParts(e);
                  return (
                    <div key={e.id} className={`ag-row ${e.outcome}`}>
                      <span className="ag-clock" title={e.at}>
                        {clockOf(e.at)}
                      </span>
                      <span className="ag-node">
                        <Icon name={k.icon} size="12px" />
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
                        <div className="ag-detail">
                          <span className="ag-kind">{lead}</span>
                          {rest ? (
                            <>
                              {DOT}
                              {rest}
                            </>
                          ) : null}
                        </div>
                      </div>
                      <span className="ag-when">
                        {relTime(parseUTC(e.at), now)}
                      </span>
                    </div>
                  );
                })}
              </div>
            ))}
          </div>

          {/* Older rows sit behind an explicit fetch rather than an infinite
              scroll: the log is something you consult, and a page that grows
              while you read it is hard to keep your place in. */}
          {hasMore ? (
            <div className="ag-edge">
              <button
                type="button"
                className="chip"
                onClick={loadMore}
                disabled={loadingMore}
              >
                {loadingMore ? "Loading…" : `Load ${PAGE_SIZE} more`}
              </button>
              {moreError ? (
                <span className="ag-edge-err">{moreError}</span>
              ) : null}
            </div>
          ) : null}
        </>
      )}
    </>
  );
}
