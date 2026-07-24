import type { ReactNode } from "react";

// The WebVTT parsing and transcript-rendering helpers, shared by the Player's
// Transcript card and the public share page's. They started life inside
// Player.tsx; the share page needs the same parse-and-find behaviour but must
// not import a view (and must never reach an authenticated API client), so they
// live here instead. This module is deliberately free of any api/ import: it
// takes VTT text and gives back cues, and the caller decides which URL that
// text came from.

// Cue is one parsed WebVTT row: a start timestamp (whole seconds) plus its
// (tag-stripped) text.
export type Cue = { ts: number; text: string };

// transcriptFilenameBase makes a filesystem-safe download name from the title.
// It takes the title alone — deliberately not the video id, which on peeq IS
// the YouTube id, and not the share token, which is a secret. A recipient's
// Downloads folder must not become a place either one leaks to.
export function transcriptFilenameBase(title: string): string {
  const base = title.replace(/[^\w.-]+/g, "_").slice(0, 80);
  return base || "transcript";
}

// PAREN_SOUND_EVENTS / stripSoundEvents mirror the sound-event stripping in
// backend/internal/subtitles/vtt.go — the backend copy feeds the summary and the
// embeddings, this one draws the transcript panel, and a rule added to one has
// to be added to the other or the two views disagree. Square brackets are
// stripped outright (YouTube uses them only for sound events and speaker
// labels); parentheses only when the inner text is in this closed list, because
// real speech does use parentheses.
const PAREN_SOUND_EVENTS = new Set([
  "music",
  "background music",
  "musique",
  "applause",
  "applauses",
  "cheering",
  "cheers",
  "laughter",
  "laughs",
  "laughing",
  "singing",
  "sings",
  "silence",
  "no audio",
  "inaudible",
  "foreign",
]);

function stripSoundEvents(s: string): string {
  return s
    .replace(/\[[^\]]*\]/g, " ")
    .replace(/[♪♫♬♩]/g, " ")
    .replace(/\([^)]*\)/g, (m) =>
      PAREN_SOUND_EVENTS.has(m.slice(1, -1).trim().toLowerCase()) ? " " : m,
    )
    .replace(/\s+/g, " ")
    .trim();
}

// parseVtt is a small, deliberately forgiving client-side WebVTT parser —
// good enough for yt-dlp/whisper-generated subtitle tracks: it scans for
// "HH:MM:SS.mmm --> HH:MM:SS.mmm" (or "MM:SS.mmm --> ...") timing lines and
// collects every following non-blank line as that cue's text, stripping any
// inline <...> markup tags. It intentionally does not implement the full
// WebVTT spec (cue settings, NOTE blocks, styling) — peeq only needs the
// timestamp + text pairs to render a searchable, click-to-seek transcript.
export function parseVtt(text: string): Cue[] {
  const lines = text.split(/\r?\n/);
  const timingRe =
    /(\d{1,2}:)?\d{2}:\d{2}[.,]\d{3}\s*-->\s*(\d{1,2}:)?\d{2}:\d{2}[.,]\d{3}/;
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
    const cueText = stripSoundEvents(
      textLines
        .join(" ")
        .replace(/<[^>]+>/g, "")
        .trim(),
    );
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

export function matchesFind(text: string, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return false;
  return text.toLowerCase().includes(q);
}

// highlightCue wraps every case-insensitive occurrence of `query` in
// `text` with <mark>, matching the mockup's in-player transcript find.
export function highlightCue(text: string, query: string): ReactNode {
  if (!matchesFind(text, query)) return text;
  const q = query.trim();
  const escaped = q.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const splitRe = new RegExp(`(${escaped})`, "gi");
  const isMatch = new RegExp(`^${escaped}$`, "i");
  return text
    .split(splitRe)
    .map((part, i) =>
      isMatch.test(part) ? <mark key={i}>{part}</mark> : part,
    );
}

// transcriptToText joins parsed cues into the plain-text transcript the ".txt"
// download saves — one cue per line, timestamps dropped.
export function transcriptToText(cues: Cue[]): string {
  return cues.map((c) => c.text).join("\n");
}
