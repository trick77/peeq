import { useEffect, useRef, useState } from "react";
import { VideoCard } from "../../components/VideoCard";
import { listVideos } from "../../api/videos";
import { useVideoToggles } from "../../hooks/useVideoToggles";
import { useStableCallback } from "../../hooks/useStableCallback";
import { useDebouncedValue } from "../../hooks/useDebouncedValue";
import { CATEGORIES } from "../../categories";
import { SORT_OPTIONS } from "../Library";
import { controlClass } from "../../ui";
import { useSettings } from "../../settingsStore";
import type { LibraryVideo, VideoSort } from "../../api/types";

export function ArchiveTab({
  channelId,
  onOpenVideo,
}: {
  channelId: string;
  onOpenVideo: (id: string) => void;
}) {
  const [videos, setVideos] = useState<LibraryVideo[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const debouncedQuery = useDebouncedValue(query, 250, "");
  const [category, setCategory] = useState("all");
  const [sort, setSort] = useState<VideoSort>("added_newest");
  // 0 until the settings land or if they never do: a window of nothing badges
  // no card, which beats badging the wrong ones.
  const { settings } = useSettings();
  const retentionDays = settings?.retention_days ?? 0;

  // The Archive tab keeps its own search/category/sort state rather than
  // sharing the Library's: visiting a channel must never change what the
  // Library shows when the user goes back to it.
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

  const openVideo = useStableCallback(onOpenVideo);
  const { toggleFavorite, toggleWatched } = useVideoToggles(videos, setVideos, {
    onError: setError,
  });

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
              onOpen={openVideo}
              onToggleFavorite={toggleFavorite}
              onToggleWatched={toggleWatched}
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
