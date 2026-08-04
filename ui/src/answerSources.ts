import { splitCitations, type AnswerPart } from "./citations";
import type { AnswerSource, AnswerVideo } from "./api/answer";
import type { SearchMatch } from "./api/search";

// answerSources — turn a finished answer into the evidence the page shows.
//
// Retrieval hands the model twelve passages and the model uses the ones that
// actually bear on the question. Everything the reader sees is derived from
// THAT subset: the numbered sources, and the moments below them. The twelve are
// a working set, not a claim; showing all of them padded the page with videos
// that never mentioned the topic, and left the answer citing [2] [4] [5] with
// no [1] anywhere in sight.
//
// Both derivations live here so they cannot disagree. If the panel and the
// results list each did their own, the sources list and the moments below it
// could name different videos — which is the bug this file exists to close.

// CitedSource is a source plus the number it is RENDERED as. The answer text
// carries the backend's numbering, since it was streamed before anyone knew
// which sources would be cited; `display` is the first-mention number the
// superscript and the sources row both show.
//
// That number belongs to the VIDEO, not the passage. Retrieval hands the model
// up to three passages from one video, so an answer leaning on a single video
// listed it three times over — same title, three timestamps — and nothing told
// the reader those three were one thing. Two citations of the same video now
// carry the same numeral and collapse to one row; each mark still seeks to its
// own moment.
export type CitedSource = AnswerSource & { display: number };

// citedInOrder returns the sources the answer actually cited, in order of first
// mention, numbered by video from 1.
//
// One entry per cited number, NOT per video: the panel resolves every bracket in
// the text through this list, and groupCited gathers the moments a video was
// cited at. Collapsing here would strand a citation on a missing source and thin
// every card below to a single moment. Deduping to rows is the panel's job.
//
// It reuses splitCitations rather than running its own regex over the text: the
// bracket rules there are subtle — "[note and [1]" yields a citation, a
// trailing "[1" does not — and a second implementation would drift from them.
//
// `known` is built from the FULL source set on purpose. Passing only the cited
// ones would be circular: a number counts as cited BECAUSE splitCitations
// recognised it, and the same set is what keeps a hallucinated [9] rendering as
// literal text instead of pointing nowhere.
//
// The numbering is stable while the answer streams: text only ever grows at the
// end, so first-mention order can gain entries but never reorder them. A
// citation's number never changes under the reader.
export function citedInOrder(
  text: string,
  sources: AnswerSource[],
): CitedSource[] {
  const byNumber = new Map(sources.map((s) => [s.n, s]));
  const known = new Set(byNumber.keys());
  const out: CitedSource[] = [];
  const taken = new Set<number>();
  const numberOf = new Map<string, number>();
  for (const part of splitCitations(text, known)) {
    if (part.kind !== "cite" || taken.has(part.n)) continue;
    taken.add(part.n);
    const source = byNumber.get(part.n)!;
    let display = numberOf.get(source.video_id);
    if (display === undefined) {
      display = numberOf.size + 1;
      numberOf.set(source.video_id, display);
    }
    out.push({ ...source, display });
  }
  return out;
}

// RenderedPart is one piece of the answer body: prose, or a citation already
// resolved to the source whose number it shows.
//
// splitCitations returns the BACKEND number; this carries the source, because
// the backend number must not reach any rendered string — a mark drawn as 2
// whose label says "Source 4" sends a screen-reader user to a row that is not
// there.
export type RenderedPart =
  { kind: "text"; text: string } | { kind: "cite"; source: CitedSource };

// answerParts resolves an answer into the parts its body renders, collapsing a
// run of ADJACENT citations that all show the same numeral down to the first.
//
// Two ways a run arises, and the rule matches on `display` so one covers both:
//
//   - The model repeats one excerpt ("[1][1]"). Both marks are the same passage,
//     so the second says nothing the first did not.
//   - The model cites two passages of ONE video ("[1][2]"). Retrieval hands it
//     up to three per video and never tells it they share a numeral, so it has
//     no way to avoid this. Both marks then drew "1", and "…stages ¹¹" reads as
//     a typo, or as eleven.
//
// Adjacent means nothing between the marks but blank space on ONE line; that
// space goes with the dropped mark so no gap is left hanging. Separated by any
// prose, both marks stay — a video cited at two different claims should carry
// its numeral at each, and there the repetition is the point rather than a
// stutter.
//
// A line break ends a run even though it is whitespace. "[1]\n\n[2]" is two
// paragraphs, the second of which happens to open on its citation, and treating
// it as a stutter would drop a claim's only mark AND swallow the break that
// separated them into one paragraph.
//
// The dropped mark of a "[1][2]" run takes a seek with it, which is the cost
// here: it was a button onto its own moment. That moment stays reachable on the
// video's card below (groupCited keeps one entry per cited passage), and two
// marks a reader cannot tell apart are worse than one mark and a second route.
//
// A mark that the model wrote INSIDE a sentence — "…hardest stages [1]." — is
// moved past the punctuation that follows it — a full stop or a comma — and the
// space before it is dropped: "…hardest stages.¹". See marksAfterPunctuation.
//
// Two more passes run before the collapse, each with its own note below: a
// markdown bullet the model opened a line with is stripped (stripListMarkers),
// and the marks of a run are put in ascending order (sortRunsAscending).
//
// Stable while streaming: text only ever grows at the end, so a run already
// collapsed stays collapsed and no mark appears, disappears, or renumbers under
// the reader. `streaming` is what keeps that true of the move as well — a mark
// with nothing after it yet is held back until the character that decides its
// side of the punctuation has arrived.
export function answerParts(
  rawText: string,
  sources: AnswerSource[],
  streaming = false,
): RenderedPart[] {
  const text = stripListMarkers(rawText, streaming);
  const cited = citedInOrder(text, sources);
  const display = new Map(cited.map((s) => [s.n, s]));
  // The FULL retrieved set, same as citedInOrder uses: that is what keeps a
  // hallucinated [9] rendering as the characters the model produced.
  const known = new Set(sources.map((s) => s.n));

  const out: RenderedPart[] = [];
  // The last citation emitted, and whether only whitespace has passed since it.
  let lastCite: CitedSource | undefined;
  // Whitespace held back mid-run: it belongs to the run if the next mark repeats
  // the numeral, and is emitted untouched if anything else follows.
  let held = "";

  const flushHeld = () => {
    if (held) out.push({ kind: "text", text: held });
    held = "";
  };

  // The move runs BEFORE the collapse, on the raw marks. Punctuation lands in
  // front of a whole run, and the mark a run collapses to is its first — so the
  // two orders place the punctuation identically, and this one also lets the
  // collapse see a run the move itself created ("[1]. [2]" -> "[1] [2]").
  //
  // The ascending sort runs between the two, on the runs the move has finished
  // assembling, and before the collapse can act on adjacency.
  for (const part of sortRunsAscending(
    marksAfterPunctuation(splitCitations(text, known), streaming),
    display,
  )) {
    if (part.kind === "text") {
      if (lastCite && !part.text.trim() && !part.text.includes("\n")) {
        held += part.text;
        continue;
      }
      flushHeld();
      out.push(part);
      lastCite = undefined;
      continue;
    }
    const source = display.get(part.n)!;
    if (lastCite && source.display === lastCite.display) {
      held = "";
      continue;
    }
    flushHeld();
    out.push({ kind: "cite", source });
    lastCite = source;
  }
  flushHeld();
  return out;
}

// Punctuation a mark belongs after rather than before: what ends a sentence, and
// what ends a clause inside one. A comma strands a mark exactly the way a full
// stop does — "the riders ¹, and later" leaves the numeral floating between two
// clauses, belonging to neither.
const TRAILING_PUNCT = /^[.!?,;:]+/;
// Whitespace on ONE line. A newline is a paragraph, not a gap before a mark.
const SAME_LINE_SPACE = /^[^\S\n]*$/;

// marksAfterPunctuation moves a citation past the punctuation it was written in
// front of, so an answer reads "…hardest stages.¹" rather than "…hardest
// stages ¹." The space the model left before the mark goes with the move; the
// space after the punctuation stays, which is what closes the mark up against
// the words it belongs to.
//
// The model is asked to place the mark after the punctuation, but that is a
// request, not a guarantee, and a mark stranded before the stop is the most
// visible thing on the page. Doing it here rather than in splitCitations keeps
// numbering, display numbers and the accessible names provably untouched: this
// reorders tokens and rewrites whitespace, and never invents, drops or renumbers
// a mark.
//
// Two rules keep the move from moving anything under the reader mid-stream:
//
//   - `streaming` holds back a mark with nothing but blank space after it. The
//     character that decides which side of the punctuation it belongs on has not
//     arrived. Rendering it now and shifting it a frame later is the one thing
//     this function must not do, and withholding a trailing marker is already
//     what splitCitations does with an unfinished "[1".
//   - A mark that IS rendered is followed by settled text, so its side of the
//     punctuation is decided once and never revised.
//
// Idempotent: "stages.[1]" has no punctuation after the mark and comes through
// untouched.
function marksAfterPunctuation(
  parts: AnswerPart[],
  streaming: boolean,
): AnswerPart[] {
  const out = parts.slice();
  if (streaming) {
    // Trailing marks — everything after the last text that carries a character.
    let last = out.length - 1;
    while (last >= 0 && (out[last].kind === "cite" || isBlank(out[last])))
      last--;
    out.length = last + 1;
  }

  for (let start = 0; start < out.length; start++) {
    if (out[start].kind !== "cite") continue;
    // The end of the run of marks this one opens: "[1][2]" and "[1] [2]" are one
    // run, and the punctuation goes in front of all of it.
    let end = start;
    for (let j = start + 1; j < out.length; j++) {
      if (out[j].kind === "cite") {
        end = j;
        continue;
      }
      if (isBlank(out[j])) continue;
      break;
    }

    const beforeIndex = start - 1;
    const before = out[beforeIndex];
    const after = out[end + 1];
    start = end;
    // Nothing to move (no punctuation on the far side), or nowhere to move it
    // to (a mark opening the answer): leave the run alone.
    if (after === undefined || after.kind !== "text") continue;
    const stop = TRAILING_PUNCT.exec(after.text);
    if (!stop) continue;
    if (before === undefined || before.kind !== "text") continue;
    // A mark that OPENS a paragraph has nothing in front of it on its own line,
    // and moving the stop back would leave a period orphaned at the end of the
    // paragraph above.
    if (/\n[^\S\n]*$/.test(before.text)) continue;

    out[end + 1] = { kind: "text", text: after.text.slice(stop[0].length) };
    out[beforeIndex] = {
      kind: "text",
      text: before.text.replace(/[^\S\n]+$/, "") + stop[0],
    };
  }
  return out;
}

function isBlank(part: AnswerPart): boolean {
  return part.kind === "text" && SAME_LINE_SPACE.test(part.text);
}

// sortRunsAscending puts the marks of a run in ascending numeral order, so a run
// reads "² ⁴" and never "⁴ ²".
//
// The out-of-order run is not the model misbehaving. `display` numbers the VIDEO
// at its first mention, so a video mentioned early and again later keeps its low
// number — and an answer that cites a new video and then returns to an earlier
// one writes a perfectly sensible "[3][1]" that renders as "2 1". Nothing was
// wrong with the text; the numbering the reader sees is ours, so putting it in
// order is ours too.
//
// Only the marks move. The blank space between them keeps its position, since a
// run's gaps belong to the run's shape rather than to any one mark.
//
// Running before the collapse is what makes the collapse strictly better rather
// than merely undisturbed: "[1][3][2]" where 1 and 2 are one video renders
// "1 2 1" today, sorts to "1 1 2", and collapses to "1 2".
//
// The sort is stable, so two marks showing the same numeral keep their written
// order and the collapse still keeps the FIRST of them — which is the one whose
// moment the surviving button seeks to.
//
// Mid-stream a numeral can still move: a run's last mark has not necessarily
// arrived, so "…[3]" paints "2" and becomes "1 2" when "[1]" follows. That is one
// glyph shifting one place, and the alternative — withholding a whole run until
// the sentence settles — would leave finished claims visibly uncited for as long
// as the model kept writing.
function sortRunsAscending(
  parts: AnswerPart[],
  display: Map<number, CitedSource>,
): AnswerPart[] {
  const out = parts.slice();
  for (let start = 0; start < out.length; start++) {
    if (out[start].kind !== "cite") continue;
    // Where the marks of this run sit. Same adjacency rule as everywhere else:
    // blank space on one line does not end a run, anything else does.
    const at = [start];
    let end = start;
    for (let j = start + 1; j < out.length; j++) {
      if (out[j].kind === "cite") {
        at.push(j);
        end = j;
        continue;
      }
      if (isBlank(out[j])) continue;
      break;
    }
    start = end;
    if (at.length < 2) continue;

    const marks = at.map((i) => out[i]);
    marks.sort((a, b) => numeralOf(a, display) - numeralOf(b, display));
    at.forEach((i, k) => {
      out[i] = marks[k];
    });
  }
  return out;
}

// numeralOf is the number a mark is RENDERED as, which is what the order the
// reader sees is built from — never the backend number the model wrote.
function numeralOf(
  part: AnswerPart,
  display: Map<number, CitedSource>,
): number {
  return part.kind === "cite" ? display.get(part.n)!.display : 0;
}

// A list marker opening a line: the bullet and the space after it. Anchored to a
// line start, and a following space is required, so nothing inside a sentence is
// ever touched.
const LIST_MARKER = /(^|\n)([^\S\n]*)[-*•][^\S\n]+/g;
// The same marker with nothing after it yet — only meaningful mid-stream.
const TRAILING_LIST_MARKER = /(^|\n)[^\S\n]*[-*•][^\S\n]*$/;

// stripListMarkers removes a markdown bullet the model opened a line with.
//
// The answer body renders as plain text and does not set `white-space`, so a
// bulleted list arrives as a paragraph with its markers still in it: the newline
// collapses to a space and the reader sees "…real stars.³ ⁴ - Hypotheses
// proposing…", a hyphen that belongs to nothing. The prompt now asks for prose,
// but a prompt is a request, and this is the second half of that fix.
//
// What it must NOT touch is the reason for both anchors. An em dash is prose and
// a hyphen inside "well-known" is a word; only a bullet at the head of a line,
// with space after it, is a marker. The newline itself stays — it is the
// paragraph break, and marksAfterPunctuation reads it to tell a mark that opens
// a paragraph from one that follows a sentence.
//
// The trailing case exists for the same reason splitCitations withholds an
// unfinished "[1": mid-stream the marker's space has not arrived yet, so matching
// only the completed form would render the hyphen for one frame and swallow it
// the next. Stripping it while it is still the last thing in the buffer means it
// never appears at all.
//
// That trade runs the other way for a line opening on a negative number: "\n-"
// is withheld, and the hyphen appears once "40" settles it. Rare enough to be
// the better side of the trade — every bulleted answer hits the first case.
//
// A NUMBERED marker ("1. ") is deliberately left alone. Stripping it would mean
// deleting digits and a full stop on the strength of a guess, and a sentence
// opening on a year — "2019. That season…" — is prose the reader wrote nothing
// to lose. The prompt asking for prose is what covers that shape.
export function stripListMarkers(text: string, streaming = false): string {
  const out = text.replace(LIST_MARKER, "$1$2");
  return streaming ? out.replace(TRAILING_LIST_MARKER, "$1") : out;
}

// CitedResult is one video and the cited moments within it, in the shape the
// result cards render.
export type CitedResult = { video: AnswerVideo; matches: SearchMatch[] };

// groupCited gathers cited sources under the video each came from.
//
// Moments stay in CITATION order within a card rather than being sorted by
// timestamp, so the numbers ascend down the page in step with the sources list.
// A source whose video is missing from the frame is dropped rather than
// rendered against a placeholder card.
export function groupCited(
  cited: CitedSource[],
  videos: AnswerVideo[],
): CitedResult[] {
  const byId = new Map(videos.map((v) => [v.id, v]));
  const order: string[] = [];
  const groups = new Map<string, CitedResult>();
  for (const s of cited) {
    const video = byId.get(s.video_id);
    if (!video) continue;
    let g = groups.get(s.video_id);
    if (!g) {
      g = { video, matches: [] };
      groups.set(s.video_id, g);
      order.push(s.video_id);
    }
    g.matches.push({
      start_seconds: s.start_seconds,
      snippet: s.snippet,
      // Retrieval diagnostics the card does not render; the moments here were
      // chosen by the answer, not by a distance.
      distance: 0,
      kind: s.kind,
    });
  }
  return order.map((id) => groups.get(id)!);
}
