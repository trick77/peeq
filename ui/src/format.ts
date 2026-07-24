// format.ts — small display-formatting helpers shared by VideoCard and
// Player (Task 14). Kept framework-free so they're trivially unit-testable
// alongside the components that use them.

// formatDuration renders whole seconds as mono "M:SS" (or "H:MM:SS" past an
// hour), matching the mockup's `.dur`/`.scrub-times` badges.
export function formatDuration(totalSeconds: number | undefined): string {
  if (
    totalSeconds === undefined ||
    !Number.isFinite(totalSeconds) ||
    totalSeconds < 0
  ) {
    return "--:--";
  }
  const s = Math.floor(totalSeconds);
  const hours = Math.floor(s / 3600);
  const minutes = Math.floor((s % 3600) / 60);
  const seconds = s % 60;
  const pad = (n: number) => String(n).padStart(2, "0");
  if (hours > 0) {
    return `${hours}:${pad(minutes)}:${pad(seconds)}`;
  }
  return `${minutes}:${pad(seconds)}`;
}

// daysBetween returns the whole number of days elapsed from `from` (an ISO
// timestamp) to now. Negative/invalid input yields 0 rather than NaN, so a
// caller doing retention arithmetic on it never produces "Expires in NaN
// days".
export function daysSince(
  iso: string | undefined,
  now: Date = new Date(),
): number {
  if (!iso) return 0;
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return 0;
  const diffMs = now.getTime() - then;
  return Math.max(0, Math.floor(diffMs / (24 * 60 * 60 * 1000)));
}

// formatAgo renders an ISO timestamp as a full-word relative age ("3 days
// ago", "5 months ago") for the video-card date line. Coarse by design — the
// same day/month/year thresholds the channel page's abbreviated formatAge
// uses, just spelled out, since the card has the room. Built on daysSince, so
// it shares the invalid/future -> "today" guard and is testable via `now`.
export function formatAgo(
  iso: string | undefined,
  now: Date = new Date(),
): string {
  if (!iso) return "";
  const days = daysSince(iso, now);
  if (days <= 0) return "today";
  const unit = (n: number, word: string) =>
    `${n} ${word}${n === 1 ? "" : "s"} ago`;
  if (days < 30) return unit(days, "day");
  if (days < 365) return unit(Math.round(days / 30), "month");
  return unit(Math.round(days / 365), "year");
}

// GRADIENT_CLASSES mirrors the mockup's six thumbnail gradient fallbacks
// (g1..g6, defined in index.css). gradientClassFor picks a stable one per
// video id (a simple string hash) so a given card keeps the same color
// across re-renders instead of flickering between them.
const GRADIENT_CLASSES = ["g1", "g2", "g3", "g4", "g5", "g6"];

export function gradientClassFor(id: string): string {
  let hash = 0;
  for (let i = 0; i < id.length; i++) {
    hash = (hash * 31 + id.charCodeAt(i)) | 0;
  }
  const index = Math.abs(hash) % GRADIENT_CLASSES.length;
  return GRADIENT_CLASSES[index];
}
