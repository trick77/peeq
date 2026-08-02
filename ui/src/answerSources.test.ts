import { describe, it, expect } from "vitest";
import { answerParts, citedInOrder, groupCited } from "./answerSources";
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

  function shape(text: string, sources: AnswerSource[]) {
    return answerParts(text, sources).map((p) =>
      p.kind === "text" ? p.text : `[${p.source.display}]`,
    );
  }

  it("collapses two passages of one video cited back to back", () => {
    const got = answerParts("Hardest stages[1][2].", oneVideo);
    expect(shape("Hardest stages[1][2].", oneVideo)).toEqual([
      "Hardest stages",
      "[1]",
      ".",
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
      "Hardest stages",
      "[1]",
      ".",
    ]);
  });

  // The model repeating one excerpt. Simpler than the two-passage case — both
  // marks were always the same passage — and it collapsed for the same reason.
  it("collapses a repeated citation", () => {
    expect(shape("As noted[1][1].", [src(1)])).toEqual([
      "As noted",
      "[1]",
      ".",
    ]);
  });

  // Two videos. The numerals differ, so there is nothing to stutter and both
  // marks are evidence the reader can act on.
  it("keeps adjacent marks that show different numerals", () => {
    const got = shape("The riders[1][2].", [
      src(1, { video_id: "va" }),
      src(2, { video_id: "vb" }),
    ]);
    expect(got).toEqual(["The riders", "[1]", "[2]", "."]);
  });

  // One video cited at two different claims. The numeral repeats because each
  // claim is separately sourced, which is the point rather than a stutter.
  it("keeps repeated numerals that prose separates", () => {
    const text = "Early on[1], and later[2].";
    // Both marks show 1 — it is one video — and that is exactly what a reader
    // needs at each claim. Only side by side does it read as a stutter.
    expect(shape(text, oneVideo)).toEqual([
      "Early on",
      "[1]",
      ", and later",
      "[1]",
      ".",
    ]);
    // Still two seeks, to two different moments.
    const marks = answerParts(text, oneVideo).filter((p) => p.kind === "cite");
    expect(
      marks.map((m) => (m.kind === "cite" ? m.source.start_seconds : -1)),
    ).toEqual([70, 640]);
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
    ).toEqual(["Hardest stages", "[1]", "\n\nThe hosts argued", "[1]", "."]);
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
      "Hardest stages",
      "[1]",
      ". Then more",
      "[1]",
      ".",
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
