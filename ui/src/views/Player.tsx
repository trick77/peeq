import { useEffect, useRef, useState, type ReactNode } from "react";
import { Icon } from "../icons";
import { Scrubber } from "../components/Scrubber";
import { getVideo, setFavorite, setWatched, setResume, deleteVideo, streamUrl } from "../api/videos";
import { resummarize, subtitlesUrl } from "../api/search";
import type { Video } from "../api/types";
import { formatDuration } from "../format";
import { writeNowPlaying, clearNowPlaying } from "../nowPlaying";

// RESUME_THROTTLE_MS bounds how often `timeupdate` (which fires ~4x/sec)
// is allowed to actually POST the resume position — see handleTimeUpdate.
// visibilitychange/pagehide bypass this throttle entirely (flushOnHide
// below), so closing the tab never loses more than this much progress.
const RESUME_THROTTLE_MS = 5000;

// fmt is the Task 17 alias for formatDuration used throughout the
// intelligence panels below (chapters/highlights/transcript cues) — kept as
// its own name to match the brief's `fmt(ts)` calls.
const fmt = formatDuration;

// Cue is one parsed WebVTT row: a start timestamp (whole seconds) plus its
// (tag-stripped) text.
type Cue = { ts: number; text: string };

// parseVtt is a small, deliberately forgiving client-side WebVTT parser —
// good enough for yt-dlp/whisper-generated subtitle tracks: it scans for
// "HH:MM:SS.mmm --> HH:MM:SS.mmm" (or "MM:SS.mmm --> ...") timing lines and
// collects every following non-blank line as that cue's text, stripping any
// inline <...> markup tags. It intentionally does not implement the full
// WebVTT spec (cue settings, NOTE blocks, styling) — peeq only needs the
// timestamp + text pairs to render a searchable, click-to-seek transcript.
export function parseVtt(text: string): Cue[] {
  const lines = text.split(/\r?\n/);
  const timingRe = /(\d{1,2}:)?\d{2}:\d{2}[.,]\d{3}\s*-->\s*(\d{1,2}:)?\d{2}:\d{2}[.,]\d{3}/;
  const cues: Cue[] = [];
  let i = 0;
  while (i < lines.length) {
    const match = lines[i].match(timingRe);
    if (!match) {
      i++;
      continue;
    }
    const start = lines[i].split("-->")[0].trim().replace(",", ".");
    const ts = parseVttTimestamp(start);
    i++;
    const textLines: string[] = [];
    while (i < lines.length && lines[i].trim() !== "") {
      textLines.push(lines[i].trim());
      i++;
    }
    const cueText = textLines
      .join(" ")
      .replace(/<[^>]+>/g, "")
      .trim();
    if (cueText) cues.push({ ts, text: cueText });
  }
  return cues;
}

function parseVttTimestamp(ts: string): number {
  const parts = ts.split(":").map(Number);
  if (parts.length === 3) return parts[0] * 3600 + parts[1] * 60 + parts[2];
  if (parts.length === 2) return parts[0] * 60 + parts[1];
  return 0;
}

function matchesFind(text: string, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return false;
  return text.toLowerCase().includes(q);
}

// highlightCue wraps every case-insensitive occurrence of `query` in
// `text` with <mark>, matching the mockup's in-player transcript find.
function highlightCue(text: string, query: string): ReactNode {
  if (!matchesFind(text, query)) return text;
  const q = query.trim();
  const escaped = q.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const splitRe = new RegExp(`(${escaped})`, "gi");
  const isMatch = new RegExp(`^${escaped}$`, "i");
  return text.split(splitRe).map((part, i) => (isMatch.test(part) ? <mark key={i}>{part}</mark> : part));
}

const DONE_STATUSES = new Set(["done", "no_transcript", "pending", "running", "error"]);

// Player — the "Now playing" view: an HTML5 <video> stage with a custom
// scrubber (SponsorBlock overlay + auto-skip), resume tracking, the
// favorite/watched/delete/"Watch on YouTube" action row, captions (Task 17),
// and the Summary/Contents/Highlights/Transcript intelligence panels from
// the approved Phase 3 mockup (mixed layout: Summary + Highlights sit beside
// the video in a sticky sidebar; Contents and the collapsible Transcript run
// full-width below it).
export function Player({
  videoId,
  seekTo,
  onSeekConsumed,
  onDeleted,
}: {
  videoId: string | null;
  // seekTo — the Task 18 jump-to-moment target (Search's onOpen, via App's
  // pendingSeek). Applied once in handleLoadedMetadata, in place of the
  // stored resume position, the same way resume itself is applied only
  // once per video load.
  seekTo?: number;
  // onSeekConsumed — fired exactly once, right after seekTo is applied in
  // handleLoadedMetadata. This makes the seek one-shot: without it, a stale
  // seekTo left set in the parent (e.g. App's pendingSeek) would replay and
  // yank the playhead back on any later remount of this component (say,
  // navigating away then back to "Now playing" via the rail), overriding
  // the user's real resume position. The parent should clear its stored
  // seek target from this callback.
  onSeekConsumed?: () => void;
  onDeleted: () => void;
}) {
  const [video, setVideo] = useState<Video | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [skipToast, setSkipToast] = useState<string | null>(null);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [ccOn, setCcOn] = useState(false);
  const [transcriptOpen, setTranscriptOpen] = useState(false);
  const [cues, setCues] = useState<Cue[]>([]);
  const [transcriptLoading, setTranscriptLoading] = useState(false);
  const [transcriptError, setTranscriptError] = useState<string | null>(null);
  const [find, setFind] = useState("");
  const [resummarizing, setResummarizing] = useState(false);
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
    setCcOn(false);
    setTranscriptOpen(false);
    setCues([]);
    setTranscriptError(null);
    setFind("");
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

  // Forget the reload-restore marker when the Player unmounts. This cleanup
  // runs on in-app navigation away (to Library, after delete, …) but NOT on a
  // real page reload/close — React tears the tree down without running effect
  // cleanups then. So navigating away means a subsequent reload lands on
  // Library, while a reload *while still on the Player* leaves the marker
  // intact and reopens the video (see nowPlaying.ts, App.tsx).
  useEffect(() => {
    return () => clearNowPlaying();
  }, []);

  // CC track starts hidden (captions off) — applied once per video whenever
  // its <track> becomes available, independent of the click handler below.
  useEffect(() => {
    const el = videoRef.current;
    if (!el || !video?.has_subtitles) return;
    const track = el.textTracks[0];
    if (track) track.mode = "hidden";
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [video?.id, video?.has_subtitles]);

  // Fetch + client-side parse the VTT transcript the first time the
  // Transcript card is expanded — not on every render, and not for videos
  // without subtitles.
  useEffect(() => {
    if (!transcriptOpen || !video?.has_subtitles || cues.length > 0) return;
    let active = true;
    setTranscriptLoading(true);
    setTranscriptError(null);
    fetch(subtitlesUrl(video.id))
      .then((res) => {
        if (!res.ok) throw new Error("failed to load transcript");
        return res.text();
      })
      .then((text) => {
        if (active) setCues(parseVtt(text));
      })
      .catch(() => {
        if (active) setTranscriptError("Failed to load transcript.");
      })
      .finally(() => {
        if (active) setTranscriptLoading(false);
      });
    return () => {
      active = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [transcriptOpen, video?.id, video?.has_subtitles]);

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
    if (seekTo !== undefined && (!durationKnown || seekTo < el.duration)) {
      el.currentTime = seekTo;
      setCurrentTime(seekTo);
      positionRef.current = seekTo;
      onSeekConsumed?.();
    } else if (video.resume_position_seconds > 0 && (!durationKnown || video.resume_position_seconds < el.duration)) {
      el.currentTime = video.resume_position_seconds;
      setCurrentTime(video.resume_position_seconds);
      positionRef.current = video.resume_position_seconds;
    }
    // Metadata having loaded means el.currentTime now reflects a real
    // position (either the applied resume above, or genuinely 0:00) —
    // safe for the unmount/hide flush to trust from here on.
    positionKnownRef.current = true;
    // Record the video as opened-but-paused. The play/pause/ended handlers
    // below keep this in sync; writing false here (rather than leaving a
    // stale true from a previous session) ensures a video that reopens paused
    // after a reload won't itself trigger another reopen on the next reload.
    writeNowPlaying(video.id, false);
  }

  // Keep the reload-restore marker in sync with the real <video> play state:
  // playing=true only while media is actually advancing, false once paused or
  // ended. App reads this on init and reopens the Player only when it reads
  // true — i.e. the video was playing at the instant of the reload.
  function handlePlay() {
    if (video) writeNowPlaying(video.id, true);
  }

  function handlePauseOrEnded() {
    if (video) writeNowPlaying(video.id, false);
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

  // seek — shared by the scrubber and every intelligence-panel click target
  // (chapters, highlights, transcript cues): sets the <video>'s currentTime
  // directly and keeps the state/positionRef bookkeeping in sync.
  function seek(seconds: number) {
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

  // CC toggle — flips the <track>'s TextTrack.mode between 'showing' and
  // 'hidden' directly (imperative, mirroring the native captions button),
  // keeping ccOn in sync purely for the button's "on" styling.
  function handleToggleCC() {
    const el = videoRef.current;
    const track = el?.textTracks?.[0];
    if (track) {
      track.mode = track.mode === "showing" ? "hidden" : "showing";
      setCcOn(track.mode === "showing");
    } else {
      setCcOn((v) => !v);
    }
  }

  async function handleResummarize() {
    if (!video) return;
    setResummarizing(true);
    try {
      await resummarize(video.id);
    } finally {
      setResummarizing(false);
    }
  }

  const hitCount = find ? cues.filter((c) => matchesFind(c.text, find)).length : 0;

  return (
    <div className="playgrid">
      <div className="leftcol">
        <div className="stage stage-wrap">
          <video
            ref={videoRef}
            src={streamUrl(video.id)}
            controls
            onLoadedMetadata={handleLoadedMetadata}
            onTimeUpdate={handleTimeUpdate}
            onPlay={handlePlay}
            onPause={handlePauseOrEnded}
            onEnded={handlePauseOrEnded}
          >
            {video.has_subtitles && (
              <track kind="subtitles" srcLang={video.audio_language || "en"} src={subtitlesUrl(video.id)} default={false} />
            )}
          </video>
          <div className={`skip-toast${skipToast ? " show" : ""}`}>
            <Icon name="skipForward" size="15px" />
            {skipToast}
          </div>
          <Scrubber
            currentSeconds={currentTime}
            durationSeconds={duration || video.duration_seconds || 0}
            segments={segments}
            onSeek={seek}
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
            {video.has_subtitles && (
              <button
                type="button"
                className={`abtn cc-btn${ccOn ? " on" : ""}`}
                onClick={handleToggleCC}
              >
                <Icon name="captions" size="17px" /> CC
              </button>
            )}
            <button type="button" className="abtn danger" onClick={handleDelete}>
              <Icon name="trash" size="17px" /> Delete
            </button>
            <a className="abtn" href={video.url} target="_blank" rel="noreferrer">
              <Icon name="externalLink" size="17px" /> Watch on YouTube
            </a>
          </div>
        </div>

        <div className="belowvideo">
          <div className="card full">
            <div className="hd">
              <Icon name="listTree" size="16px" />
              <span className="lbl">Contents</span>
              {video.chapters.length > 0 && (
                <span className="meta">{video.chapters.length} chapters · click to seek</span>
              )}
            </div>
            <div className="tabbody">
              {video.chapters.length === 0 ? (
                <p className="placeholder">No chapters.</p>
              ) : (
                <div className="toc toc-grid">
                  {video.chapters.map((c, i) => (
                    <button key={i} type="button" className="row" onClick={() => seek(c.ts)}>
                      <span className="ts mono">{fmt(c.ts)}</span>
                      <span>
                        <span className="ttl">{c.title}</span>
                      </span>
                      {c.source === "yt-dlp" && <span className="src">yt-dlp</span>}
                    </button>
                  ))}
                </div>
              )}
            </div>
          </div>

          {video.has_subtitles && (
            <div className="card full">
              <button
                type="button"
                className="hd hd-btn"
                onClick={() => setTranscriptOpen((v) => !v)}
                aria-expanded={transcriptOpen}
              >
                <Icon
                  name="chevronRight"
                  size="16px"
                  style={{ transition: "transform .15s", transform: transcriptOpen ? "rotate(90deg)" : "none" }}
                />
                <span className="lbl">Transcript</span>
                <span className="meta">searchable · click to seek</span>
              </button>
              {transcriptOpen && (
                <>
                  <div className="tsearch">
                    <div className="searchbox">
                      <Icon name="search" size="16px" />
                      <input
                        placeholder="Find in transcript…"
                        value={find}
                        onChange={(e) => setFind(e.target.value)}
                      />
                      <span className="count mono">{find ? `${hitCount} / ${cues.length}` : "—"}</span>
                    </div>
                  </div>
                  <div className="tabbody transcript-body">
                    {transcriptLoading && <p className="placeholder">Loading transcript…</p>}
                    {transcriptError && <p className="errline">{transcriptError}</p>}
                    {!transcriptLoading && !transcriptError && cues.length === 0 && (
                      <p className="placeholder">No transcript available.</p>
                    )}
                    {!transcriptLoading && !transcriptError && cues.length > 0 && (
                      <div className="transcript">
                        {cues.map((cue, i) => (
                          <button
                            key={i}
                            type="button"
                            className={`cue${matchesFind(cue.text, find) ? " hit" : ""}`}
                            onClick={() => seek(cue.ts)}
                          >
                            <span className="ts mono">{fmt(cue.ts)}</span>
                            <span className="line">{highlightCue(cue.text, find)}</span>
                          </button>
                        ))}
                      </div>
                    )}
                  </div>
                </>
              )}
            </div>
          )}
        </div>
      </div>

      <aside className="side">
        <div className="card">
          <div className="hd">
            <Icon name="alignLeft" size="16px" />
            <span className="lbl">Summary</span>
          </div>
          <div className="tabbody summ">
            {video.summary_status === "done" &&
              (video.summary.trim() ? (
                video.summary
                  .split("\n\n")
                  .filter((p) => p.trim())
                  .map((p, i) => <p key={i}>{p}</p>)
              ) : (
                <p className="placeholder">No summary text.</p>
              ))}
            {video.summary_status === "no_transcript" && <p className="placeholder">No transcript available.</p>}
            {(video.summary_status === "pending" || video.summary_status === "running") && (
              <p className="placeholder">Summarizing…</p>
            )}
            {video.summary_status === "error" && (
              <>
                <p className="errline">Summarization failed.</p>
                <button type="button" className="abtn" onClick={handleResummarize} disabled={resummarizing}>
                  <Icon name="download" size="15px" /> {resummarizing ? "Queuing…" : "Re-summarize"}
                </button>
              </>
            )}
            {!DONE_STATUSES.has(video.summary_status) && <p className="placeholder">No summary yet.</p>}
          </div>
        </div>

        <div className="card">
          <div className="hd">
            <Icon name="star" size="16px" />
            <span className="lbl">Highlights</span>
          </div>
          <div className="tabbody">
            {video.key_points.length === 0 ? (
              <p className="placeholder">No highlights.</p>
            ) : (
              <div className="hl">
                {video.key_points.map((k, i) => (
                  <button key={i} type="button" className="row" onClick={() => seek(k.ts)}>
                    <Icon name="starFilled" size="15px" style={{ color: "var(--color-kept)" }} />
                    <span className="ts mono">{fmt(k.ts)}</span>
                    <span className="txt">{k.text}</span>
                  </button>
                ))}
              </div>
            )}
          </div>
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
