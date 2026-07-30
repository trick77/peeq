import { describe, it, expect } from "vitest";
import { splitCitations } from "./citations";

const known = new Set([1, 2, 3]);

describe("splitCitations", () => {
  it("returns plain text unchanged", () => {
    expect(splitCitations("just prose", known)).toEqual([
      { kind: "text", text: "just prose" },
    ]);
  });

  it("pulls a citation out of its surroundings", () => {
    expect(splitCitations("Attia says so[1] here.", known)).toEqual([
      { kind: "text", text: "Attia says so" },
      { kind: "cite", n: 1 },
      { kind: "text", text: " here." },
    ]);
  });

  it("handles adjacent citations", () => {
    const got = splitCitations("both[1][2] agree", known);
    expect(got.filter((p) => p.kind === "cite")).toEqual([
      { kind: "cite", n: 1 },
      { kind: "cite", n: 2 },
    ]);
  });

  // The model is told to cite only what it was given, but a hallucinated marker
  // must render as the characters produced rather than as a link to nothing.
  it("leaves an unknown citation number as literal text", () => {
    const got = splitCitations("claim[9] here", known);
    expect(got.some((p) => p.kind === "cite")).toBe(false);
    expect(got.map((p) => (p.kind === "text" ? p.text : "")).join("")).toBe(
      "claim[9] here",
    );
  });

  // Mid-stream, a citation arrives one character at a time. Rendering the
  // partial marker would flash a raw bracket and then swallow it a frame later.
  it("withholds an incomplete trailing marker", () => {
    for (const partial of ["as shown[", "as shown[1", "as shown[12"]) {
      const got = splitCitations(partial, known);
      const rendered = got
        .map((p) => (p.kind === "text" ? p.text : ""))
        .join("");
      expect(rendered).toBe("as shown");
    }
  });

  it("completes the marker once its bracket arrives", () => {
    const got = splitCitations("as shown[1]", known);
    expect(got[got.length - 1]).toEqual({ kind: "cite", n: 1 });
  });

  // A bracket that is not a citation is ordinary punctuation.
  it("keeps non-numeric brackets as text", () => {
    const got = splitCitations("an aside [see below] here", known);
    expect(got.every((p) => p.kind === "text")).toBe(true);
    expect(got.map((p) => (p.kind === "text" ? p.text : "")).join("")).toBe(
      "an aside [see below] here",
    );
  });

  // A stray "[" earlier in the answer must not consume the "]" that closes a
  // real marker further along, or the citation renders as literal text.
  it("still links a citation after an unmatched bracket", () => {
    const got = splitCitations("an aside [ and so[1] here", known);
    expect(got.filter((p) => p.kind === "cite")).toEqual([
      { kind: "cite", n: 1 },
    ]);
    expect(got.map((p) => (p.kind === "text" ? p.text : "")).join("")).toBe(
      "an aside [ and so here",
    );
  });

  it("handles empty text", () => {
    expect(splitCitations("", known)).toEqual([]);
  });

  it("renders nothing as a citation when no sources are known", () => {
    const got = splitCitations("claim[1]", new Set());
    expect(got.some((p) => p.kind === "cite")).toBe(false);
  });
});
