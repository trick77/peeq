import { useEffect, useState } from "react";
import { VideoCard } from "../components/VideoCard";
import { PillStrip } from "../components/PillStrip";
import {
  listVideos,
  getSettings,
  setFavorite,
  setWatched,
  redownload,
} from "../api";
import type { Video, VideoFilter, VideoSort, Settings } from "../api/types";
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
export const SORT_OPTIONS: { id: VideoSort; label: string }[] = [
  { id: "newest", label: "Newest first" },
  { id: "oldest", label: "Oldest first" },
  { id: "longest", label: "Longest first" },
  { id: "title", label: "Title A–Z" },
];

// matchesFilter mirrors videos.Store.List's SQL WHERE clauses exactly (see
// backend/internal/videos/store.go), so the chip counts computed here from
// the unfiltered "all" list agree with what each chip's own listVideos(id)
// call actually returns.
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
      return v.watched;
    case "favorites":
      return v.favorite;
    case "downloading":
      return v.status === "queued" || v.status === "downloading";
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

// Library — the default view: filter chips + a grid of VideoCards, per the
// mockup's `.chips`/`.grid` blocks. The search query itself lives in App
// (it's the top bar's search box, wired there since the top bar is
// Library-only chrome) and arrives here as the `search` prop.
export function Library({
  onOpenVideo,
  onOpenChannel,
  search,
  activeDownloads = 0,
}: {
  onOpenVideo: (id: string) => void;
  // onOpenChannel — optional: wired by App (Task 11), rendered as channel
  // name links in Task 15.
  onOpenChannel?: (id: string) => void;
  search: string;
  /**
   * How many downloads are pending or running, from App's queue state. Used
   * only as a change signal: a video appears in the Library the moment its
   * download finishes, and this is what tells the list to go and look.
   */
  activeDownloads?: number;
}) {
  const [filter, setFilter] = useState<VideoFilter>("unwatched");
  const [category, setCategory] = useState<string>("all");
  const [sort, setSort] = useState<VideoSort>("newest");
  const [debouncedQuery, setDebouncedQuery] = useState("");
  const [allVideos, setAllVideos] = useState<Video[]>([]);
  const [videos, setVideos] = useState<Video[]>([]);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Unfiltered list (for chip counts) + settings (for the "Expires in N
  // days" calc) are loaded once. The download queue used to be loaded here
  // too, purely to map job_id -> video_id for a per-card progress ring;
  // both are gone with the in-flight cards themselves.
  useEffect(() => {
    let active = true;
    getSettings()
      .then((s) => {
        if (active) setSettings(s);
      })
      .catch(() => {});
    listVideos({ filter: "all" })
      .then((v) => {
        if (active) setAllVideos(v);
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, []);

  // Debounce the search box so typing "abyss" fires one request, not five.
  useEffect(() => {
    const id = setTimeout(() => setDebouncedQuery(search), 250);
    return () => clearTimeout(id);
  }, [search]);

  // The chip's own filtered list, refetched whenever the active chip,
  // search query, or sort changes.
  useEffect(() => {
    let active = true;
    setError(null);
    listVideos({ filter, category, q: debouncedQuery, sort })
      .then((v) => {
        if (active) setVideos(v);
      })
      .catch((e: Error) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
    };
  }, [filter, category, debouncedQuery, sort]);

  // If the selected category vanishes when the top chip changes (e.g. no
  // Music among Unwatched), fall back to All categories so the grid isn't
  // stranded on an invisible filter.
  useEffect(() => {
    if (
      category !== "all" &&
      !allVideos.some(
        (v) => matchesFilter(v, filter) && v.category === category,
      )
    ) {
      setCategory("all");
    }
  }, [filter, allVideos]); // eslint-disable-line react-hooks/exhaustive-deps

  // A video only enters the Library once its download finishes, so the list
  // has to refresh when one does. App.tsx already owns the single SSE
  // subscription and the queue poll for the whole session, so rather than run
  // a second copy of both here, it hands down how many downloads are in
  // flight: when that number changes, something started or finished and both
  // lists are refetched.
  useEffect(() => {
    let active = true;
    listVideos({ filter: "all" })
      .then((v) => {
        if (active) setAllVideos(v);
      })
      .catch(() => {});
    listVideos({ filter, category, q: debouncedQuery, sort })
      .then((v) => {
        if (active) setVideos(v);
      })
      .catch(() => {});
    return () => {
      active = false;
    };
    // filter/category/query/sort are deliberately NOT dependencies: their own
    // effect above already refetches on a change, and repeating them here
    // would double every request the user makes while a download runs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeDownloads]);

  function applyLocalUpdate(id: string, patch: Partial<Video>) {
    setVideos((prev) =>
      prev.map((v) => (v.id === id ? { ...v, ...patch } : v)),
    );
    setAllVideos((prev) =>
      prev.map((v) => (v.id === id ? { ...v, ...patch } : v)),
    );
  }

  // Both toggles roll back on failure AND say so: a card that silently flips
  // and flips back reads as a broken button, and the user's next move is to
  // click it again. The Archive tab and the Player report the same failures;
  // this was the last of the three that didn't.
  async function handleToggleFavorite(id: string) {
    const current =
      videos.find((v) => v.id === id) ?? allVideos.find((v) => v.id === id);
    if (!current) return;
    const next = !current.favorite;
    applyLocalUpdate(id, { favorite: next });
    try {
      await setFavorite(id, next);
    } catch (e) {
      applyLocalUpdate(id, { favorite: current.favorite });
      setError((e as Error).message);
    }
  }

  async function handleToggleWatched(id: string) {
    const current =
      videos.find((v) => v.id === id) ?? allVideos.find((v) => v.id === id);
    if (!current) return;
    const next = !current.watched;
    // The API answers with the watched flag alone, so the zeroed resume
    // position has to be mirrored here: without it, un-watching a card would
    // make the progress bar appear (VideoCard only draws it when !watched)
    // still showing the position the server has just cleared.
    applyLocalUpdate(id, { watched: next, resume_position_seconds: 0 });
    try {
      await setWatched(id, next);
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
      const [all, current] = await Promise.all([
        listVideos({ filter: "all" }),
        listVideos({ filter, category, q: debouncedQuery, sort }),
      ]);
      setAllVideos(all);
      setVideos(current);
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
  const catScope = allVideos.filter((v) => matchesFilter(v, filter));

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
      <div className="chips">
        {CHIPS.map((chip) => (
          <button
            key={chip.id}
            type="button"
            className={`chip${filter === chip.id ? " on" : ""}`}
            onClick={() => setFilter(chip.id)}
          >
            {chip.label}{" "}
            <span className="n">
              {allVideos.filter((v) => matchesFilter(v, chip.id)).length}
            </span>
          </button>
        ))}
      </div>
      <PillStrip>
        <div className="catchips">
          <button
            type="button"
            className={`catchip${category === "all" ? " on" : ""}`}
            onClick={() => setCategory("all")}
          >
            All categories <span className="n">{catScope.length}</span>
          </button>
          {CATEGORIES.filter((c) =>
            catScope.some((v) => v.category === c.id),
          ).map((c) => (
            <button
              key={c.id}
              type="button"
              className={`catchip${category === c.id ? " on" : ""}`}
              onClick={() => setCategory(c.id)}
            >
              <span className="dotc" style={{ background: c.color }} />
              {c.label}{" "}
              <span className="n">
                {catScope.filter((v) => v.category === c.id).length}
              </span>
            </button>
          ))}
        </div>
      </PillStrip>
      <div className="listbar">
        <select
          className={controlClass}
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
      {error ? <div className="errline">{error}</div> : null}
      <div className="grid">{visible.map(renderCard)}</div>
      {visible.length === 0 && !error ? (
        <p style={{ color: "var(--color-faint)" }}>{emptyMessage(filter)}</p>
      ) : null}
    </>
  );
}
