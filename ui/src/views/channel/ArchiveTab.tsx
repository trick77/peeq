import { useEffect, useRef, useState } from "react";
import { VideoCard } from "../../components/VideoCard";
import { listVideos } from "../../api/videos";
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

  return (
    <>
      <div className="listbar">
        <input
          className={controlClass}
          style={{ maxWidth: 280 }}
          type="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search this channel"
          aria-label="Search this channel"
        />
        <select
          className={controlClass}
          style={{ maxWidth: 200 }}
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
        {videos.map((v) => (
          <VideoCard
            key={v.id}
            video={v}
            retentionDays={retentionDays}
            onOpen={onOpenVideo}
            onToggleFavorite={() => {}}
            onToggleWatched={() => {}}
          />
        ))}
      </div>
      {videos.length === 0 && !error ? (
        <p style={{ color: "var(--color-faint)" }}>
          {debouncedQuery || category !== "all" ? "No videos match." : "Nothing archived from this channel yet."}
        </p>
      ) : null}
    </>
  );
}
