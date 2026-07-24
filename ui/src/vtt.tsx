import { useCallback, useEffect, useRef, useState } from "react";
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

// sameLines reports whether two line slices hold the same strings in order.
function sameLines(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((s, i) => s === b[i]);
}

// parseVtt is a small, deliberately forgiving client-side WebVTT parser —
// good enough for yt-dlp/whisper-generated subtitle tracks: it scans for
// "HH:MM:SS.mmm --> HH:MM:SS.mmm" (or "MM:SS.mmm --> ...") timing lines and
// collects every following non-blank line as that cue's text, stripping any
// inline <...> markup tags. It intentionally does not implement the full
// WebVTT spec (cue settings, NOTE blocks, styling) — peeq only needs the
// timestamp + text pairs to render a searchable, click-to-seek transcript.
//
// It also collapses YouTube's rolling-window auto-captions, where each cue
// re-emits the tail of the previous one (so a naive parse shows every line two
// or three times, in the panel and in the ".txt" download alike). The rules
// below mirror ParseVTT's flush() in backend/internal/subtitles/vtt.go — the
// backend copy feeds the summary and the embeddings, this one draws the panel,
// and the two must agree on what the transcript actually says.
export function parseVtt(text: string): Cue[] {
  const lines = text.split(/\r?\n/);
  const timingRe =
    /(\d{1,2}:)?\d{2}:\d{2}[.,]\d{3}\s*-->\s*(\d{1,2}:)?\d{2}:\d{2}[.,]\d{3}/;
  const cues: Cue[] = [];
  // last / lastLines describe the cue emitted (or merged into) most recently:
  // its joined text and its individual lines, which is what the two collapses
  // below compare against.
  let last = "";
  let lastLines: string[] = [];
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
    // Strip tags and sound events per line, before the collapse sees them:
    // YouTube re-emits "[Music] I play" then "[Music] I play games", so only
    // the words that survive stripping can be compared.
    let cueLines: string[] = [];
    while (i < lines.length && lines[i].trim() !== "") {
      const clean = stripSoundEvents(lines[i].replace(/<[^>]+>/g, "").trim());
      if (clean) cueLines.push(clean);
      i++;
    }
    if (cueLines.length === 0) continue;

    // Sliding-window collapse: drop the longest run of leading lines that
    // exactly repeats the trailing lines of the previous cue.
    if (lastLines.length > 0) {
      const maxK = Math.min(cueLines.length, lastLines.length);
      for (let k = maxK; k >= 1; k--) {
        if (
          sameLines(cueLines.slice(0, k), lastLines.slice(lastLines.length - k))
        ) {
          cueLines = cueLines.slice(k);
          break;
        }
      }
    }
    // Every line was a repeat — this cue carries nothing new.
    if (cueLines.length === 0) continue;

    const cueText = cueLines.join(" ").trim();
    if (!cueText) continue;

    // Whole-cue collapse, for a caption that re-grows one line word by word:
    // keep only the longest form, dropping the partial repeats around it.
    if (last !== "" && cueText.startsWith(last)) {
      if (cues.length > 0) cues[cues.length - 1].text = cueText;
      last = cueText;
      lastLines = cueLines;
      continue;
    }
    if (last !== "" && last.startsWith(cueText)) continue;

    cues.push({ ts, text: cueText });
    last = cueText;
    lastLines = cueLines;
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

// COPY_CONFIRM_MS is how long the copy button reads "Copied" before flipping
// back, matching the share-link copy button in components/ShareControl.tsx.
const COPY_CONFIRM_MS = 2000;

// useCopyTranscript backs the transcript "Copy text" button in both the Player
// and the share page. It puts exactly what the ".txt" download contains on the
// clipboard (transcriptToText, so the two can never drift), then confirms by
// flipping `copied` for two seconds — the transcript card sits far below the
// Player's stage toast, and the share page has no toast at all, so the
// confirmation has to live on the button itself.
export function useCopyTranscript(): {
  copied: boolean;
  error: string;
  copy: (cues: Cue[]) => Promise<void>;
} {
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState("");
  const timer = useRef<number | undefined>(undefined);

  useEffect(() => {
    return () => window.clearTimeout(timer.current);
  }, []);

  const copy = useCallback(async (cues: Cue[]) => {
    try {
      await navigator.clipboard.writeText(transcriptToText(cues));
      setError("");
      setCopied(true);
      window.clearTimeout(timer.current);
      timer.current = window.setTimeout(
        () => setCopied(false),
        COPY_CONFIRM_MS,
      );
    } catch {
      // Clipboard writes fail on an insecure origin or a denied permission;
      // the .txt download next to the button is the way out either way.
      setCopied(false);
      setError("Copy failed — download the .txt instead.");
    }
  }, []);

  return { copied, error, copy };
}
