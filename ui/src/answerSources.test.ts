import { describe, it, expect } from "vitest";
import {
  answerParts,
  citedInOrder,
  groupCited,
  stripListMarkers,
} from "./answerSources";
import type { AnswerSource, AnswerVideo } from "./api/answer";

function src(n: number, over: Partial<AnswerSource> = {}): AnswerSource {
  return {
    n,
    video_id: `v${n}`,
    title: `Video ${n}`,
    channel_name: "ch",
    start_seconds: n * 10,
    kind: "transcript",
    snippet: `snippet ${n}`,
    ...over,
  };
}

function vid(id: string): AnswerVideo {
  return {
    id,
    title: `Video ${id}`,
    channel_id: "c",
    channel_name: "ch",
    duration_seconds: 600,
    has_thumbnail: true,
    status: "downloaded",
  };
}

describe("citedInOrder", () => {
  // The reported bug: the model cited [2] [4] [5] and the page showed twelve
  // sources numbered 1..12, so the answer read as though it were skipping
  // evidence the reader could see.
  it("renumbers from 1 in order of first mention", () => {
    const got = citedInOrder("A[4] then B[2].", [src(1), src(2), src(4)]);
    expect(got.map((s) => [s.n, s.display])).toEqual([
      [4, 1],
      [2, 2],
    ]);
  });

  it("gives a repeated citation one number", () => {
    const got = citedInOrder("A[2], and again[2], then[1].", [src(1), src(2)]);
    expect(got.map((s) => s.n)).toEqual([2, 1]);
    expect(got.map((s) => s.display)).toEqual([1, 2]);
  });

  // The number names the video, so a second passage from a video already cited
  // reuses its number instead of claiming the next one — and the video after it
  // gets 2, not 3.
  it("numbers by video, not by passage", () => {
    const got = citedInOrder("A[1] B[2] C[3]", [
      src(1, { video_id: "va" }),
      src(2, { video_id: "va" }),
      src(3, { video_id: "vb" }),
    ]);
    expect(got.map((s) => [s.n, s.display])).toEqual([
      [1, 1],
      [2, 1],
      [3, 2],
    ]);
  });

  it("leaves out a passage the answer never cited", () => {
    const got = citedInOrder("Only this[1].", [src(1), src(2), src(3)]);
    expect(got).toHaveLength(1);
    expect(got[0].video_id).toBe("v1");
  });

  // A number with no source behind it is text the model produced, not a
  // citation — so it claims no display number and shifts nothing.
  it("ignores a citation to a source that does not exist", () => {
    const got = citedInOrder("Real[1] and invented[9].", [src(1), src(2)]);
    expect(got.map((s) => [s.n, s.display])).toEqual([[1, 1]]);
  });

  // Numbering has to be stable as text arrives: a number that changed under the
  // reader mid-stream would be worse than no number at all.
  it("keeps earlier numbers fixed as the answer grows", () => {
    const sources = [src(1), src(2), src(3)];
    const first = citedInOrder("A[3]", sources);
    const later = citedInOrder("A[3] then B[1] then C[2]", sources);
    expect(first[0].display).toBe(1);
    expect(later[0]).toEqual(first[0]);
    expect(later.map((s) => s.n)).toEqual([3, 1, 2]);
  });

  it("returns nothing for an answer that cites nothing", () => {
    expect(citedInOrder("I couldn't tell.", [src(1)])).toEqual([]);
  });
});

describe("answerParts", () => {
  // The reported bug: an answer read "…one of its hardest stages ¹¹". Sources 1
  // and 2 were two moments of one video, so both marks drew the same numeral and
  // the pair read as a typo, or as eleven.
  const oneVideo = [
    src(1, { video_id: "va", start_seconds: 70 }),
    src(2, { video_id: "va", start_seconds: 640 }),
  ];

  function shape(text: string, sources: AnswerSource[], streaming = false) {
    return answerParts(text, sources, streaming).map((p) =>
      p.kind === "text" ? p.text : `[${p.source.display}]`,
    );
  }

  it("collapses two passages of one video cited back to back", () => {
    const got = answerParts("Hardest stages[1][2].", oneVideo);
    expect(shape("Hardest stages[1][2].", oneVideo)).toEqual([
      "Hardest stages.",
      "[1]",
    ]);
    // The mark that survives is the FIRST of the run, so it seeks where the
    // reader's eye was told to look.
    const mark = got.find((p) => p.kind === "cite")!;
    expect(mark.kind === "cite" && mark.source.n).toBe(1);
    expect(mark.kind === "cite" && mark.source.start_seconds).toBe(70);
  });

  // Same run, written with a space. The space belongs to the dropped mark and
  // goes with it rather than being left hanging before the full stop.
  it("collapses across whitespace without leaving a gap", () => {
    expect(shape("Hardest stages[1] [2].", oneVideo)).toEqual([
      "Hardest stages.",
      "[1]",
    ]);
  });

  // The model repeating one excerpt. Simpler than the two-passage case — both
  // marks were always the same passage — and it collapsed for the same reason.
  it("collapses a repeated citation", () => {
    expect(shape("As noted[1][1].", [src(1)])).toEqual(["As noted.", "[1]"]);
  });

  // Two videos. The numerals differ, so there is nothing to stutter and both
  // marks are evidence the reader can act on.
  it("keeps adjacent marks that show different numerals", () => {
    const got = shape("The riders[1][2].", [
      src(1, { video_id: "va" }),
      src(2, { video_id: "vb" }),
    ]);
    expect(got).toEqual(["The riders.", "[1]", "[2]"]);
  });

  // One video cited at two different claims. The numeral repeats because each
  // claim is separately sourced, which is the point rather than a stutter.
  it("keeps repeated numerals that prose separates", () => {
    const text = "Early on[1], and later[2].";
    // Both marks show 1 — it is one video — and that is exactly what a reader
    // needs at each claim. Only side by side does it read as a stutter.
    expect(shape(text, oneVideo)).toEqual([
      "Early on,",
      "[1]",
      " and later.",
      "[1]",
    ]);
    // Still two seeks, to two different moments.
    const marks = answerParts(text, oneVideo).filter((p) => p.kind === "cite");
    expect(
      marks.map((m) => (m.kind === "cite" ? m.source.start_seconds : -1)),
    ).toEqual([70, 640]);
  });

  // The reported bug: a run rendered "2 1". `display` numbers the video at its
  // first mention, so an answer that cites a new video and then returns to an
  // earlier one writes a sensible "[3][1]" whose numerals come out descending.
  it("puts a descending run in ascending order", () => {
    const sources = [src(1, { video_id: "va" }), src(3, { video_id: "vb" })];
    expect(shape("Alpha[1]. Beta[3][1].", sources)).toEqual([
      "Alpha.",
      "[1]",
      " Beta.",
      "[1]",
      "[2]",
    ]);
  });

  // Sorting makes the collapse strictly better rather than merely undisturbed:
  // 1 and 2 are one video, so "1 2 1" becomes "1 1 2" and then "1 2".
  it("lets the sort feed the collapse", () => {
    const sources = [
      src(1, { video_id: "va", start_seconds: 70 }),
      src(2, { video_id: "va", start_seconds: 640 }),
      src(3, { video_id: "vb" }),
    ];
    expect(shape("Both[1][3][2].", sources)).toEqual(["Both.", "[1]", "[2]"]);
  });

  // The sort is stable, so the mark that survives a collapsed pair is still the
  // one the reader's eye was sent to.
  it("keeps the first of two marks showing one numeral", () => {
    const got = answerParts("Both[1][3][2].", [
      src(1, { video_id: "va", start_seconds: 70 }),
      src(2, { video_id: "va", start_seconds: 640 }),
      src(3, { video_id: "vb" }),
    ]);
    const first = got.find((p) => p.kind === "cite")!;
    expect(first.kind === "cite" && first.source.start_seconds).toBe(70);
  });

  // Prose between the marks means there is no run to sort. Each claim keeps the
  // numeral it was written with, in the order it was written.
  it("does not reorder marks that prose separates", () => {
    const sources = [src(1, { video_id: "va" }), src(3, { video_id: "vb" })];
    expect(shape("Beta[3] and then alpha[1].", sources)).toEqual([
      "Beta",
      "[1]",
      " and then alpha.",
      "[2]",
    ]);
  });

  // A line break is whitespace, but it is not a stutter. Collapsing here would
  // leave the second paragraph's only claim unsourced and join the two
  // paragraphs into one on the way.
  it("does not collapse across a line break", () => {
    expect(shape("Hardest stages[1]\n\n[2] and more.", oneVideo)).toEqual([
      "Hardest stages",
      "[1]",
      "\n\n",
      "[1]",
      " and more.",
    ]);
  });

  // The same break with prose after it was never at risk — the prose ends the
  // run on its own — but it is the shape a real answer takes, so it is pinned.
  it("keeps both marks when a paragraph of prose separates them", () => {
    expect(
      shape("Hardest stages[1]\n\nThe hosts argued[2].", oneVideo),
    ).toEqual(["Hardest stages", "[1]", "\n\nThe hosts argued.", "[1]"]);
  });

  // A hallucinated number is text the model produced, and stays that way — it
  // must not be swallowed as part of a run.
  it("leaves an unknown marker as literal text", () => {
    expect(shape("Real[1] invented[9].", [src(1)])).toEqual([
      "Real",
      "[1]",
      " invented[9].",
    ]);
  });

  // Text only ever grows at the end, so a collapsed run stays collapsed and no
  // mark appears or disappears under the reader.
  it("stays stable as the answer streams in", () => {
    const first = shape("Hardest stages[1][2]", oneVideo);
    expect(first).toEqual(["Hardest stages", "[1]"]);
    expect(shape("Hardest stages[1][2]. Then more[2].", oneVideo)).toEqual([
      "Hardest stages.",
      "[1]",
      " Then more.",
      "[1]",
    ]);
  });

  // The reported bug: the model writes the mark in front of the full stop, so
  // the answer read "…hardest stages ¹." The mark moves past the stop and the
  // space before it goes with the move.
  it("moves a mark past the full stop it was written before", () => {
    expect(shape("Hardest stages [1]. Then more.", oneVideo)).toEqual([
      "Hardest stages.",
      "[1]",
      " Then more.",
    ]);
  });

  it("moves a mark past a question mark and an exclamation", () => {
    expect(shape("Was it hard[1]? Yes[1]!", [src(1)])).toEqual([
      "Was it hard?",
      "[1]",
      " Yes!",
      "[1]",
    ]);
  });

  // A mark already in the right place is left exactly as it is.
  it("leaves a mark that already follows the full stop", () => {
    expect(shape("Hardest stages.[1] Then more.", oneVideo)).toEqual([
      "Hardest stages.",
      "[1]",
      " Then more.",
    ]);
  });

  // A comma strands a mark the same way a full stop does: "Early on ¹, and
  // later" leaves the numeral between two clauses, belonging to neither.
  it("moves a mark past a comma", () => {
    expect(shape("Early on [1], and later.", oneVideo)).toEqual([
      "Early on,",
      "[1]",
      " and later.",
    ]);
  });

  it("moves a mark past a semicolon and a colon", () => {
    expect(shape("First [1]; second [1]: third.", [src(1)])).toEqual([
      "First;",
      "[1]",
      " second:",
      "[1]",
      " third.",
    ]);
  });

  // The whole run moves together, and what is left is the run's first mark.
  it("moves a run of marks past the stop as one", () => {
    expect(shape("Hardest stages [1][2].", oneVideo)).toEqual([
      "Hardest stages.",
      "[1]",
    ]);
  });

  // The move can put two marks of one video side by side that the full stop had
  // separated. That is the stutter the collapse exists to prevent, and it runs
  // after the move for exactly this case.
  it("collapses a run the move itself created", () => {
    expect(shape("Hardest stages [1]. [2] And more.", oneVideo)).toEqual([
      "Hardest stages.",
      "[1]",
      " And more.",
    ]);
  });

  // A mark opening a paragraph has nothing in front of it on its own line, so
  // the stop stays where the model put it rather than being orphaned at the end
  // of the paragraph above.
  it("does not move a stop back across a line break", () => {
    expect(shape("Hardest stages[1]\n\n[2]. And more.", oneVideo)).toEqual([
      "Hardest stages",
      "[1]",
      "\n\n",
      "[1]",
      ". And more.",
    ]);
  });

  // A hallucinated number is literal text, so the stop after it is nothing to
  // move — the sentence keeps the characters the model produced.
  it("does not move a stop past an unknown marker", () => {
    expect(shape("Real[1] but invented [9].", [src(1)])).toEqual([
      "Real",
      "[1]",
      " but invented [9].",
    ]);
  });

  // While the answer streams, a mark with nothing after it is held back: the
  // character that decides its side of the stop has not arrived. Rendering it
  // and shifting it a frame later is the motion this avoids.
  it("holds back a trailing mark while streaming, then places it", () => {
    const streamed = (text: string) => shape(text, oneVideo, true);
    expect(streamed("Hardest stages [1]")).toEqual(["Hardest stages "]);
    expect(streamed("Hardest stages [1].")).toEqual(["Hardest stages.", "[1]"]);
    expect(streamed("Hardest stages [1]. Then")).toEqual([
      "Hardest stages.",
      "[1]",
      " Then",
    ]);
  });

  // The collapse is a rendering decision about the body and nothing else: the
  // dropped mark's moment is still a row on the video's card below.
  it("does not thin the moments a collapsed run contributed", () => {
    const cited = citedInOrder("Hardest stages[1][2].", oneVideo);
    expect(cited).toHaveLength(2);
    const cards = groupCited(cited, [vid("va")]);
    expect(cards[0].matches.map((m) => m.start_seconds)).toEqual([70, 640]);
  });
});

describe("stripListMarkers", () => {
  // The reported bug: the answer body is plain text and sets no `white-space`,
  // so a bulleted list arrives as one paragraph and the reader sees
  // "…real stars. 3 4 - Hypotheses proposing…" — a hyphen belonging to nothing.
  it("removes a bullet the model opened a line with", () => {
    expect(stripListMarkers("Real stars.[3][4]\n- Hypotheses propose X.")).toBe(
      "Real stars.[3][4]\nHypotheses propose X.",
    );
  });

  it("removes an asterisk and a bullet character too", () => {
    expect(stripListMarkers("First.\n* Second.\n• Third.")).toBe(
      "First.\nSecond.\nThird.",
    );
  });

  it("removes a marker opening the answer", () => {
    expect(stripListMarkers("- Only point.")).toBe("Only point.");
  });

  // Indented markers are the same marker. The indent stays, since it is
  // whitespace the paragraph was written with.
  it("keeps the indent and the line break", () => {
    expect(stripListMarkers("Lead.\n  - Point.")).toBe("Lead.\n  Point.");
  });

  // The two anchors exist for these. An em dash is prose and a hyphen inside a
  // word is part of the word; only a bullet at the head of a line is a marker.
  it("leaves em dashes and hyphens inside prose alone", () => {
    const text = "It is well-known — and load-bearing — that 3-4 holds.";
    expect(stripListMarkers(text)).toBe(text);
  });

  // A hyphen at a line start with no space after it is a word, not a marker.
  it("leaves a line-leading hyphen with no space after it", () => {
    expect(stripListMarkers("Lead.\n-40 degrees.")).toBe("Lead.\n-40 degrees.");
  });

  // Mid-stream the marker's space has not arrived yet. Matching only the
  // completed form would flash the hyphen for one frame and swallow it the next,
  // which is what splitCitations avoids for an unfinished "[1".
  it("withholds a trailing marker while streaming", () => {
    expect(stripListMarkers("Real stars.\n-", true)).toBe("Real stars.\n");
    expect(stripListMarkers("Real stars.\n- ", true)).toBe("Real stars.\n");
    expect(stripListMarkers("Real stars.\n- Hy", true)).toBe("Real stars.\nHy");
  });

  // Once the answer has settled a lone trailing hyphen is something the model
  // meant to write, so it is left alone.
  it("keeps a trailing hyphen once the answer is settled", () => {
    expect(stripListMarkers("Real stars.\n-")).toBe("Real stars.\n-");
  });

  // The strip runs inside answerParts, so the body never renders the marker.
  it("keeps the marker out of the rendered parts", () => {
    const parts = answerParts("Real stars.[1]\n- Next point.", [src(1)]);
    const text = parts.map((p) => (p.kind === "text" ? p.text : "")).join("");
    expect(text).not.toContain("-");
    expect(text).toContain("\nNext point.");
  });
});

describe("groupCited", () => {
  it("gathers moments under their video, in citation order", () => {
    const cited = citedInOrder("A[2] B[1] C[3]", [
      src(1, { video_id: "va", start_seconds: 10 }),
      src(2, { video_id: "vb", start_seconds: 20 }),
      src(3, { video_id: "va", start_seconds: 5 }),
    ]);
    const got = groupCited(cited, [vid("va"), vid("vb")]);

    expect(got.map((g) => g.video.id)).toEqual(["vb", "va"]);
    // Second card holds [1] then [3] — mention order, not 5s before 10s.
    expect(got[1].matches.map((m) => m.start_seconds)).toEqual([10, 5]);
    expect(got[1].matches[0].snippet).toBe("snippet 1");
  });

  it("drops a source whose video never arrived rather than faking a card", () => {
    const cited = citedInOrder("A[1]", [src(1, { video_id: "gone" })]);
    expect(groupCited(cited, [vid("va")])).toEqual([]);
  });
});
