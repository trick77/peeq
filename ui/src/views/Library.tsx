import { useEffect, useRef, useState } from "react";
import { VideoCard } from "../components/VideoCard";
import { PillStrip } from "../components/PillStrip";
import { SearchField } from "../components/SearchField";
import {
  listVideos,
  getVideoCounts,
  setFavorite,
  setWatched,
  redownload,
} from "../api";
import { useSettings } from "../settingsStore";
import type { Video, VideoCounts, VideoFilter, VideoSort } from "../api/types";
import { CATEGORIES } from "../categories";
import { controlClass } from "../ui";

// "Watched" is a filter chip like any other: selecting it lists watched videos
// in the main grid. (It used to be split into an "Already watched" drawer below
// the grid; that turned out to be a second, more awkward route to a list a chip
// gives directly.)
const CHIPS: { id: VideoFilter; label: string }[] = [
  { id: "unwatched", label: "Unwatched" },
  { id: "all", label: "All" },
  { id: "in_progress", label: "In progress" },
  { id: "favorites", label: "Favorites" },
  { id: "watched", label: "Watched" },
];

// SORT_OPTIONS is shared with the channel page's Archive tab so the two
// lists can never drift apart in wording or in accepted values.
//
// The import-date pair leads and is the default: the question the grid is
// opened with is "what arrived last?", and ranking by airdate answered a
// different one — import a batch of older videos and nothing new appears at the
// top. Airdate is still one click away, unchanged.
//
// EVERY option names its date. Two different dates are on offer here, so an
// unqualified "Newest first" cannot be read: it looks like it must mean the
// obvious one, and which one is obvious depends on the reader.
//
// The ids stay `added_*` while the labels say "imported" — the ids are the
// ?sort= wire contract the backend maps in sortClauses (videos/store.go) and
// are asserted in tests on both sides. Only the wording moved.
export const SORT_OPTIONS: { id: VideoSort; label: string }[] = [
  { id: "added_newest", label: "Recently imported" },
  { id: "added_oldest", label: "Imported, oldest first" },
  { id: "newest", label: "Newest airdate" },
  { id: "oldest", label: "Oldest airdate" },
  { id: "longest", label: "Longest first" },
  { id: "title", label: "Title A–Z" },
];

// INBOX_SORT_OPTIONS drops the import-date pair for the Inbox and the channel
// page's New tab. Items there have never been downloaded, so they have no
// import date at all — offering it would be an option that cannot work, which
// is also why those two lists still open on airdate rather than the default
// used here. It
// lives beside SORT_OPTIONS rather than in Inbox.tsx so the two lists stay
// adjacent and cannot drift.
export const INBOX_SORT_OPTIONS = SORT_OPTIONS.filter(
  (o) => o.id !== "added_newest" && o.id !== "added_oldest",
);

// matchesFilter mirrors videos.Store.List's SQL WHERE clauses (see
// backend/internal/videos/store.go). It is only consulted for the optimistic
// hide below (`visible`): a card the user just toggled has to leave the grid
// before the server is asked again. The chip numbers themselves are the
// server's (getVideoCounts), so this is no longer counting anything.
//
// One branch is deliberately WIDER than its SQL counterpart rather than an
// exact mirror: "unwatched" also accepts queued/downloading, where the Go
// clause is status = 'downloaded' only. It agrees anyway, because the server
// applies notInFlight to every list, so a queued or downloading row never
// reaches this function to be judged. Narrowing it would be equivalent, not a
// fix — left alone because the extra states document what the filter means
// ("play-eligible") independently of what the server happens to send.
function matchesFilter(v: Video, filter: VideoFilter): boolean {
  switch (filter) {
    case "unwatched":
      // "Unwatched" means never opened: play-eligible, not watched, and the
      // resume position still at zero. A partially-watched row is "in_progress".
      return (
        (v.status === "downloaded" ||
          v.status === "queued" ||
          v.status === "downloading") &&
        !v.watched &&
        v.resume_position_seconds === 0
      );
    case "in_progress":
      return (
        v.status === "downloaded" && !v.watched && v.resume_position_seconds > 0
      );
    case "watched":
      // Not tombstoned: a swept video was reclaimed BECAUSE it was watched, and
      // the server leaves those out of this filter. Counting them here would
      // put a number on the chip that the grid it opens does not show.
      return v.watched && v.status !== "tombstoned";
    case "favorites":
      return v.favorite;
    default:
      return true;
  }
}

// emptyMessage tells the truth about *why* the grid is empty. Since the default
// filter is now "unwatched", a "No videos yet." on an empty grid would falsely
// read as "your library is empty" when it really means "nothing unwatched" — so
// each status filter names what it found nothing of. Only "all" being empty
// means the library itself is empty.
function emptyMessage(filter: VideoFilter): string {
  switch (filter) {
    case "unwatched":
      return "Nothing unwatched.";
    case "in_progress":
      return "Nothing in progress.";
    case "watched":
      return "Nothing watched yet.";
    case "favorites":
      return "No favorites yet.";
    default:
      return "No videos yet.";
  }
}

// Library — the default view: a search + sort bar, filter chips, and a grid of
// VideoCards. The search query itself still lives in App and arrives as the
// `search`/`onSearchChange` pair: the state is lifted so that a query survives
// navigating away to a channel and back, which is exactly what it did while it
// belonged to the (now deleted) top bar.
export function Library({
  onOpenVideo,
  onOpenChannel,
  search,
  onSearchChange,
  queueSignal = "",
  onQueued,
}: {
  onOpenVideo: (id: string) => void;
  // onOpenChannel — optional: wired by App (Task 11), rendered as channel
  // name links in Task 15.
  onOpenChannel?: (id: string) => void;
  search: string;
  onSearchChange: (value: string) => void;
  /**
   * Identity of the jobs currently pending or running, from App's queue state.
   * Used only as a change signal: a video appears in the Library the moment its
   * download finishes, and a change here tells the list to go and look. It is
   * an identity (an id list), not a count, so one job finishing as another
   * starts still registers — see App's queueSignal.
   */
  queueSignal?: string;
  /**
   * Tells App a download was just queued from here (a re-download). Without it
   * the re-download is silent: the card leaves the ready-only grid the instant
   * its status flips to queued, and nothing else on screen — least of all the
   * rail — would say where it went until the worker happens to emit progress.
   */
  onQueued?: () => void;
}) {
  const [filter, setFilter] = useState<VideoFilter>("unwatched");
  const [category, setCategory] = useState<string>("all");
  const [sort, setSort] = useState<VideoSort>("added_newest");
  const [debouncedQuery, setDebouncedQuery] = useState("");
  const [counts, setCounts] = useState<VideoCounts | null>(null);
  const [videos, setVideos] = useState<Video[]>([]);
  const { settings } = useSettings();
  const [error, setError] = useState<string | null>(null);
  // Each list has exactly one fetching effect, and the queue's refetch is a
  // dependency of it rather than a second effect. That is what makes the
  // `active` cleanup enough: any change — a chip, a keystroke, a download
  // finishing — re-runs the effect and cancels the response of the run before,
  // so a slow request can never repaint over a newer one.
  //
  // queueSeen tells the effects whether this run is the queue's doing. A
  // background refetch must neither clear an error the user is reading (a
  // failed toggle on a card) nor surface its own failure over a grid the user
  // never touched; the list stays as it was and the next change tries again.
  const queueSeen = useRef(queueSignal);

  // Settings (for the "Expires in N days" calc) load once — nothing the user
  // does on this page changes them. The download queue used to be loaded here
  // too, purely to map job_id -> video_id for a per-card progress ring; both are
  // gone with the in-flight cards themselves.
  // The chip numbers: unscoped by chip and category, but NOT by search. A count
  // has to answer "how many would I see if I clicked this", and with a query
  // in the box the answer is scoped to that query — a chip reading 65 next to
  // a grid of 3 is the count lying about the click. The server computes them
  // with the same WHERE clauses as the grid's own fetch below, so the two can
  // never disagree about what matches — and it sends five numbers per chip
  // instead of every row in the library, which is what made a 300-video
  // library slow to open.
  //
  // countsTick is how a settled toggle asks for the numbers again: it re-runs
  // THIS effect, which reads the live debouncedQuery, rather than fetching from
  // the toggle handler. That handler is a closure over the render it was
  // clicked in, and it resolves after an await — by which time the user may
  // have typed. A fetch from there would carry the old query, claim the
  // newest epoch, and repaint every chip with whole-library numbers over a
  // searched grid, with nothing to correct them until the query changed.
  const [countsTick, setCountsTick] = useState(0);
  useEffect(() => {
    let active = true;
    getVideoCounts({ q: debouncedQuery })
      .then((c) => {
        if (active) setCounts(c);
      })
      .catch(() => {});
    return () => {
      active = false;
    };
    // queueSignal: a download finishing changes the numbers — see below.
  }, [debouncedQuery, countsTick, queueSignal]);

  // Debounce the search box so typing "abyss" fires one request, not five.
  useEffect(() => {
    const id = setTimeout(() => setDebouncedQuery(search), 250);
    return () => clearTimeout(id);
  }, [search]);

  // The chip's own filtered list, refetched whenever the active chip,
  // search query, or sort changes.
  useEffect(() => {
    let active = true;
    const byQueue = queueSeen.current !== queueSignal;
    queueSeen.current = queueSignal;
    if (!byQueue) setError(null);
    listVideos({ filter, category, q: debouncedQuery, sort })
      .then((v) => {
        if (active) setVideos(v);
      })
      .catch((e: Error) => {
        if (active && !byQueue) setError(e.message);
      });
    return () => {
      active = false;
    };
    // queueSignal: a video only enters the Library once its download
    // finishes, so the list is refetched when the set of jobs in flight
    // changes. App owns the single SSE subscription and the queue poll and
    // hands that set down as a string; it is a dependency of the two fetch
    // effects rather than a third effect of its own, so a change is one
    // request per list, with the same epoch guard as every other refetch.
  }, [filter, category, debouncedQuery, sort, queueSignal]);

  // If the selected category vanishes when the top chip changes (e.g. no
  // Music among Unwatched), fall back to All categories so the grid isn't
  // stranded on an invisible filter.
  //
  // Never while a query is in the box, though. This list is scoped to the search
  // now, so a query that happens to match no Music video would read as "Music is
  // gone" and clear the selection — and this effect only ever RESETS, so clearing
  // the box would not bring it back. A search is a temporary narrowing; it must
  // not destroy a choice the user made before typing. Under a query the category
  // chip stays lit and its count is free to read 0, which is the honest answer
  // for "Music, matching this text": nothing. The chip row keeps the selected
  // category visible even at 0 (see catChips below) so it can still be undone.
  useEffect(() => {
    if (debouncedQuery !== "" || counts === null) return;
    if (category !== "all" && !counts.categories[filter]?.[category]) {
      setCategory("all");
    }
  }, [filter, counts, debouncedQuery]); // eslint-disable-line react-hooks/exhaustive-deps

  function applyLocalUpdate(id: string, patch: Partial<Video>) {
    setVideos((prev) =>
      prev.map((v) => (v.id === id ? { ...v, ...patch } : v)),
    );
  }

  // A toggle moves a video between chips, so the numbers are asked for again
  // once the server has taken it. Not adjusted by hand: the server owns the
  // definition of every chip (see matchesFilter's note), and a wrong guess
  // here would sit on screen until the next refetch. Bumps the counts effect
  // instead of fetching here — see countsTick for why.
  function refreshCounts() {
    setCountsTick((t) => t + 1);
  }

  // Both toggles roll back on failure AND say so: a card that silently flips
  // and flips back reads as a broken button, and the user's next move is to
  // click it again. The Archive tab and the Player report the same failures;
  // this was the last of the three that didn't.
  async function handleToggleFavorite(id: string) {
    const current = videos.find((v) => v.id === id);
    if (!current) return;
    const next = !current.favorite;
    applyLocalUpdate(id, { favorite: next });
    try {
      await setFavorite(id, next);
      refreshCounts();
    } catch (e) {
      applyLocalUpdate(id, { favorite: current.favorite });
      setError((e as Error).message);
    }
  }

  async function handleToggleWatched(id: string) {
    const current = videos.find((v) => v.id === id);
    if (!current) return;
    const next = !current.watched;
    // The API answers with the watched flag alone, so the zeroed resume
    // position has to be mirrored here: without it, un-watching a card would
    // make the progress bar appear (VideoCard only draws it when !watched)
    // still showing the position the server has just cleared.
    applyLocalUpdate(id, { watched: next, resume_position_seconds: 0 });
    try {
      await setWatched(id, next);
      refreshCounts();
    } catch (e) {
      applyLocalUpdate(id, {
        watched: current.watched,
        resume_position_seconds: current.resume_position_seconds,
      });
      setError((e as Error).message);
    }
  }

  async function handleRedownload(id: string) {
    try {
      await redownload(id);
      // Only tell App — do NOT refetch here. The video is 'queued' now, which
      // the ready-only list excludes, so its card must leave the grid; but the
      // refetch belongs to the list effects, not to this handler. onQueued
      // updates App's jobs, which changes queueSignal, which re-runs those
      // effects against the CURRENT filter. Doing the refetch
      // here instead would fetch with this handler's stale-closure filter: if
      // the user changed the chip during the redownload request, this handler
      // would resolve last, claim the newest epoch, and paint the grid with the
      // old filter's rows under the new chip. Deferring to the queue effect
      // reads the live filter and also drops the redundant second refetch.
      onQueued?.();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  const retentionDays = settings?.retention_days ?? 14;

  // The active chip's list comes from the server, but an optimistic watched /
  // favorite toggle can push a card out of the current filter before any
  // refetch — e.g. marking a card watched while the Unwatched chip is active.
  // Re-apply the same predicate the server used so the card leaves the grid
  // immediately. For "all" this keeps everything (matchesFilter → true), so
  // watched videos still show inline there.
  const visible = videos.filter((v) => matchesFilter(v, filter));

  // Category row scoped to the active watch-status chip, so it only offers
  // categories that actually exist under the current top-level filter.
  const catCounts = counts?.categories[filter] ?? {};

  function renderCard(video: Video) {
    return (
      <VideoCard
        key={video.id}
        video={video}
        retentionDays={retentionDays}
        onOpen={onOpenVideo}
        onToggleFavorite={handleToggleFavorite}
        onToggleWatched={handleToggleWatched}
        onOpenChannel={onOpenChannel}
        onRedownload={handleRedownload}
      />
    );
  }

  return (
    <>
      {/* Search and sort lead the page, above the chips: they act on the whole
          grid, whereas a chip only narrows what the two of them already
          selected. The sort control sits at the far right of the same row so
          the pair reads as one toolbar rather than two stray controls. */}
      <div className="listbar">
        <SearchField
          value={search}
          onChange={onSearchChange}
          placeholder="Search titles"
          label="Search titles"
        />
        <select
          className={`${controlClass} sortsel push-end`}
          style={{ maxWidth: 190 }}
          value={sort}
          onChange={(e) => setSort(e.target.value as VideoSort)}
          aria-label="Sort"
        >
          {SORT_OPTIONS.map((o) => (
            <option key={o.id} value={o.id}>
              {o.label}
            </option>
          ))}
        </select>
      </div>
      <PillStrip lead>
        <div className="chips">
          {CHIPS.map((chip) => (
            <button
              key={chip.id}
              type="button"
              className={`chip${filter === chip.id ? " on" : ""}`}
              onClick={() => setFilter(chip.id)}
            >
              {chip.label}{" "}
              <span className="n">{counts?.filters[chip.id] ?? 0}</span>
            </button>
          ))}
        </div>
      </PillStrip>
      <PillStrip>
        <div className="catchips">
          <button
            type="button"
            className={`catchip${category === "all" ? " on" : ""}`}
            onClick={() => setCategory("all")}
          >
            All categories{" "}
            <span className="n">{counts?.filters[filter] ?? 0}</span>
          </button>
          {CATEGORIES.filter(
            (c) =>
              (catCounts[c.id] ?? 0) > 0 ||
              // The selected one always stays on the row, even when the current
              // search leaves it with nothing: it is what the grid is filtered by,
              // and a lit filter you cannot see is a filter you cannot undo.
              c.id === category,
          ).map((c) => (
            <button
              key={c.id}
              type="button"
              className={`catchip${category === c.id ? " on" : ""}`}
              onClick={() => setCategory(c.id)}
            >
              <span className="dotc" style={{ background: c.color }} />
              {c.label} <span className="n">{catCounts[c.id] ?? 0}</span>
            </button>
          ))}
        </div>
      </PillStrip>
      {error ? <div className="errline">{error}</div> : null}
      {/* .gridwrap is the size-query container the grid steps its column count
          off (see .gridwrap/.grid in index.css). It has to be a wrapper rather
          than .page: container-type implies contain: layout, which would make
          the element a containing block for fixed-position descendants, and
          ConfirmDialog's .modal-overlay renders inside the view rather than
          through a portal — it would stop covering the viewport. */}
      <div className="gridwrap">
        <div className="grid">{visible.map(renderCard)}</div>
      </div>
      {visible.length === 0 && !error ? (
        <p style={{ color: "var(--color-faint)" }}>{emptyMessage(filter)}</p>
      ) : null}
    </>
  );
}
