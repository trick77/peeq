import { useEffect, useRef, useState } from "react";
import { VideoCard, type DownloadProgress } from "../components/VideoCard";
import { listVideos, getSettings, listDownloads, streamDownloads, setFavorite, setWatched } from "../api";
import type { Video, VideoFilter, Job, Settings } from "../api/types";

const CHIPS: { id: VideoFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "unwatched", label: "Unwatched" },
  { id: "watched", label: "Watched" },
  { id: "favorites", label: "Favorites" },
  { id: "downloading", label: "Downloading" },
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
  const [allVideos, setAllVideos] = useState<Video[]>([]);
  const [videos, setVideos] = useState<Video[]>([]);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [progressByVideoId, setProgressByVideoId] = useState<Record<string, DownloadProgress>>({});
  const jobsRef = useRef<Job[]>([]);

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
    listVideos("all")
      .then((v) => {
        if (active) setAllVideos(v);
      })
      .catch(() => {});
    listDownloads()
      .then((j) => {
        jobsRef.current = j;
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, []);

  // The chip's own filtered list, refetched whenever the active chip
  // changes.
  useEffect(() => {
    let active = true;
    setError(null);
    listVideos(filter)
      .then((v) => {
        if (active) setVideos(v);
      })
      .catch((e: Error) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
    };
  }, [filter]);

  // Live download progress: map each SSE "progress" event's job_id to the
  // video_id the download dock/queue knows about, so a downloading card's
  // ring stays current without polling.
  useEffect(() => {
    const controller = new AbortController();
    streamDownloads((evt) => {
      if (evt.event !== "progress") return;
      const data = evt.data as { job_id: number; percent: number; eta: string };
      const job = jobsRef.current.find((j) => j.job_id === data.job_id);
      if (!job) return;
      setProgressByVideoId((prev) => ({ ...prev, [job.video_id]: { percent: data.percent, eta: data.eta } }));
    }, controller.signal).catch(() => {});
    return () => controller.abort();
  }, []);

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
          />
        ))}
      </div>
      {videos.length === 0 && !error ? <p style={{ color: "var(--color-faint)" }}>No videos yet.</p> : null}
    </>
  );
}
