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

// ageBucket reduces a whole-day age to the count and unit both formatters
// render. It is the single place the thresholds live: formatAgo and formatAge
// differ only in how they spell the result, and the two drifted apart for
// exactly as long as each carried its own copy of this arithmetic.
//
// Callers handle `days <= 0` before getting here — the two disagree on what
// that means ("today" either way, but only after their own empty-input guard).
//
// The month/year boundary is where the month COUNT would reach 12, not at a
// literal 365 days. Twelve months is a year and must read as one; clamping to
// "11 months ago" instead would be just as wrong the other way, since it says
// eleven for something eleven and a half months old. So once the rounded month
// count hits 12, the age is a year and the year bucket takes it.
function ageBucket(days: number): {
  n: number;
  unit: "day" | "month" | "year";
} {
  if (days < 30) return { n: days, unit: "day" };
  const months = Math.round(days / 30);
  if (months < 12) return { n: months, unit: "month" };
  // max(1, …) because the handover starts at ~345 days, which rounds to 0
  // years — "0 years ago" would be nonsense for something nearly a year old.
  return { n: Math.max(1, Math.round(days / 365)), unit: "year" };
}

// formatAgo renders an ISO timestamp as a full-word relative age ("3 days
// ago", "5 months ago"). The primary of the two ages: use it wherever the
// layout has room. Coarse by design — the same buckets as the abbreviated
// formatAge below, just spelled out. Built on daysSince, so it shares the
// invalid/future -> "today" guard and is testable via `now`.
export function formatAgo(
  iso: string | undefined,
  now: Date = new Date(),
): string {
  if (!iso) return "";
  const days = daysSince(iso, now);
  if (days <= 0) return "today";
  const { n, unit } = ageBucket(days);
  return `${n} ${unit}${n === 1 ? "" : "s"} ago`;
}

// formatAge is formatAgo's abbreviated sibling ("3 d ago", "2 mo ago"), for
// the tight spots: the channel header's stat grid, and the secondary half of a
// card eyebrow that already carries a full-word age. It lived in Channel.tsx
// until the library card started showing two dates at once and needed both
// forms in one line.
//
// One difference from formatAgo survives the shared buckets: unknown input
// reads "—" rather than "", and a caller depends on it — Channel.tsx's stat
// cell must not collapse. That is why this keeps its own NaN guard instead of
// leaning on daysSince, which cannot distinguish "unparseable" from "today".
const AGE_ABBREV = { day: "d", month: "mo", year: "y" } as const;

export function formatAge(
  iso: string | undefined,
  now: Date = new Date(),
): string {
  if (!iso) return "—";
  if (Number.isNaN(parseStamp(iso))) return "—";
  const days = daysSince(iso, now);
  if (days <= 0) return "today";
  const { n, unit } = ageBucket(days);
  return `${n} ${AGE_ABBREV[unit]} ago`;
}

// CODEC_LABELS turns ffprobe's codec_name into what the codec is called in
// public. The wire deliberately carries the raw value, so this mapping is the
// only place the wording lives and can change without a migration.
//
// The aliases are not redundant: which spelling ffprobe reports depends on the
// container it read (an mp4 says "h264" where the stream tag says "avc1"), so
// both have to resolve to the same label.
const CODEC_LABELS: Record<string, string> = {
  h264: "H.264",
  avc1: "H.264",
  hevc: "H.265",
  hev1: "H.265",
  hvc1: "H.265",
  h265: "H.265",
  vp9: "VP9",
  vp09: "VP9",
  vp8: "VP8",
  av1: "AV1",
  av01: "AV1",
  aac: "AAC",
  mp4a: "AAC",
  opus: "Opus",
  vorbis: "Vorbis",
  mp3: "MP3",
  flac: "FLAC",
  ac3: "AC-3",
  eac3: "E-AC-3",
};

// codecLabel names a codec for display. An unmapped codec falls back to its
// own name uppercased rather than to a placeholder: a codec peeq has not seen
// before is still more useful shown than hidden.
export function codecLabel(raw: string | undefined): string {
  if (!raw) return "";
  return CODEC_LABELS[raw.toLowerCase()] ?? raw.toUpperCase();
}

// NAMED_HEIGHTS are the two heights people call by a name rather than a
// number. Matched exactly, not by threshold: a >= 2160 test labels a 4320p
// file "4K", which is precisely the "the strip says something the file isn't"
// problem this strip exists to fix.
const NAMED_HEIGHTS: Record<number, string> = { 2160: "4K", 4320: "8K" };

// resolutionLabel names a pixel height the way people say it. Everything
// outside the two named heights takes the "p" form, including odd ones (a
// 1082px-tall re-encode reads "1082p", which is strange but true, and better
// than rounding it into a lie).
export function resolutionLabel(height: number | undefined): string {
  if (!height || height <= 0) return "";
  return NAMED_HEIGHTS[height] ?? `${height}p`;
}

// formatSize renders a byte count: one decimal for GB, whole units below. A
// 1.4 GB file is worth telling apart from a 1.9 GB one; nobody needs 412.3 MB.
// Moved here from Player.tsx when the stat strip stopped being its only
// caller.
export function formatSize(bytes: number | undefined): string {
  if (!bytes || bytes <= 0) return "";
  if (bytes >= 1024 ** 3) return `${(bytes / 1024 ** 3).toFixed(1)} GB`;
  if (bytes >= 1024 ** 2) return `${(bytes / 1024 ** 2).toFixed(0)} MB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${bytes} B`;
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
