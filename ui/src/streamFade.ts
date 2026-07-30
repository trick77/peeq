// streamFade — split streamed answer text into the segments that fade in.
//
// Ported from ../loom's ui/src/chat/streamFade.ts. Loom does this as a rehype
// plugin because its answers are markdown; peeq's are plain prose with [n]
// citation markers, so the same segmentation applies directly with no plugin.
//
// The effect itself is CSS (.ans-seg in index.css): each segment animates its
// COLOR from a dimmed version of the ink to full ink. Deliberately not opacity
// — fading opacity on text lets the page background show through the glyphs,
// which reads as washed-out rather than as arriving.
//
// The whole accumulated answer is re-split on every render rather than only the
// newest fragment. That is what keeps existing segments byte-identical between
// renders, so React reconciles them to the same elements and only genuinely new
// ones start their animation. Segmenting per arriving delta instead would
// re-wrap earlier text as boundaries shifted, and the entire answer would
// flicker on every token.

// MAX_SEG_CHARS bounds a segment. Coarser than per-word on purpose: word-level
// segments make the fade read as a stutter, and produce far more DOM nodes than
// the effect needs.
const MAX_SEG_CHARS = 28;

// splitIntoSegments groups words into clause-sized runs, cutting at sentence
// and clause punctuation or once a run is long enough. Trailing whitespace
// stays attached to its word so joining the segments reproduces the input
// exactly.
export function splitIntoSegments(value: string): string[] {
  const tokens = value.match(/\S+\s*|\s+/g);
  if (!tokens) return value ? [value] : [];

  const segments: string[] = [];
  let current = "";
  for (const tok of tokens) {
    current += tok;
    const endsClause = /[.!?,;:—)\]]$/.test(tok.trimEnd());
    if (endsClause || current.length >= MAX_SEG_CHARS) {
      segments.push(current);
      current = "";
    }
  }
  if (current !== "") segments.push(current);
  return segments;
}
