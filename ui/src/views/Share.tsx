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
// Shared with the Player's Transcript card. ../vtt is deliberately free of any
// api/ import, so nothing session-gated can reach this page through it.
import {
  highlightCue,
  matchesFind,
  parseVtt,
  transcriptFilenameBase,
  transcriptToText,
  type Cue,
} from "../vtt";

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
  // Transcript panel state — mirrors the Player's: collapsed until asked for,
  // then fetched once and parsed client-side.
  const [transcriptOpen, setTranscriptOpen] = useState(false);
  const [cues, setCues] = useState<Cue[]>([]);
  const [transcriptLoading, setTranscriptLoading] = useState(false);
  const [transcriptError, setTranscriptError] = useState<string | null>(null);
  const [find, setFind] = useState("");

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

  // Fetch + client-side parse the VTT transcript the first time the Transcript
  // card is expanded — not on page load, and not for videos without subtitles.
  // The URL is the token-gated share route; this page never knows a video id to
  // build the authenticated one with.
  useEffect(() => {
    if (!token || !transcriptOpen || !video?.has_subtitles || cues.length > 0) {
      return;
    }
    let active = true;
    setTranscriptLoading(true);
    setTranscriptError(null);
    fetch(shareSubtitlesUrl(token))
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
  }, [transcriptOpen, token, video?.has_subtitles]);

  // seek jumps the player to a chapter, highlight or cue's moment and starts it.
  function seek(ts: number) {
    const el = videoRef.current;
    if (!el) return;
    el.currentTime = ts;
    void el.play().catch(() => {});
  }

  // downloadTranscriptTxt saves the transcript as plain text, built from the
  // cues already parsed for the panel (no extra request) — the same client-side
  // Blob the Player uses. The .vtt download is a plain link to the share
  // subtitle endpoint, whose bytes this page has already fetched anyway.
  function downloadTranscriptTxt() {
    if (!video) return;
    const blob = new Blob([transcriptToText(cues)], {
      type: "text/plain;charset=utf-8",
    });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = transcriptFilenameBase(video.title) + ".txt";
    a.click();
    URL.revokeObjectURL(url);
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
  const chapters = video.chapters ?? [];
  const hitCount = cues.filter((c) => matchesFind(c.text, find)).length;

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

          {video.has_subtitles && (
            <div className="card sharepage-transcript">
              <button
                type="button"
                className="hd hd-btn"
                onClick={() => setTranscriptOpen((v) => !v)}
                aria-expanded={transcriptOpen}
              >
                <Icon
                  name="chevronRight"
                  size="16px"
                  style={{
                    transition: "transform .15s",
                    transform: transcriptOpen ? "rotate(90deg)" : "none",
                  }}
                />
                <span className="lbl">Transcript</span>
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
                      <span className="count mono">
                        {find ? `${hitCount} / ${cues.length}` : "—"}
                      </span>
                    </div>
                    {cues.length > 0 && (
                      <div className="sharepage-dl">
                        <span className="meta">Download</span>
                        <button
                          type="button"
                          className="pill"
                          onClick={downloadTranscriptTxt}
                        >
                          <Icon name="download" size="14px" /> .txt
                        </button>
                        {/* The captions, not the video: this href is the same
                            token-gated VTT the <track> above already loads, and
                            the filename comes from the title alone. */}
                        <a
                          className="pill"
                          href={shareSubtitlesUrl(token)}
                          download={
                            transcriptFilenameBase(video.title) + ".vtt"
                          }
                        >
                          <Icon name="download" size="14px" /> .vtt
                        </a>
                      </div>
                    )}
                  </div>
                  <div className="tabbody transcript-body">
                    {transcriptLoading && (
                      <p className="placeholder">Loading transcript…</p>
                    )}
                    {transcriptError && (
                      <p className="errline">{transcriptError}</p>
                    )}
                    {!transcriptLoading &&
                      !transcriptError &&
                      cues.length === 0 && (
                        <p className="placeholder">No transcript available.</p>
                      )}
                    {!transcriptLoading &&
                      !transcriptError &&
                      cues.length > 0 && (
                        <div className="transcript">
                          {cues.map((cue, i) => (
                            <button
                              key={i}
                              type="button"
                              className={`cue${matchesFind(cue.text, find) ? " hit" : ""}`}
                              onClick={() => seek(cue.ts)}
                            >
                              <span className="ts mono">{fmt(cue.ts)}</span>
                              <span className="line">
                                {highlightCue(cue.text, find)}
                              </span>
                            </button>
                          ))}
                        </div>
                      )}
                  </div>
                </>
              )}
            </div>
          )}

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

          {/* Chapters. Unlike the Player's Contents card there is no empty
              state — a public page shouldn't advertise a panel it has nothing
              for — and no yt-dlp/MiMo source tag, which is internal trivia the
              recipient has no use for. */}
          {chapters.length > 0 && (
            <div className="card">
              <div className="hd">
                <Icon name="listTree" size="16px" />
                <span className="lbl">Chapters</span>
                <span className="meta">{chapters.length} chapters</span>
              </div>
              <div className="tabbody">
                {/* Plain .toc, not the Player's two-column .toc-grid — the
                    share aside is a single narrow column. */}
                <div className="toc">
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
        </aside>
      </main>
    </div>
  );
}
