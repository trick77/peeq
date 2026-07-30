import { useEffect, useRef, useState } from "react";
import { Icon } from "../icons";
import { Spinner } from "../ui";
import {
  getSharedVideo,
  shareStreamUrl,
  shareSubtitlesUrl,
  shareThumbnailUrl,
  type PublicVideo,
} from "../api/share";
import { formatDuration } from "../format";
import { daysUntil } from "../components/ShareControl";
// The same scrubber the owner's Player draws, and the same skip policy. Like
// ../vtt, ../components/Scrubber imports nothing from api/, so nothing
// session-gated reaches this page through it.
import { AUTO_SKIP, categoryLabel, Scrubber } from "../components/Scrubber";
import { TranscriptCard } from "../components/TranscriptCard";
// Shared with the Player's Transcript card. ../vtt is deliberately free of any
// api/ import, so nothing session-gated can reach this page through it.
import { transcriptFilenameBase } from "../vtt";
import { DOT } from "../sep";

// Highlight timestamps use the same m:ss / h:mm:ss formatter as the rest of the
// app (Player aliases it the same way).
const fmt = formatDuration;

// expiryLabel is the footer's "link expires …" note.
function expiryLabel(video: PublicVideo): string {
  const d = daysUntil(video.expires_at);
  if (d === null) return "This is a shared video.";
  if (d <= 0) return "This link expires today.";
  if (d === 1) return "This link expires in 1 day.";
  return `This link expires in ${d} days.`;
}

// PeeqMark is the small brand lockup the footer wears (the top bar it also used
// to sit in is gone) — the same magnifier logo the rail wears, scaled down for a
// chromeless page.
function PeeqMark({ size = 26 }: { size?: number }) {
  return (
    <span className="sharepage-logo" style={{ width: size, height: size }}>
      <Icon name="search" size={`${Math.round(size * 0.5)}px`} />
    </span>
  );
}

// Share is the public, chromeless page a share link opens. It renders with no
// rail, no top bar, and no login — App branches to it above the app shell. It
// shows the video plus everything peeq knows about its content: summary,
// highlights, chapters and the searchable transcript, with the captions
// downloadable as .txt/.vtt. The video FILE itself stays watch-only — the
// stream endpoint never sends an attachment disposition, and nothing here asks
// it to. It shows a neutral dead-end for an expired/revoked link.
//
// The token is the only identifier this page ever handles. publicVideoDTO
// carries no video id and no url — peeq's video id IS the YouTube id, so its
// absence is what keeps the source video unnamed here. Download filenames come
// from the title alone for the same reason (see transcriptFilenameBase).
export function Share({ token }: { token: string | null }) {
  const [status, setStatus] = useState<"loading" | "ready" | "dead">("loading");
  const [video, setVideo] = useState<PublicVideo | null>(null);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  // Playback position + media duration, for the scrubber. The Player keeps the
  // same pair; here there is no resume position to restore and nothing to POST
  // back, so they exist purely to draw the bar.
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  // subtitlesReadyFor holds the token whose media metadata has loaded, gating
  // the <track> below — the same workaround the Player carries. On iPadOS 27
  // (public beta 1) a <track> child present while the video loads makes Safari
  // fail resource selection outright — networkState 3 (NETWORK_NO_SOURCE),
  // readyState 0, video.error stays null — so the page sits on the poster at
  // 0:00 forever with nothing detectable from JS. Mounting the track only after
  // loadedmetadata loads the media fine and keeps captions working. Keyed on
  // the token rather than a boolean so a different share opened without a
  // remount starts track-free again. See tubearchivist/tubearchivist#1196.
  const [subtitlesReadyFor, setSubtitlesReadyFor] = useState<string | null>(
    null,
  );
  // The transient "Skipped ad · 0:45" notice over the stage.
  const [toast, setToast] = useState<string | null>(null);
  const toastTimerRef = useRef<number | undefined>(undefined);

  // Clear a pending toast timer on unmount so it can't fire into a gone tree.
  useEffect(() => {
    return () => {
      if (toastTimerRef.current !== undefined) {
        window.clearTimeout(toastTimerRef.current);
      }
    };
  }, []);

  useEffect(() => {
    if (!token) {
      setStatus("dead");
      return;
    }
    let active = true;
    getSharedVideo(token)
      .then((v) => {
        if (active) {
          setVideo(v);
          setStatus("ready");
        }
      })
      .catch(() => {
        // 404 (unknown/expired/revoked) and any other failure land on the same
        // neutral dead-end — the page never reveals whether the video exists.
        if (active) setStatus("dead");
      });
    return () => {
      active = false;
    };
  }, [token]);

  useEffect(() => {
    if (video) document.title = `${video.title} · Peeq`;
    return () => {
      document.title = "Peeq";
    };
  }, [video]);

  // seek jumps the player to a chapter, highlight or cue's moment and starts it.
  function seek(ts: number) {
    const el = videoRef.current;
    if (!el) return;
    el.currentTime = ts;
    setCurrentTime(ts);
    void el.play().catch(() => {});
  }

  // scrubTo is the scrubber's own seek. It sets the position WITHOUT starting
  // playback: dragging the bar of a paused video shouldn't start it, whereas
  // clicking a chapter or a transcript line is a "take me there and play" ask.
  function scrubTo(seconds: number) {
    const el = videoRef.current;
    if (!el) return;
    el.currentTime = seconds;
    setCurrentTime(seconds);
  }

  if (status === "loading") {
    return (
      <div className="sharepage">
        <div className="sharepage-load">
          <Spinner size="22px" />
        </div>
      </div>
    );
  }

  if (status === "dead" || !video || !token) {
    return (
      <div className="sharepage">
        <div className="sharepage-dead">
          <span className="ic">
            <Icon name="clock" size="26px" />
          </span>
          <h1>This link has expired</h1>
          <p>
            The share link for this video is no longer active. Ask whoever sent
            it for a fresh one.
          </p>
        </div>
      </div>
    );
  }

  const highlights = video.key_points ?? [];
  const chapters = video.chapters ?? [];
  const segments = video.sponsorblock_segments ?? [];

  // showToast puts a message over the stage briefly. A later toast replaces an
  // earlier one rather than stacking, so the timer is always reset.
  function showToast(text: string) {
    setToast(text);
    if (toastTimerRef.current !== undefined) {
      window.clearTimeout(toastTimerRef.current);
    }
    toastTimerRef.current = window.setTimeout(() => setToast(null), 2600);
  }

  function handleLoadedMetadata() {
    const el = videoRef.current;
    // Open the subtitle gate first, ahead of everything else, so it still
    // opens if this fires more than once on the same media.
    if (token) setSubtitlesReadyFor(token);
    // NaN until real media metadata loads (and always NaN under jsdom); the
    // scrubber falls back to the DTO's duration_seconds while it is unknown.
    if (el && Number.isFinite(el.duration)) setDuration(el.duration);
  }

  // handleTimeUpdate drives the scrubber and performs the SponsorBlock skip,
  // matching the Player's behaviour exactly: jump past whichever AUTO_SKIP
  // segment the playhead just entered, and play through everything else
  // (intros, outros, recaps) — those are drawn on the bar but never cut, since
  // removing them unasked takes away video the viewer may want. A video with no
  // segments makes the loop a no-op.
  //
  // Unlike the Player there is no resume POST here: a share recipient has no
  // account, and the public routes are read-only by design.
  function handleTimeUpdate() {
    const el = videoRef.current;
    if (!el) return;
    setCurrentTime(el.currentTime);
    for (const seg of segments) {
      if (!AUTO_SKIP.has(seg.category)) continue;
      if (el.currentTime >= seg.start_time && el.currentTime < seg.end_time) {
        el.currentTime = seg.end_time;
        setCurrentTime(seg.end_time);
        showToast(
          `Skipped ${categoryLabel(seg.category)}${DOT}${formatDuration(
            seg.end_time - seg.start_time,
          )}`,
        );
        break;
      }
    }
  }

  return (
    <div className="sharepage">
      {/* No top bar. The peeq lockup and a "Shared with you" label sat here and
          told the recipient nothing they didn't already know from clicking the
          link — the footer still carries the attribution. The page now opens on
          the video itself; .sharepage-main pays the vertical inset the bar used
          to provide (see its padding). */}
      <main className="sharepage-main">
        <div className="sharepage-primary">
          {/* stage-wrap gives the toast below something to position against. */}
          <div className="stage stage-wrap">
            <video
              ref={videoRef}
              className="sharepage-video"
              controls
              poster={
                video.has_thumbnail ? shareThumbnailUrl(token) : undefined
              }
              src={shareStreamUrl(token)}
              onLoadedMetadata={handleLoadedMetadata}
              onTimeUpdate={handleTimeUpdate}
            >
              {video.has_subtitles && subtitlesReadyFor === token && (
                <track
                  kind="subtitles"
                  srcLang={video.audio_language || "en"}
                  src={shareSubtitlesUrl(token)}
                  default={false}
                />
              )}
            </video>
            <div className={`stage-toast${toast ? " show" : ""}`} role="status">
              <Icon name="skipForward" size="15px" />
              {toast}
            </div>
            {/* The scrubber earns its place here only for the SponsorBlock
                bands — the <video> keeps its native controls, which already
                seek. It is skipped entirely for a video with no segments, so a
                clean video shows one seek bar, not two. */}
            {segments.length > 0 && (
              <Scrubber
                currentSeconds={currentTime}
                durationSeconds={duration || video.duration_seconds || 0}
                segments={segments}
                onSeek={scrubTo}
              />
            )}
          </div>

          <div className="sharepage-meta">
            <h1>{video.title}</h1>
            <div className="sharepage-sub">
              <span className="ch">{video.channel_name}</span>
              {video.duration_seconds ? (
                <span className="pill">
                  {formatDuration(video.duration_seconds)}
                </span>
              ) : null}
            </div>
          </div>

          {/* Chapters, where the Player puts them: a full-width card in this
              column between the video and the Transcript, two columns wide, NOT
              a sidebar panel. The sidebar keeps Summary and Highlights, which is
              exactly the Player's split.

              Two things still differ from the Player's Contents card, both
              because this page is public: no empty state (it shouldn't
              advertise a panel it has nothing for) and no yt-dlp/MiMo source
              tag (internal trivia the recipient has no use for). */}
          {chapters.length > 0 && (
            <div className="card sharepage-chapters">
              <div className="hd">
                <Icon name="listTree" size="16px" />
                <span className="lbl">Chapters</span>
                <span className="meta">{chapters.length} chapters</span>
              </div>
              <div className="tabbody">
                <div className="toc toc-grid">
                  {chapters.map((c, i) => (
                    <button
                      key={i}
                      type="button"
                      className="row"
                      onClick={() => seek(c.ts)}
                    >
                      <span className="ts mono">{fmt(c.ts)}</span>
                      <span>
                        <span className="ttl">{c.title}</span>
                      </span>
                    </button>
                  ))}
                </div>
              </div>
            </div>
          )}

          {video.has_subtitles && (
            /* Keyed on the token for the same reason the Player is keyed on
               the video id: this page is not remounted when the token changes,
               and the panel must not carry one share's open state or search
               term over to the next. The component's URL-keyed cache already
               protects the cues themselves. */
            <TranscriptCard
              key={token}
              vttUrl={shareSubtitlesUrl(token)}
              filenameBase={transcriptFilenameBase(video.title)}
              seek={seek}
              className="sharepage-transcript"
            />
          )}

          <footer className="sharepage-foot">
            <PeeqMark size={20} />
            <span>
              Shared via{" "}
              <b>
                P<span>ee</span>q
              </b>
              {DOT}
              {expiryLabel(video)}
            </span>
          </footer>
        </div>

        <aside className="sharepage-side">
          {video.summary_status === "done" && video.summary.trim() ? (
            <div className="card">
              <div className="hd">
                <Icon name="alignLeft" size="16px" />
                <span className="lbl">Summary</span>
              </div>
              <div className="tabbody summ">
                <p>{video.summary}</p>
              </div>
            </div>
          ) : null}

          {/* Chapters are NOT here — they are a full-width card in the primary
              column, above the Transcript, exactly as the Player has them. This
              aside carries Summary and Highlights, which is the Player's split. */}
          {highlights.length > 0 && (
            <div className="card">
              <div className="hd">
                <Icon name="listTree" size="16px" />
                <span className="lbl">Highlights</span>
              </div>
              <div className="tabbody">
                <div className="hl">
                  {highlights.map((k, i) => (
                    <button
                      key={i}
                      type="button"
                      className="row"
                      onClick={() => seek(k.ts)}
                    >
                      <span className="ts mono">{fmt(k.ts)}</span>
                      <span className="txt">{k.text}</span>
                    </button>
                  ))}
                </div>
              </div>
            </div>
          )}
        </aside>
      </main>
    </div>
  );
}
