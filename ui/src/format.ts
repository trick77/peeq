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

// The summarize worker runs four stages in order (summarize → classify → embed
// → key points), emitting a live phase for each on the "summary" SSE event.
// SUMMARY_PHASES is the single source of truth mapping each phase string to its
// display label and 1-based step, so the Queue can render "Key points 4/4" with
// a matching progress meter. Anything unrecognised (or absent, before the first
// event) reads as the first stage, "Summarizing".
const SUMMARY_PHASES = [
  { phase: "summarizing", label: "Summarizing" },
  { phase: "classifying", label: "Classifying" },
  { phase: "embedding", label: "Embedding" },
  { phase: "keypoints", label: "Key points" },
] as const;

// SUMMARY_PHASE_COUNT — the "/4" denominator and the number of progress dots.
export const SUMMARY_PHASE_COUNT = SUMMARY_PHASES.length;

// summaryPhaseInfo resolves a live phase to its label and 1-based step.
export function summaryPhaseInfo(phase: string | undefined): {
  label: string;
  step: number;
} {
  const i = SUMMARY_PHASES.findIndex((p) => p.phase === phase);
  const idx = i === -1 ? 0 : i;
  return { label: SUMMARY_PHASES[idx].label, step: idx + 1 };
}

// summaryPhaseLabel is the word-only view of the above, kept for the Activity
// row (which shows the phase without a step meter).
export function summaryPhaseLabel(phase: string | undefined): string {
  return summaryPhaseInfo(phase).label;
}

// parseStamp turns any timestamp the backend sends into a Date.
//
// Two shapes arrive. Date-only ('2026-03-01', from published_at) and true ISO
// ('...Z') both parse as UTC, which is right. But SQLite's datetime('now')
// yields '2026-03-01 09:00:00' — UTC with no zone marker — and JS parses that
// space-separated form as LOCAL time, silently shifting the age by the
// viewer's UTC offset and flipping "today" to "1 day ago" near a boundary.
// Elsewhere the fix is spelled out at each call site (`new Date(x + "Z")` in
// Channel.tsx, `x.replace(" ", "T") + "Z"` in Activity.tsx); doing it here
// means daysSince and both formatters get it for free, whatever they are
// handed.
function parseStamp(iso: string): number {
  const normalized = /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/.test(iso)
    ? iso.replace(" ", "T") + "Z"
    : iso;
  return new Date(normalized).getTime();
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
  const then = parseStamp(iso);
  if (Number.isNaN(then)) return 0;
  const diffMs = now.getTime() - then;
  return Math.max(0, Math.floor(diffMs / (24 * 60 * 60 * 1000)));
}

// formatAgo renders an ISO timestamp as a full-word relative age ("3 days
// ago", "5 months ago"). The primary of the two ages: use it wherever the
// layout has room. Coarse by design — the same day/month/year thresholds as
// the abbreviated formatAge below, just spelled out. Built on daysSince, so
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
  // Cap at 11 months: Math.round(days / 30) reaches 12 by ~345 days, which
  // would read "12 months ago" right before the year bucket takes over.
  if (days < 365) return unit(Math.min(11, Math.round(days / 30)), "month");
  return unit(Math.round(days / 365), "year");
}

// formatAge is formatAgo's abbreviated sibling ("3 d ago", "2 mo ago"), for
// the tight spots: the channel header's stat grid, and the secondary half of a
// card eyebrow that already carries a full-word age. It lived in Channel.tsx
// until the library card started showing two dates at once and needed both
// forms in one line.
//
// Two differences from formatAgo. Unknown input reads "—" rather than "" — a
// caller depends on that (Channel.tsx's stat cell must not collapse). The
// uncapped month bucket is not intentional, just inherited: past ~345 days it
// reads "12 mo ago" where formatAgo caps at 11. Preserved on the move so this
// stayed a relocation; worth aligning separately.
export function formatAge(
  iso: string | undefined,
  now: Date = new Date(),
): string {
  if (!iso) return "—";
  const then = parseStamp(iso);
  if (Number.isNaN(then)) return "—";
  const days = Math.floor((now.getTime() - then) / 86400000);
  if (days <= 0) return "today";
  if (days === 1) return "1 d ago";
  if (days < 30) return `${days} d ago`;
  if (days < 365) return `${Math.round(days / 30)} mo ago`;
  return `${Math.round(days / 365)} y ago`;
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
