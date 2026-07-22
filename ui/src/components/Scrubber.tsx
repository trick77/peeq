import type { MouseEvent } from "react";
import { formatDuration } from "../format";

export type SponsorblockSegment = {
  category: string;
  start_time: number;
  end_time: number;
};

// AUTO_SKIP is the set of categories the player jumps past on its own: paid
// sponsor reads, unpaid self-promotion, and "like and subscribe" reminders.
// Everything else the backend stores — intros, outros, recaps, filler
// tangents, non-music sections — is drawn on the bar and plays normally, so a
// part of the video is never removed without the viewer asking.
//
// It lives here, next to the segment type, because BOTH the scrubber (which
// band style to draw) and the player (what to skip) read it. Two copies would
// drift, and a bar that stripes a segment as skipped while the player plays it
// is worse than either behaviour on its own.
export const AUTO_SKIP = new Set(["sponsor", "selfpromo", "interaction"]);

// CATEGORY_LABELS names the categories in the viewer's words rather than the
// API's slugs, for the band tooltip and the skip toast. An unlisted category
// falls back to its slug: SponsorBlock can add one at any time, and showing
// "hook" is better than showing nothing.
const CATEGORY_LABELS: Record<string, string> = {
  sponsor: "ad",
  selfpromo: "self-promotion",
  interaction: "subscribe reminder",
  intro: "intro",
  outro: "outro",
  preview: "recap",
  filler: "filler",
  music_offtopic: "non-music section",
};

export function categoryLabel(category: string): string {
  return CATEGORY_LABELS[category] ?? category;
}

// Scrubber — the mockup's `.scrub`/`.scrub-times` block: a seek bar with
// the played fraction plus SponsorBlock segments overlaid as
// diagonal-striped bands. Clicking/dragging seeks via onSeek (a fraction
// [0,1] of duration); Player.tsx converts that to currentTime.
export function Scrubber({
  currentSeconds,
  durationSeconds,
  segments,
  onSeek,
}: {
  currentSeconds: number;
  durationSeconds: number;
  segments: SponsorblockSegment[];
  onSeek: (seconds: number) => void;
}) {
  const duration = durationSeconds > 0 ? durationSeconds : 0;
  const playedPercent =
    duration > 0 ? Math.min(100, (currentSeconds / duration) * 100) : 0;

  function handleClick(e: MouseEvent<HTMLDivElement>) {
    if (duration <= 0) return;
    const rect = e.currentTarget.getBoundingClientRect();
    const fraction = Math.min(
      1,
      Math.max(0, (e.clientX - rect.left) / rect.width),
    );
    onSeek(fraction * duration);
  }

  return (
    <div className="scrub-wrap">
      <div
        className="scrub"
        onClick={handleClick}
        role="slider"
        aria-label="Seek"
        aria-valuenow={currentSeconds}
      >
        {segments.map((seg, i) => {
          if (duration <= 0) return null;
          const left = (seg.start_time / duration) * 100;
          const width = ((seg.end_time - seg.start_time) / duration) * 100;
          const skipped = AUTO_SKIP.has(seg.category);
          return (
            <div
              key={`${seg.category}-${i}`}
              className={skipped ? "sb" : "sb mark"}
              style={{ left: `${left}%`, width: `${width}%` }}
              title={
                skipped
                  ? `Skipped automatically: ${categoryLabel(seg.category)}`
                  : `Marked: ${categoryLabel(seg.category)}`
              }
            />
          );
        })}
        <div className="played" style={{ width: `${playedPercent}%` }} />
      </div>
      <div className="scrub-times">
        <span className="mono">{formatDuration(currentSeconds)}</span>
        {/* The legend names both band styles, and only the ones actually on
            the bar: claiming segments are "auto-skipped" while some of them
            deliberately play would misdescribe what just happened. */}
        {segments.length > 0 ? (
          <span className="sb-legend">
            {segments.some((s) => AUTO_SKIP.has(s.category)) ? (
              <span>
                <i /> skipped
              </span>
            ) : null}
            {segments.some((s) => !AUTO_SKIP.has(s.category)) ? (
              <span>
                <i className="mark" /> marked
              </span>
            ) : null}
          </span>
        ) : (
          <span />
        )}
        <span className="mono">{formatDuration(duration)}</span>
      </div>
    </div>
  );
}
