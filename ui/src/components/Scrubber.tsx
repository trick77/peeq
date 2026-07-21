import type { MouseEvent } from "react";
import { formatDuration } from "../format";

export type SponsorblockSegment = {
  category: string;
  start_time: number;
  end_time: number;
};

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
          return (
            <div
              key={`${seg.category}-${i}`}
              className="sb"
              style={{ left: `${left}%`, width: `${width}%` }}
              title={`SponsorBlock: ${seg.category}`}
            />
          );
        })}
        <div className="played" style={{ width: `${playedPercent}%` }} />
      </div>
      <div className="scrub-times">
        <span className="mono">{formatDuration(currentSeconds)}</span>
        {segments.length > 0 ? (
          <span className="sb-legend">
            <i /> SponsorBlock — auto-skipped
          </span>
        ) : (
          <span />
        )}
        <span className="mono">{formatDuration(duration)}</span>
      </div>
    </div>
  );
}
