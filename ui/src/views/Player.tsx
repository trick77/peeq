import { useEffect, useRef, useState } from "react";
import { Icon } from "../icons";
import { Scrubber } from "../components/Scrubber";
import { getVideo, setFavorite, setWatched, setResume, deleteVideo, streamUrl } from "../api/videos";
import type { Video } from "../api/types";
import { formatDuration } from "../format";

// RESUME_THROTTLE_MS bounds how often `timeupdate` (which fires ~4x/sec)
// is allowed to actually POST the resume position — see handleTimeUpdate.
// visibilitychange/pagehide bypass this throttle entirely (flushOnHide
// below), so closing the tab never loses more than this much progress.
const RESUME_THROTTLE_MS = 5000;

// Player — the "Now playing" view: an HTML5 <video> stage with a custom
// scrubber (SponsorBlock overlay + auto-skip), resume tracking, and the
// favorite/watched/delete/"Watch on YouTube" action row, per the mockup's
// `.playgrid` block.
export function Player({ videoId, onDeleted }: { videoId: string | null; onDeleted: () => void }) {
  const [video, setVideo] = useState<Video | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [skipToast, setSkipToast] = useState<string | null>(null);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const videoRef = useRef<HTMLVideoElement>(null);
  const lastSentRef = useRef(0);
  // positionRef tracks the latest known playhead position independent of
  // the <video> DOM node itself. On unmount, React nulls out videoRef
  // *before* this effect's cleanup runs, so reading videoRef.current there
  // is unreliable — positionRef, updated on every timeupdate, is not.
  const positionRef = useRef(0);
  // positionRef starts at 0, which is indistinguishable from "the user
  // really is at 0:00" — without this guard, flushing on unmount before
  // loadedMetadata/timeupdate has ever set a real position would overwrite
  // a legitimately stored resume_position_seconds with 0. Only flush once
  // a real position has been observed.
  const positionKnownRef = useRef(false);
  const resumeAppliedRef = useRef(false);
  const toastTimerRef = useRef<number | undefined>(undefined);

  useEffect(() => {
    resumeAppliedRef.current = false;
    positionKnownRef.current = false;
    setVideo(null);
    setError(null);
    setCurrentTime(0);
    setDuration(0);
    if (!videoId) return;
    let active = true;
    getVideo(videoId)
      .then((v) => {
        if (active) setVideo(v);
      })
      .catch((e: Error) => {
        if (active) setError(e.message);
      });
    return () => {
      active = false;
    };
  }, [videoId]);

  // Flush the resume position immediately on tab-hide/unload, so the
  // RESUME_THROTTLE_MS window never costs more than itself worth of
  // progress even if the tab is closed mid-throttle. The cleanup function
  // also flushes on unmount — e.g. clicking back to Library, the common
  // in-SPA exit, which would otherwise silently discard up to
  // RESUME_THROTTLE_MS worth of progress. Both paths read positionRef
  // (not videoRef) so they always send the latest playhead position, even
  // once React has already detached the <video> node's ref on unmount.
  useEffect(() => {
    function flush() {
      if (!video || !positionKnownRef.current) return;
      setResume(video.id, positionRef.current).catch(() => {});
    }
    document.addEventListener("visibilitychange", flush);
    window.addEventListener("pagehide", flush);
    return () => {
      document.removeEventListener("visibilitychange", flush);
      window.removeEventListener("pagehide", flush);
      flush();
    };
  }, [video]);

  useEffect(() => {
    return () => {
      if (toastTimerRef.current !== undefined) {
        window.clearTimeout(toastTimerRef.current);
      }
    };
  }, []);

  if (!videoId) {
    return <p style={{ color: "var(--color-faint)" }}>Nothing playing. Pick a video from the Library.</p>;
  }
  if (error) {
    return <div className="errline">{error}</div>;
  }
  if (!video) {
    return <p style={{ color: "var(--color-faint)" }}>Loading…</p>;
  }

  const segments = video.sponsorblock_segments ?? [];

  function handleLoadedMetadata() {
    const el = videoRef.current;
    if (!el || !video || resumeAppliedRef.current) return;
    resumeAppliedRef.current = true;
    setDuration(el.duration);
    // el.duration is NaN until real media metadata loads (e.g. under
    // jsdom, or before the stream responds) — only use it to *withhold* a
    // too-far resume once it's a known finite number; a not-yet-known
    // duration must never block applying resume.
    const durationKnown = Number.isFinite(el.duration);
    if (video.resume_position_seconds > 0 && (!durationKnown || video.resume_position_seconds < el.duration)) {
      el.currentTime = video.resume_position_seconds;
      setCurrentTime(video.resume_position_seconds);
      positionRef.current = video.resume_position_seconds;
    }
    // Metadata having loaded means el.currentTime now reflects a real
    // position (either the applied resume above, or genuinely 0:00) —
    // safe for the unmount/hide flush to trust from here on.
    positionKnownRef.current = true;
  }

  function handleTimeUpdate() {
    const el = videoRef.current;
    if (!el || !video) return;
    setCurrentTime(el.currentTime);
    positionRef.current = el.currentTime;
    positionKnownRef.current = true;

    // SponsorBlock auto-skip: jump past whichever segment the playhead has
    // just entered. sponsorblock_segments is only ever populated once the
    // backend adds SponsorBlock support to a given video; an empty array
    // makes this a no-op.
    for (const seg of segments) {
      if (el.currentTime >= seg.start_time && el.currentTime < seg.end_time) {
        el.currentTime = seg.end_time;
        setCurrentTime(seg.end_time);
        positionRef.current = seg.end_time;
        setSkipToast(`Skipped ${seg.category} · ${formatDuration(seg.end_time - seg.start_time)}`);
        if (toastTimerRef.current !== undefined) window.clearTimeout(toastTimerRef.current);
        toastTimerRef.current = window.setTimeout(() => setSkipToast(null), 2600);
        break;
      }
    }

    const now = Date.now();
    if (now - lastSentRef.current >= RESUME_THROTTLE_MS) {
      lastSentRef.current = now;
      setResume(video.id, el.currentTime).catch(() => {});
    }
  }

  function handleSeek(seconds: number) {
    const el = videoRef.current;
    if (!el) return;
    el.currentTime = seconds;
    setCurrentTime(seconds);
    positionRef.current = seconds;
    positionKnownRef.current = true;
  }

  async function handleToggleFavorite() {
    if (!video) return;
    const next = !video.favorite;
    setVideo({ ...video, favorite: next });
    try {
      await setFavorite(video.id, next);
    } catch {
      setVideo((v) => (v ? { ...v, favorite: !next } : v));
    }
  }

  async function handleToggleWatched() {
    if (!video) return;
    const next = !video.watched;
    setVideo({ ...video, watched: next });
    try {
      await setWatched(video.id, next);
    } catch {
      setVideo((v) => (v ? { ...v, watched: !next } : v));
    }
  }

  async function handleDelete() {
    if (!video) return;
    try {
      await deleteVideo(video.id);
      onDeleted();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  return (
    <div className="playgrid">
      <div>
        <div className="stage stage-wrap">
          <video
            ref={videoRef}
            src={streamUrl(video.id)}
            controls
            onLoadedMetadata={handleLoadedMetadata}
            onTimeUpdate={handleTimeUpdate}
          />
          <div className={`skip-toast${skipToast ? " show" : ""}`}>
            <Icon name="skipForward" size="15px" />
            {skipToast}
          </div>
          <Scrubber
            currentSeconds={currentTime}
            durationSeconds={duration || video.duration_seconds || 0}
            segments={segments}
            onSeek={handleSeek}
          />
        </div>
        <div className="playmeta">
          <h1>{video.title}</h1>
          <div className="sub">
            {video.channel_name || video.channel_id}
            {video.format_used ? <span className="pill">{video.format_used}</span> : null}
            {video.filesize_bytes ? <span className="pill">{formatSize(video.filesize_bytes)}</span> : null}
          </div>
          <div className="playacts">
            <button
              type="button"
              className={`abtn${video.favorite ? " gold" : ""}`}
              onClick={handleToggleFavorite}
            >
              <Icon name={video.favorite ? "starFilled" : "star"} size="17px" />
              <span>{video.favorite ? "Kept forever" : "Keep forever"}</span>
            </button>
            <button type="button" className="abtn" onClick={handleToggleWatched}>
              <Icon name="check" size="17px" /> {video.watched ? "Mark unwatched" : "Mark watched"}
            </button>
            <button type="button" className="abtn danger" onClick={handleDelete}>
              <Icon name="trash" size="17px" /> Delete
            </button>
            <a className="abtn" href={video.url} target="_blank" rel="noreferrer">
              <Icon name="externalLink" size="17px" /> Watch on YouTube
            </a>
          </div>
        </div>
      </div>
      <aside className="side">
        <div className="panel">
          <div className="ph">
            <Icon name="alignLeft" size="17px" />
            <b>Summary</b>
          </div>
          <p className="placeholder">Coming in a later update.</p>
        </div>
        <div className="panel">
          <div className="ph">
            <Icon name="listTree" size="17px" />
            <b>Contents</b>
          </div>
          <p className="placeholder">Coming in a later update.</p>
        </div>
        <div className="panel">
          <div className="ph">
            <Icon name="search" size="17px" />
            <b>Highlights</b>
          </div>
          <p className="placeholder">Coming in a later update.</p>
        </div>
      </aside>
    </div>
  );
}

function formatSize(bytes: number): string {
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(0)} MB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${bytes} B`;
}
