import { describe, it, expect } from "vitest";
import { splitIntoSegments } from "./streamFade";

describe("splitIntoSegments", () => {
  // Joining the segments must reproduce the input exactly, or the fade would
  // silently alter the answer's spacing.
  it("is lossless", () => {
    for (const input of [
      "Yes — twice, and the two takes disagree.",
      "one two three four five six seven eight nine ten",
      "trailing space ",
      "  leading space",
      "a",
      "",
    ]) {
      expect(splitIntoSegments(input).join("")).toBe(input);
    }
  });

  it("cuts at clause punctuation", () => {
    const segs = splitIntoSegments("First part, second part.");
    expect(segs[0]).toBe("First part, ");
  });

  it("cuts long runs without punctuation", () => {
    const segs = splitIntoSegments("word ".repeat(40));
    expect(segs.length).toBeGreaterThan(1);
    for (const s of segs) {
      // The bound is applied after appending a whole word, so a segment can
      // overshoot by at most one word.
      expect(s.length).toBeLessThan(28 + 10);
    }
  });

  // Segment boundaries must be stable as text grows, or every render would
  // re-wrap earlier text and the whole answer would re-animate on every token.
  it("keeps earlier segments identical as text is appended", () => {
    const prefix = "Yes — twice, and the two takes disagree.";
    const before = splitIntoSegments(prefix);
    const after = splitIntoSegments(prefix + " Attia argues that sodium");
    expect(after.slice(0, before.length - 1)).toEqual(
      before.slice(0, before.length - 1),
    );
  });

  it("returns nothing for an empty string", () => {
    expect(splitIntoSegments("")).toEqual([]);
  });
});
