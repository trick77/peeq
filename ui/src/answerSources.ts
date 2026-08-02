import { splitCitations } from "./citations";
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
// Adjacent means nothing between the marks but whitespace; that whitespace goes
// with the dropped mark so no gap is left hanging. Separated by any prose, both
// marks stay — a video cited at two different claims should carry its numeral at
// each, and there the repetition is the point rather than a stutter.
//
// The dropped mark of a "[1][2]" run takes a seek with it, which is the cost
// here: it was a button onto its own moment. That moment stays reachable on the
// video's card below (groupCited keeps one entry per cited passage), and two
// marks a reader cannot tell apart are worse than one mark and a second route.
//
// Stable while streaming: text only ever grows at the end, so a run already
// collapsed stays collapsed and no mark appears, disappears, or renumbers under
// the reader.
export function answerParts(
  text: string,
  sources: AnswerSource[],
): RenderedPart[] {
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

  for (const part of splitCitations(text, known)) {
    if (part.kind === "text") {
      if (lastCite && !part.text.trim()) {
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
