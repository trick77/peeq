import { useEffect, useRef, useState } from "react";
import { VideoCard } from "../../components/VideoCard";
import { listVideos, setFavorite, setWatched } from "../../api/videos";
import { getSettings } from "../../api";
import { CATEGORIES } from "../../categories";
import { SORT_OPTIONS } from "../Library";
import { controlClass } from "../../ui";
import type { Video, VideoSort } from "../../api/types";

export function ArchiveTab({
  channelId,
  onOpenVideo,
}: {
  channelId: string;
  onOpenVideo: (id: string) => void;
}) {
  const [videos, setVideos] = useState<Video[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [debouncedQuery, setDebouncedQuery] = useState("");
  const [category, setCategory] = useState("all");
  const [sort, setSort] = useState<VideoSort>("newest");
  const [retentionDays, setRetentionDays] = useState(0);

  // The Archive tab keeps its own search/category/sort state rather than
  // sharing the Library's: visiting a channel must never change what the
  // Library shows when the user goes back to it.
  useEffect(() => {
    const id = setTimeout(() => setDebouncedQuery(query), 250);
    return () => clearTimeout(id);
  }, [query]);

  const loadSeq = useRef(0);

  useEffect(() => {
    const seq = ++loadSeq.current;
    setError(null);
    listVideos({ channel: channelId, q: debouncedQuery, category, sort })
      .then((vs) => {
        if (seq !== loadSeq.current) return;
        setVideos(vs);
      })
      .catch((e: Error) => {
        if (seq !== loadSeq.current) return;
        setError(e.message);
      });
  }, [channelId, debouncedQuery, category, sort]);

  useEffect(() => {
    getSettings()
      .then((s) => setRetentionDays(s.retention_days))
      .catch(() => setRetentionDays(0));
  }, []);

  // Mirrors Library's handleToggleFavorite/handleToggleWatched: flip the
  // field locally first so the card updates without a refetch, then make
  // the API call; on failure, revert the optimistic update and surface the
  // error through the tab's own error banner rather than swallowing it.
  async function handleToggleFavorite(id: string) {
    const current = videos.find((v) => v.id === id);
    if (!current) return;
    const next = !current.favorite;
    setVideos((prev) =>
      prev.map((v) => (v.id === id ? { ...v, favorite: next } : v)),
    );
    try {
      await setFavorite(id, next);
    } catch (e) {
      setVideos((prev) =>
        prev.map((v) =>
          v.id === id ? { ...v, favorite: current.favorite } : v,
        ),
      );
      setError((e as Error).message);
    }
  }

  // The watched toggle zeroes resume_position_seconds server-side in both
  // directions (videos.SetWatched), and the response carries only the watched
  // flag — so the optimistic update has to mirror the reset. Without it,
  // un-watching a partly-played video makes its progress bar appear (VideoCard
  // draws it only when !watched) still showing a position the server has
  // already cleared.
  async function handleToggleWatched(id: string) {
    const current = videos.find((v) => v.id === id);
    if (!current) return;
    const next = !current.watched;
    setVideos((prev) =>
      prev.map((v) =>
        v.id === id ? { ...v, watched: next, resume_position_seconds: 0 } : v,
      ),
    );
    try {
      await setWatched(id, next);
    } catch (e) {
      setVideos((prev) =>
        prev.map((v) =>
          v.id === id
            ? {
                ...v,
                watched: current.watched,
                resume_position_seconds: current.resume_position_seconds,
              }
            : v,
        ),
      );
      setError((e as Error).message);
    }
  }

  return (
    <>
      <div className="listbar">
        <input
          className={controlClass}
          style={{ maxWidth: 280 }}
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          // Enter means "done typing" here too — the list filtered as the words
          // were typed. Same gesture as SearchField, which this box predates.
          onKeyDown={(e) => {
            if (e.key === "Enter") e.currentTarget.blur();
          }}
          placeholder="Search this channel"
          aria-label="Search this channel"
        />
        {/* Wide enough for the longest category label ("Entertainment &
            Music") plus the chevron, so the control is not laid out against
            less room than its own options need. */}
        <select
          className={controlClass}
          style={{ maxWidth: 250 }}
          value={category}
          onChange={(e) => setCategory(e.target.value)}
          aria-label="Category"
        >
          <option value="all">All categories</option>
          {CATEGORIES.map((c) => (
            <option key={c.id} value={c.id}>
              {c.label}
            </option>
          ))}
        </select>
        <select
          className={`${controlClass} sortsel`}
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

      {/* .gridwrap is the size-query container — see the note in Library.tsx
          for why it is a wrapper and not .page. */}
      <div className="gridwrap">
        <div className="grid">
          {videos.map((v) => (
            <VideoCard
              key={v.id}
              video={v}
              retentionDays={retentionDays}
              onOpen={onOpenVideo}
              onToggleFavorite={handleToggleFavorite}
              onToggleWatched={handleToggleWatched}
            />
          ))}
        </div>
      </div>
      {videos.length === 0 && !error ? (
        <p style={{ color: "var(--color-faint)" }}>
          {debouncedQuery || category !== "all"
            ? "No videos match."
            : "Nothing archived from this channel yet."}
        </p>
      ) : null}
    </>
  );
}
