// emphasis — render the inline markdown the answer model leaks, rather than
// showing its delimiters.
//
// The answer body is plain prose and the system prompt asks for exactly that
// ("no bullet lists, no headings, no markdown formatting of any kind"). A prompt
// is a request: the model still bolds a video title now and then, and the reader
// saw the asterisks — `The video **"Why Agentic Systems Need Ontologies"**
// discusses…`. stripListMarkers is the same second half of the same prompt, for
// bullets; this is it for emphasis.
//
// Parsed into React nodes, never dangerouslySetInnerHTML — see highlight.ts for
// why that line exists and why no markdown library is added to do this.
//
// Two rules run through everything below, and both come from the fact that this
// parses text that is still arriving:
//
//   - THE DELIMITERS ARE NEVER RENDERED, in any state. A "[9]" with no matching
//     source falls back to literal because square brackets are ordinary prose;
//     an asterisk that opened a bold span is not, so an unclosed "**foo" at the
//     end of a settled answer renders "foo" with the asterisks simply dropped.
//     Putting them on screen at settle would be the flash that withholding an
//     unfinished "[1" exists to prevent, arriving from the other direction.
//   - A MARK OPENS WHERE ITS OPENER IS, not where its closer lands, for "**",
//     "__" and "`". The <strong> element is opened by the first delta and the
//     rest of the span accumulates inside it, so the segments React already
//     rendered keep their element identity and none of them re-runs its fade
//     when the closer finally arrives. Waiting for the closer would move the
//     whole span into a new element and re-animate every word of it.
//
// Single "*" and "_" are the exception to the second rule: they need the closer
// before they mean anything, because a lone asterisk and a snake_case
// identifier are real. While streaming, an opener still looking for its closer
// withholds the text after it — the same trade splitCitations makes. The
// opener test is what keeps that from stalling on ordinary prose: "5 * 3" is
// followed by a space, so it is never an opener at all.
//
// Flat by design: no nesting, first closer wins. Prose emphasis does not nest
// and "***x***" is not worth a grammar. A newline closes whatever is open, so
// one stray "**" cannot bold the rest of the answer.

export type EmphasisMark = "strong" | "em" | "code" | "heading";

// The text half of a rendered part. Declared structurally rather than imported
// so this module stays free of answerSources — which imports it.
export type MarkedText = { kind: "text"; text: string; mark?: EmphasisMark };

// A heading marker opening a line, and the same marker with its space not yet
// arrived. Up to three leading spaces, as in markdown.
const HEADING = /^[ \t]{0,3}#{1,6}[ \t]+/;
const PARTIAL_HEADING = /^[ \t]{0,3}#{1,6}[ \t]*$/;

// applyEmphasis rewrites the text parts of a rendered answer, dropping markdown
// delimiters and flagging what they enclosed. Citation parts pass through
// untouched — numbering, ordering and display numbers are decided before this
// runs and must not depend on it.
//
// It works on the PARTS rather than on the raw answer string, so a span may
// cross a citation ("**Ontologies [1] explained**") and still be one span: the
// scan sees the text of every part as one stream and the citations sit at
// zero-width positions inside it.
export function applyEmphasis<C extends { kind: "cite" }>(
  parts: (MarkedText | C)[],
  streaming: boolean,
): (MarkedText | C)[] {
  const texts = parts.filter((p): p is MarkedText => p.kind === "text");
  if (!texts.length) return parts;

  const flat = texts.map((p) => p.text).join("");
  const { mark, drop, cut } = scan(flat, streaming);

  // Whether the scan is holding text back at all. Only then does anything past
  // the cut get withheld — `cut === flat.length` is the ordinary case and must
  // leave every part alone, including an answer that ends on its citation.
  const withholding = cut < flat.length;

  const out: (MarkedText | C)[] = [];
  let at = 0;
  for (const part of parts) {
    // A citation sitting past the cut is withheld with the prose around it. It
    // is zero-width, so without this it would render on ITS OWN, a superscript
    // in front of the sentence it annotates, and then jump once the delimiter
    // that stalled the text resolves. The sources list and the video cards
    // below are unaffected: they come from citedInOrder on the raw text, not
    // from these parts.
    if (withholding && at >= cut) break;
    if (part.kind !== "text") {
      out.push(part);
      continue;
    }
    const text = (part as MarkedText).text;
    // Split this part's characters into runs of one mark, leaving the
    // delimiters out. A part usually yields exactly one run; it yields more
    // when a span opened or closed inside it.
    let buf = "";
    let current: EmphasisMark | undefined;
    const flush = () => {
      if (buf) out.push({ kind: "text", text: buf, mark: current });
      buf = "";
    };
    for (let k = 0; k < text.length; k++) {
      const i = at + k;
      if (i >= cut) break;
      if (drop[i]) continue;
      if (buf && mark[i] !== current) flush();
      current = mark[i];
      buf += text[k];
    }
    flush();
    at += text.length;
  }
  return out;
}

type Scan = {
  // The mark each character is rendered under, by index into the flat text.
  mark: (EmphasisMark | undefined)[];
  // Whether each character is a delimiter, and so never rendered.
  drop: boolean[];
  // How much of the text is settled enough to render. Below the length only
  // mid-stream, where a delimiter that has not yet decided what it is takes
  // everything after it with it.
  cut: number;
};

function scan(s: string, streaming: boolean): Scan {
  const mark: (EmphasisMark | undefined)[] = new Array(s.length);
  const drop: boolean[] = new Array(s.length).fill(false);
  let cut = s.length;

  // The inline span currently open, the exact characters that close it, and —
  // for the kinds that had to find their closer before opening — the index it
  // closes at. Optimistic kinds carry -1 and close at the first occurrence.
  let inline: Exclude<EmphasisMark, "heading"> | undefined;
  let delim = "";
  let closeAt = -1;
  // Whether the line being scanned opened with a heading marker. Tracked apart
  // from `inline` because a heading is the whole line and outranks anything
  // inside it: "## **Ontologies**" is one heading, not a bold inside one.
  let heading = false;

  let i = 0;
  while (i < s.length) {
    // A newline ends both. Emphasis does not cross a paragraph in markdown, and
    // bounding it here is what keeps a single stray "**" from bolding every
    // word that follows it to the end of the answer.
    if (s[i] === "\n") {
      inline = undefined;
      heading = false;
      mark[i] = undefined;
      i++;
      continue;
    }

    if (inline) {
      // An optimistic span closes on the first occurrence of its delimiter; one
      // that had to find its closer before opening closes only there.
      if (s.startsWith(delim, i) && (closeAt === -1 || i === closeAt)) {
        for (let k = 0; k < delim.length; k++) drop[i + k] = true;
        i += delim.length;
        inline = undefined;
        continue;
      }
      mark[i] = heading ? "heading" : inline;
      i++;
      continue;
    }

    if (i === 0 || s[i - 1] === "\n") {
      const rest = s.slice(i);
      const h = HEADING.exec(rest);
      if (h) {
        for (let k = 0; k < h[0].length; k++) drop[i + k] = true;
        heading = true;
        i += h[0].length;
        continue;
      }
      // Mid-stream the marker's space has not arrived yet. Rendering the hash
      // now and swallowing it a frame later is the flash this file exists to
      // avoid, so hold the line back until it settles.
      if (streaming && PARTIAL_HEADING.test(rest)) {
        cut = i;
        break;
      }
    }

    // A lone "*" or "_" as the LAST character has not decided what it is yet:
    // the next delta may make it "**", or the first half of an em span. Neither
    // opens on one character, so the branches below would fall through and put
    // the delimiter itself on screen for a frame — the same flash the partial
    // heading above is held back for. Only at a boundary, so "snake_" keeps
    // rendering mid-word.
    if (
      streaming &&
      i === s.length - 1 &&
      (s[i] === "*" || s[i] === "_") &&
      boundaryBefore(s, i)
    ) {
      cut = i;
      break;
    }

    const two = s.slice(i, i + 2);
    if ((two === "**" || two === "__") && opensStrong(s, i, two)) {
      drop[i] = true;
      drop[i + 1] = true;
      inline = "strong";
      delim = two;
      closeAt = -1;
      i += 2;
      continue;
    }
    if (s[i] === "`") {
      drop[i] = true;
      inline = "code";
      delim = "`";
      closeAt = -1;
      i++;
      continue;
    }
    if ((s[i] === "*" || s[i] === "_") && opensEm(s, i)) {
      const close = findEmCloser(s, i);
      if (close !== -1) {
        drop[i] = true;
        inline = "em";
        delim = s[i];
        closeAt = close;
        i++;
        continue;
      }
      // No closer yet. Mid-stream that means it may still arrive, and rendering
      // the character now would put an asterisk on screen that a later frame
      // takes away. Settled, it was never a delimiter — it is prose.
      if (streaming) {
        cut = i;
        break;
      }
    }

    mark[i] = heading ? "heading" : undefined;
    i++;
  }

  return { mark, drop, cut };
}

// A character that can sit before an opening delimiter: the start of the text,
// space, or the punctuation a quoted or bracketed phrase opens with. This is
// what keeps "snake_case" and "__init__" from opening anything mid-word.
function boundaryBefore(s: string, i: number): boolean {
  return i === 0 || /[\s("'“‘[]/.test(s[i - 1]);
}

function opensStrong(s: string, i: number, two: string): boolean {
  if (two === "__" && !boundaryBefore(s, i)) return false;
  const next = s[i + 2];
  // Nothing after it yet is fine — mid-stream that is a span whose first word
  // has not arrived, and there is nothing to render either way. A SPACE after
  // it is not: "** " opens nothing in markdown.
  return next === undefined || !/\s/.test(next);
}

function opensEm(s: string, i: number): boolean {
  if (!boundaryBefore(s, i)) return false;
  const next = s[i + 1];
  // A lone asterisk in prose — "5 * 3" — is followed by a space and never
  // becomes an opener. That test is what keeps the streaming withhold below
  // from stalling on text that was never emphasis.
  return next !== undefined && !/\s/.test(next) && next !== s[i];
}

// findEmCloser returns the index of the delimiter that closes a single "*" or
// "_" opened at `open`, or -1 while the text holds no such character. A closer
// hugs the word it ends and does not run past the paragraph.
function findEmCloser(s: string, open: number): number {
  const ch = s[open];
  for (let i = open + 1; i < s.length; i++) {
    if (s[i] === "\n") return -1;
    if (s[i] !== ch) continue;
    if (/\s/.test(s[i - 1])) continue;
    const next = s[i + 1];
    if (next === undefined) return i;
    if (ch === "_" && /[\w]/.test(next)) continue;
    if (!/\s/.test(next) && !/[.,!?;:)\]"'”’—]/.test(next)) continue;
    return i;
  }
  return -1;
}
