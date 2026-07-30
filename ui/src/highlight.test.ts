import { describe, it, expect } from "vitest";
import { HIGHLIGHT_END, HIGHLIGHT_START, splitHighlights } from "./highlight";

const S = HIGHLIGHT_START;
const E = HIGHLIGHT_END;

describe("splitHighlights", () => {
  it("returns one plain segment for undelimited text", () => {
    expect(splitHighlights("just words")).toEqual([
      { text: "just words", match: false },
    ]);
  });

  it("splits a delimited term out of its surroundings", () => {
    expect(
      splitHighlights(`replace the ${S}electrolytes${E} you lose`),
    ).toEqual([
      { text: "replace the ", match: false },
      { text: "electrolytes", match: true },
      { text: " you lose", match: false },
    ]);
  });

  it("handles several matches in one snippet", () => {
    const got = splitHighlights(`${S}sodium${E} and ${S}potassium${E}`);
    expect(got.filter((s) => s.match).map((s) => s.text)).toEqual([
      "sodium",
      "potassium",
    ]);
  });

  it("handles a match at each edge without emitting empty segments", () => {
    const got = splitHighlights(`${S}alpha${E}${S}beta${E}`);
    expect(got).toEqual([
      { text: "alpha", match: true },
      { text: "beta", match: true },
    ]);
    expect(got.every((s) => s.text.length > 0)).toBe(true);
  });

  it("runs an unclosed match to the end rather than losing the line", () => {
    expect(splitHighlights(`tail ${S}unterminated`)).toEqual([
      { text: "tail ", match: false },
      { text: "unterminated", match: true },
    ]);
  });

  it("drops a stray end delimiter instead of rendering it", () => {
    const got = splitHighlights(`no start ${E}here`);
    expect(got).toEqual([{ text: "no start here", match: false }]);
    expect(got.map((s) => s.text).join("")).not.toContain(E);
  });

  it("never leaks a delimiter into any segment", () => {
    for (const input of [
      `${S}a${E}`,
      `${E}${S}`,
      `${S}${S}a${E}${E}`,
      `plain`,
      ``,
    ]) {
      for (const seg of splitHighlights(input)) {
        expect(seg.text).not.toContain(S);
        expect(seg.text).not.toContain(E);
      }
    }
  });

  it("returns nothing for an empty snippet", () => {
    expect(splitHighlights("")).toEqual([]);
  });
});
