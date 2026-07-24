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

// PeeqMark is the small brand lockup used in the top bar and footer — the same
// magnifier logo the rail wears, scaled down for a chromeless page.
function PeeqMark({ size = 26 }: { size?: number }) {
  return (
    <span className="sharepage-logo" style={{ width: size, height: size }}>
      <Icon name="search" size={`${Math.round(size * 0.5)}px`} />
    </span>
  );
}

// Share is the public, chromeless page a share link opens. It renders with no
// rail, no top bar, and no login — App branches to it above the app shell. It
// shows the video plus its summary and highlights (the chosen "rich" depth),
// streams watch-only, and shows a neutral dead-end for an expired/revoked link.
export function Share({ token }: { token: string | null }) {
  const [status, setStatus] = useState<"loading" | "ready" | "dead">("loading");
  const [video, setVideo] = useState<PublicVideo | null>(null);
  const videoRef = useRef<HTMLVideoElement | null>(null);

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
    if (video) document.title = `${video.title} · peeq`;
    return () => {
      document.title = "peeq";
    };
  }, [video]);

  // seek jumps the player to a highlight's moment and starts it.
  function seek(ts: number) {
    const el = videoRef.current;
    if (!el) return;
    el.currentTime = ts;
    void el.play().catch(() => {});
  }

  if (status === "loading") {
    return (
      <div className="sharepage">
        <header className="sharepage-top">
          <PeeqMark />
          <b>
            pee<span>q</span>
          </b>
        </header>
        <div className="sharepage-load">
          <Spinner size="22px" />
        </div>
      </div>
    );
  }

  if (status === "dead" || !video || !token) {
    return (
      <div className="sharepage">
        <header className="sharepage-top">
          <PeeqMark />
          <b>
            pee<span>q</span>
          </b>
        </header>
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

  return (
    <div className="sharepage">
      <header className="sharepage-top">
        <PeeqMark />
        <b>
          pee<span>q</span>
        </b>
        <span className="spacer" />
        <span className="cta">Shared with you</span>
      </header>

      <main className="sharepage-main">
        <div className="sharepage-primary">
          <div className="stage">
            <video
              ref={videoRef}
              className="sharepage-video"
              controls
              poster={
                video.has_thumbnail ? shareThumbnailUrl(token) : undefined
              }
              src={shareStreamUrl(token)}
            >
              {video.has_subtitles && (
                <track
                  kind="subtitles"
                  srcLang={video.audio_language || "en"}
                  src={shareSubtitlesUrl(token)}
                  default={false}
                />
              )}
            </video>
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

          <footer className="sharepage-foot">
            <PeeqMark size={20} />
            <span>
              Shared via{" "}
              <b>
                pee<span>q</span>
              </b>{" "}
              · {expiryLabel(video)}
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
