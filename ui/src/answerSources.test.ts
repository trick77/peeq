import { describe, it, expect } from "vitest";
import { citedInOrder, groupCited } from "./answerSources";
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
