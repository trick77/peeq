// Search snippets arrive with matched terms wrapped in two ASCII control
// characters (see rag.HighlightStart/HighlightEnd in the backend). Markup would
// have been the obvious choice on the wire, but rendering it would mean
// dangerouslySetInnerHTML on text that ultimately comes from a YouTube
// transcript. Control characters cannot occur in subtitle text and let the UI
// build plain text nodes and <mark> elements instead — highlighting with no
// injection surface at all.
export const HIGHLIGHT_START = "\u0002";
export const HIGHLIGHT_END = "\u0003";

// HighlightSegment is one run of snippet text, flagged as matched or not.
export type HighlightSegment = { text: string; match: boolean };

// splitHighlights turns a delimited snippet into segments to render. It is
// tolerant of malformed input on purpose: a snippet is display text, and an
// unbalanced delimiter should degrade to plain text rather than lose the line.
//
//   - an END with no open START is treated as literal (dropped from output)
//   - a START never closed runs to the end of the string
//   - empty runs are omitted, so callers never render an empty <mark>
export function splitHighlights(snippet: string): HighlightSegment[] {
  const out: HighlightSegment[] = [];
  let i = 0;
  let inMatch = false;
  let buf = "";

  const flush = () => {
    if (buf) out.push({ text: buf, match: inMatch });
    buf = "";
  };

  while (i < snippet.length) {
    const ch = snippet[i];
    if (ch === HIGHLIGHT_START && !inMatch) {
      flush();
      inMatch = true;
    } else if (ch === HIGHLIGHT_END && inMatch) {
      flush();
      inMatch = false;
    } else if (ch !== HIGHLIGHT_START && ch !== HIGHLIGHT_END) {
      // A delimiter that cannot open or close anything is stray; drop it so it
      // never reaches the DOM as an invisible control character.
      buf += ch;
    }
    i++;
  }
  flush();
  return out;
}
