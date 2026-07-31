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
export type CitedSource = AnswerSource & { display: number };

// citedInOrder returns the sources the answer actually cited, in order of first
// mention, renumbered from 1.
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
  for (const part of splitCitations(text, known)) {
    if (part.kind !== "cite" || taken.has(part.n)) continue;
    taken.add(part.n);
    out.push({ ...byNumber.get(part.n)!, display: out.length + 1 });
  }
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
