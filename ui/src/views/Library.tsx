import { useEffect, useState } from "react";
import { VideoCard } from "../components/VideoCard";
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

// No "Watched" chip: the "Already watched" drawer at the foot of every view
// is where watched videos live now, so a chip for them would be a second,
// worse route to the same list — one that also swaps the whole page out
// instead of unfolding in place. The filter itself still exists (the type
// mirrors what videos.Store.List accepts, and matchesFilter still covers it),
// it simply has no button.
const CHIPS: { id: VideoFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "unwatched", label: "Unwatched" },
  { id: "favorites", label: "Favorites" },
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
      return (
        (v.status === "downloaded" ||
          v.status === "queued" ||
          v.status === "downloading") &&
        !v.watched
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

// WATCHED_OPEN_KEY holds whether the "Already watched" drawer is unfolded.
// localStorage, not sessionStorage: this is a lasting preference (someone who
// wants their history in view wants it in view tomorrow too), unlike the
// per-tab markers this app has used elsewhere. Both accessors are guarded —
// a disabled or full store (private mode, SSR) must never break the Library.
const WATCHED_OPEN_KEY = "peeq.library.watchedOpen";

function readWatchedOpen(): boolean {
  try {
    return localStorage.getItem(WATCHED_OPEN_KEY) === "1";
  } catch {
    return false;
  }
}

function writeWatchedOpen(open: boolean) {
  try {
    localStorage.setItem(WATCHED_OPEN_KEY, open ? "1" : "0");
  } catch {
    // Ignore: the drawer still works for this session, it just won't persist.
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
  const [filter, setFilter] = useState<VideoFilter>("all");
  const [watchedOpen, setWatchedOpen] = useState<boolean>(readWatchedOpen);
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

  // The Library leads with what is still to watch: already-watched videos are
  // split out of the main grid and folded into a drawer below it. The
  // "watched" filter is the one case where the split would leave an empty
  // grid, so there everything stays where it is. No chip selects it any more,
  // but the guard stays: the filter is still a valid state, and a view that
  // renders nothing but a drawer would be a nasty way to find that out.
  const splitWatched = filter !== "watched";
  const queue = splitWatched ? videos.filter((v) => !v.watched) : videos;
  const watchedVideos = splitWatched ? videos.filter((v) => v.watched) : [];

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
      <div className="catchips">
        <button
          type="button"
          className={`catchip${category === "all" ? " on" : ""}`}
          onClick={() => setCategory("all")}
        >
          All categories <span className="n">{allVideos.length}</span>
        </button>
        {CATEGORIES.filter((c) =>
          allVideos.some((v) => v.category === c.id),
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
              {allVideos.filter((v) => v.category === c.id).length}
            </span>
          </button>
        ))}
      </div>
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
      <div className="grid">{queue.map(renderCard)}</div>
      {queue.length === 0 && !error ? (
        <p style={{ color: "var(--color-faint)" }}>
          {watchedVideos.length > 0
            ? "Nothing left to watch."
            : "No videos yet."}
        </p>
      ) : null}
      {watchedVideos.length > 0 ? (
        <details
          className="drawer"
          open={watchedOpen}
          onToggle={(e) => {
            const open = (e.currentTarget as HTMLDetailsElement).open;
            setWatchedOpen(open);
            writeWatchedOpen(open);
          }}
        >
          <summary className="drawer-head">
            <span className="drawer-title">
              Already watched <span className="n">{watchedVideos.length}</span>
            </span>
            <span className="drawer-btn">
              <span className="caret" aria-hidden="true">
                ▾
              </span>
              {watchedOpen ? "Hide" : "Show"}
            </span>
          </summary>
          <div className="drawer-body">
            <div className="grid">{watchedVideos.map(renderCard)}</div>
          </div>
        </details>
      ) : null}
    </>
  );
}
