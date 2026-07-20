import { useEffect, useRef, useState } from "react";
import { VideoCard, type DownloadProgress } from "../components/VideoCard";
import { listVideos, getSettings, listDownloads, streamDownloads, setFavorite, setWatched, redownload } from "../api";
import type { Video, VideoFilter, VideoSort, Job, Settings } from "../api/types";
import { CATEGORIES } from "../categories";
import { controlClass } from "../ui";

const CHIPS: { id: VideoFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "unwatched", label: "Unwatched" },
  { id: "watched", label: "Watched" },
  { id: "favorites", label: "Favorites" },
  { id: "downloading", label: "Downloading" },
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
      return v.status === "downloaded" && !v.watched;
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

// Library — the default view: filter chips + a grid of VideoCards, per the
// mockup's `.chips`/`.grid` blocks.
export function Library({ onOpenVideo }: { onOpenVideo: (id: string) => void }) {
  const [filter, setFilter] = useState<VideoFilter>("all");
  const [category, setCategory] = useState<string>("all");
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<VideoSort>("newest");
  const [debouncedQuery, setDebouncedQuery] = useState("");
  const [allVideos, setAllVideos] = useState<Video[]>([]);
  const [videos, setVideos] = useState<Video[]>([]);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [progressByVideoId, setProgressByVideoId] = useState<Record<string, DownloadProgress>>({});
  const jobsRef = useRef<Job[]>([]);
  // jobsRefreshTick forces the polling effect below to re-evaluate
  // jobsRef's hasActive state after jobsRef is (re)populated — jobsRef
  // itself is a ref, so mutating it alone doesn't trigger a re-render.
  const [jobsRefreshTick, setJobsRefreshTick] = useState(0);

  // Unfiltered list (for chip counts) + settings (for the "Expires in N
  // days" calc) + the download queue (to map job_id -> video_id for the
  // SSE progress feed below) are all loaded once.
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
    listDownloads()
      .then((j) => {
        jobsRef.current = j;
        if (active) setJobsRefreshTick((n) => n + 1);
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, []);

  // Debounce the search box so typing "abyss" fires one request, not five.
  useEffect(() => {
    const id = setTimeout(() => setDebouncedQuery(query), 250);
    return () => clearTimeout(id);
  }, [query]);

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

  // Live download progress: map each SSE "progress" event's job_id to the
  // video_id the download dock/queue knows about, so a downloading card's
  // ring stays current without polling.
  //
  // jobsRef is only ever populated once, at mount, from listDownloads() —
  // so a download queued afterward (e.g. from the Add view) produces
  // progress events whose job_id isn't in the map yet, and its card would
  // never show a ring. Mirror App.tsx's catch-up refetch: on an unknown
  // job_id, refetch listDownloads() once to learn the new mapping (guarded
  // so a burst of progress events for the same unknown job only triggers
  // one in-flight refetch, not one per event).
  useEffect(() => {
    const controller = new AbortController();
    let refetching = false;
    streamDownloads((evt) => {
      if (evt.event !== "progress") return;
      const data = evt.data as { job_id: number; percent: number; eta: string };
      const job = jobsRef.current.find((j) => j.job_id === data.job_id);
      if (!job) {
        if (!refetching) {
          refetching = true;
          listDownloads()
            .then((j) => {
              jobsRef.current = j;
              setJobsRefreshTick((n) => n + 1);
            })
            .catch(() => {})
            .finally(() => {
              refetching = false;
            });
        }
        return;
      }
      setProgressByVideoId((prev) => ({ ...prev, [job.video_id]: { percent: data.percent, eta: data.eta } }));
    }, controller.signal).catch(() => {});
    return () => controller.abort();
  }, []);

  // While any download is pending/running, periodically refresh the job
  // list (jobsRef) plus the unfiltered video list (chip counts) and the
  // active chip's own list, so a finished download's status/counts don't
  // drift stale — there is no SSE "job finished" event, only "progress"
  // (see App.tsx's poller for the same reasoning). jobsRefreshTick (bumped
  // wherever jobsRef.current is written) forces this effect to re-evaluate
  // hasActive after each poll; the timeout self-stops once jobsRef reports
  // nothing left in flight.
  useEffect(() => {
    const hasActive = jobsRef.current.some((j) => j.state === "pending" || j.state === "running");
    if (!hasActive) return;
    let active = true;
    const id = window.setTimeout(() => {
      listDownloads()
        .then((j) => {
          jobsRef.current = j;
        })
        .catch(() => {})
        .finally(() => {
          if (!active) return;
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
          setJobsRefreshTick((n) => n + 1);
        });
    }, 3000);
    return () => {
      active = false;
      window.clearTimeout(id);
    };
  }, [filter, category, debouncedQuery, sort, jobsRefreshTick]);

  function applyLocalUpdate(id: string, patch: Partial<Video>) {
    setVideos((prev) => prev.map((v) => (v.id === id ? { ...v, ...patch } : v)));
    setAllVideos((prev) => prev.map((v) => (v.id === id ? { ...v, ...patch } : v)));
  }

  async function handleToggleFavorite(id: string) {
    const current = videos.find((v) => v.id === id) ?? allVideos.find((v) => v.id === id);
    if (!current) return;
    const next = !current.favorite;
    applyLocalUpdate(id, { favorite: next });
    try {
      await setFavorite(id, next);
    } catch {
      applyLocalUpdate(id, { favorite: current.favorite });
    }
  }

  async function handleToggleWatched(id: string) {
    const current = videos.find((v) => v.id === id) ?? allVideos.find((v) => v.id === id);
    if (!current) return;
    const next = !current.watched;
    applyLocalUpdate(id, { watched: next });
    try {
      await setWatched(id, next);
    } catch {
      applyLocalUpdate(id, { watched: current.watched });
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
            {chip.label} <span className="n">{allVideos.filter((v) => matchesFilter(v, chip.id)).length}</span>
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
        {CATEGORIES.filter((c) => allVideos.some((v) => v.category === c.id)).map((c) => (
          <button
            key={c.id}
            type="button"
            className={`catchip${category === c.id ? " on" : ""}`}
            onClick={() => setCategory(c.id)}
          >
            <span className="dotc" style={{ background: c.color }} />
            {c.label} <span className="n">{allVideos.filter((v) => v.category === c.id).length}</span>
          </button>
        ))}
      </div>
      <div className="listbar">
        <input
          className={controlClass}
          style={{ maxWidth: 280 }}
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search titles"
          aria-label="Search titles"
        />
        <select
          className={controlClass}
          style={{ maxWidth: 180 }}
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
      <div className="grid">
        {videos.map((video) => (
          <VideoCard
            key={video.id}
            video={video}
            retentionDays={retentionDays}
            progress={progressByVideoId[video.id]}
            onOpen={onOpenVideo}
            onToggleFavorite={handleToggleFavorite}
            onToggleWatched={handleToggleWatched}
            onRedownload={handleRedownload}
          />
        ))}
      </div>
      {videos.length === 0 && !error ? <p style={{ color: "var(--color-faint)" }}>No videos yet.</p> : null}
    </>
  );
}
