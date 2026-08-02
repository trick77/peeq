import { useCallback, useEffect, useRef, useState } from "react";
import type { MouseEvent } from "react";
import { Icon } from "../icons";
import { AUTO_SKIP } from "../components/Scrubber";
import { formatDuration } from "../format";
import { hostedVideo, park, useParkedAt } from "../videoHost";
import type { NowPlaying } from "../nowPlaying";

// SKIP_BACK / SKIP_FORWARD are deliberately asymmetric. Back is for "what did
// they just say" — short, and pressed repeatedly — while forward is for
// getting past something, so it wants to cover ground. Forward is NOT an
// ad-skip button: SponsorBlock auto-skip already handles sponsors, and the
// segments it will handle are drawn on the progress line below.
const SKIP_BACK = 10;
const SKIP_FORWARD = 30;

// NowDock — the now-playing bar along the bottom of the page column.
//
// It exists because the <video> no longer dies when you leave the player page
// (see videoHost): something has to say what is still playing, and give you
// the two or three controls worth reaching for without going back. It draws no
// video of its own — the real element parks in the tile below, so what you see
// there is the film, not a thumbnail of it.
//
// Hidden while the video is parked on the player's stage rather than on
// `view === "player"`. Those are not the same question: the summary page
// shares the player route (a video read from the Inbox lives at /video/<id>
// too), and while reading one the dock must still show, because something
// really is still playing behind it.
export function NowDock({
  playing,
  onOpenPlayer,
  onStop,
}: {
  playing: NowPlaying | null;
  onOpenPlayer: () => void;
  onStop: () => void;
}) {
  const slotRef = useRef<HTMLButtonElement>(null);
  const parkedAt = useParkedAt();
  const [paused, setPaused] = useState(true);
  const [current, setCurrent] = useState(0);
  // The element reports its own duration, which is the one that counts: the
  // stored duration_seconds comes from yt-dlp's metadata and can be a second
  // or two out, and a progress line that reaches the right-hand edge early is
  // more obviously wrong than one that is briefly short.
  const [duration, setDuration] = useState(0);

  const visible = !!playing && parkedAt !== "stage";

  // Register the tile as the dock slot whenever the bar is on screen. videoHost
  // prefers the stage whenever both are registered, so this can be
  // unconditional in the visible case — leaving the player page is what makes
  // the stage let go, and the video lands here on the same commit.
  useEffect(() => {
    if (!visible) {
      park("dock", null);
      return;
    }
    park("dock", slotRef.current);
    return () => park("dock", null);
  }, [visible]);

  // Track the element rather than being told about it. The player page already
  // owns every listener that matters (resume, sleep timer, auto-skip); adding
  // a second listener for a read-only readout keeps the dock from having to be
  // wired through the component that does.
  //
  // Re-subscribed on the playing id because the element is replaced when the
  // open video changes — the same element identity cannot be assumed across
  // videos, only across navigation.
  useEffect(() => {
    if (!visible) return;
    const el = hostedVideo();
    if (!el) return;
    const sync = () => {
      setPaused(el.paused);
      setCurrent(el.currentTime);
      setDuration(Number.isFinite(el.duration) ? el.duration : 0);
    };
    sync();
    el.addEventListener("timeupdate", sync);
    el.addEventListener("play", sync);
    el.addEventListener("pause", sync);
    el.addEventListener("loadedmetadata", sync);
    el.addEventListener("ended", sync);
    return () => {
      el.removeEventListener("timeupdate", sync);
      el.removeEventListener("play", sync);
      el.removeEventListener("pause", sync);
      el.removeEventListener("loadedmetadata", sync);
      el.removeEventListener("ended", sync);
    };
  }, [visible, playing?.id]);

  // Every control drives the element directly. That is the point: a seek made
  // here fires the same timeupdate the player page's handler is listening to,
  // so the resume position, the sleep timer's budget and SponsorBlock
  // auto-skip all stay correct without this component knowing they exist.
  const nudge = useCallback((by: number) => {
    const el = hostedVideo();
    if (!el) return;
    const max = Number.isFinite(el.duration) ? el.duration : Infinity;
    el.currentTime = Math.min(Math.max(el.currentTime + by, 0), max);
  }, []);

  const toggle = useCallback(() => {
    const el = hostedVideo();
    if (!el) return;
    // play() rejects under the autoplay policy and returns nothing at all
    // under jsdom, and neither is worth surfacing from a transport button.
    if (el.paused) void el.play()?.catch(() => {});
    else el.pause();
  }, []);

  // The element's own duration when it has one, the stored metadata until
  // then, so the line is proportioned correctly before the media loads.
  const total = duration || playing?.durationSeconds || 0;

  const scrub = useCallback(
    (e: MouseEvent<HTMLDivElement>) => {
      const el = hostedVideo();
      if (!el || !total) return;
      const box = e.currentTarget.getBoundingClientRect();
      const ratio = (e.clientX - box.left) / box.width;
      el.currentTime = Math.min(Math.max(ratio, 0), 1) * total;
    },
    [total],
  );

  if (!visible || !playing) return null;

  const pct = total > 0 ? Math.min(current / total, 1) * 100 : 0;
  const left = total > 0 ? Math.max(total - current, 0) : 0;

  return (
    <div className="nowdock">
      {/* A 3px line, not a scrubber: it says where you are and takes a click
          to move, but the real bar — with its labelled SponsorBlock bands and
          its hover readout — belongs to the player page. The bands are drawn
          here all the same, because the stage toast that announces a skip has
          nowhere to appear once you have left, and a jump with no warning and
          no explanation reads as a bug. */}
      {/* No role and no keyboard handler, deliberately. It is a 3px line: a
          focus ring on it would be unreadable, and role="presentation" on
          something that takes a click lies to assistive tech about what it is.
          Nothing here is keyboard-only — the transport buttons beside it are
          focusable, and the position it reports is spoken by the time readout
          below. */}
      <div className="nowdock-progress" onClick={scrub} title="Seek">
        <i style={{ width: `${pct}%` }} />
        {total > 0 &&
          playing.segments
            .filter((s) => AUTO_SKIP.has(s.category))
            .map((s, i) => (
              <s
                key={`${s.category}-${s.start_time}-${i}`}
                style={{
                  left: `${(s.start_time / total) * 100}%`,
                  width: `${Math.max(((s.end_time - s.start_time) / total) * 100, 0.4)}%`,
                }}
              />
            ))}
      </div>
      <div className="nowdock-row">
        {/* The tile is the slot the real <video> parks into, and it is also
            the button back to the player: what you are looking at is what you
            would be taken to.

            A <video> inside a <button> is only valid while the video is not
            itself interactive — which is exactly why the element drops its
            native controls in the dock (see Player). Putting controls back
            here would make the markup invalid and bury this click target
            under them. */}
        <button
          type="button"
          className="nowdock-vid"
          ref={slotRef}
          onClick={onOpenPlayer}
          aria-label={`Back to ${playing.title}`}
        />
        <div className="nowdock-txt">
          <button
            type="button"
            className="nowdock-title"
            onClick={onOpenPlayer}
          >
            {playing.title}
          </button>
          <span className="nowdock-sub">
            {playing.channelName}
            {left > 0 ? (
              <>
                <span className="dot">·</span>
                {formatDuration(left)} left
              </>
            ) : null}
          </span>
        </div>
        <span className="nowdock-time">
          {formatDuration(current)} / {formatDuration(total)}
        </span>
        <div className="nowdock-acts">
          <button
            type="button"
            className="nowdock-btn skip"
            onClick={() => nudge(-SKIP_BACK)}
            aria-label={`Back ${SKIP_BACK} seconds`}
          >
            <Icon name="rotateCcw" size="18px" />
          </button>
          <button
            type="button"
            className="nowdock-play"
            onClick={toggle}
            aria-label={paused ? "Play" : "Pause"}
          >
            <Icon name={paused ? "playFilled" : "pauseFilled"} size="15px" />
          </button>
          <button
            type="button"
            className="nowdock-btn skip"
            onClick={() => nudge(SKIP_FORWARD)}
            aria-label={`Forward ${SKIP_FORWARD} seconds`}
          >
            <Icon name="rotateCw" size="18px" />
          </button>
          <span className="nowdock-div" aria-hidden="true" />
          <button
            type="button"
            className="nowdock-btn"
            onClick={onOpenPlayer}
            aria-label="Back to the player"
          >
            <Icon name="chevronUp" size="18px" />
          </button>
          {/* Stops playback AND drops the server-side now-playing pointer, so
              the rail stops offering to reopen something you just closed.
              Pausing does not — that is still watching, just not right now. */}
          <button
            type="button"
            className="nowdock-btn"
            onClick={onStop}
            aria-label="Stop and close"
          >
            <Icon name="close" size="18px" />
          </button>
        </div>
      </div>
    </div>
  );
}
