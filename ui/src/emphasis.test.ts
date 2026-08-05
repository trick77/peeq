import { describe, it, expect } from "vitest";
import { applyEmphasis, type MarkedText } from "./emphasis";

// A citation part, the shape applyEmphasis passes through untouched.
const cite = { kind: "cite" as const, n: 1 };

function text(s: string): MarkedText {
  return { kind: "text", text: s };
}

// The rendered result as the reader sees it: what is on screen, and under which
// mark. Delimiters that survived show up here as characters, which is the whole
// point of most of these assertions.
function rendered(parts: ReturnType<typeof applyEmphasis>) {
  return parts.map((p) =>
    p.kind === "text"
      ? [(p as MarkedText).text, (p as MarkedText).mark ?? "plain"]
      : ["·cite·", "cite"],
  );
}

describe("applyEmphasis", () => {
  // The reported bug, verbatim: the model bolded a video title and the reader
  // saw the asterisks.
  it("renders a bolded title as strong with no asterisks", () => {
    const got = applyEmphasis(
      [text('The video **"Why Agentic Systems Need Ontologies"** discusses…')],
      false,
    );
    expect(rendered(got)).toEqual([
      ["The video ", "plain"],
      ['"Why Agentic Systems Need Ontologies"', "strong"],
      [" discusses…", "plain"],
    ]);
  });

  it("renders __bold__, *italic*, _italic_ and `code`", () => {
    expect(rendered(applyEmphasis([text("a __b__ c")], false))).toEqual([
      ["a ", "plain"],
      ["b", "strong"],
      [" c", "plain"],
    ]);
    expect(rendered(applyEmphasis([text("a *b* c")], false))).toEqual([
      ["a ", "plain"],
      ["b", "em"],
      [" c", "plain"],
    ]);
    expect(rendered(applyEmphasis([text("a _b_ c")], false))).toEqual([
      ["a ", "plain"],
      ["b", "em"],
      [" c", "plain"],
    ]);
    expect(
      rendered(applyEmphasis([text("run `npm test` now")], false)),
    ).toEqual([
      ["run ", "plain"],
      ["npm test", "code"],
      [" now", "plain"],
    ]);
  });

  // The reason this runs on the parts rather than on the raw string.
  it("carries a span across a citation", () => {
    const got = applyEmphasis(
      [text("The **Ontologies "), cite, text(" talk** is long."), cite],
      false,
    );
    expect(rendered(got)).toEqual([
      ["The ", "plain"],
      ["Ontologies ", "strong"],
      ["·cite·", "cite"],
      [" talk", "strong"],
      [" is long.", "plain"],
      ["·cite·", "cite"],
    ]);
  });

  // The invariant: the delimiters are never rendered, in any state. Putting
  // them on screen once the answer settles would be the flash the whole file
  // exists to avoid, arriving from the other direction.
  it("drops the asterisks of an unclosed bold at settle", () => {
    expect(rendered(applyEmphasis([text("a **b c")], false))).toEqual([
      ["a ", "plain"],
      ["b c", "strong"],
    ]);
  });

  // The no-flash guarantee for the optimistic kinds, stated as the reader sees
  // it: the segments and the element they sit in are identical either side of
  // the closing delimiter, so nothing re-runs its fade.
  it("renders a bold span identically before and after its closer", () => {
    const before = rendered(
      applyEmphasis([text("The **Ontologies talk")], true),
    );
    const after = rendered(
      applyEmphasis([text("The **Ontologies talk**")], false),
    );
    expect(before).toEqual([
      ["The ", "plain"],
      ["Ontologies talk", "strong"],
    ]);
    expect(after).toEqual(before);
  });

  // A single "*" means nothing until its closer arrives, so mid-stream it takes
  // the text after it with it rather than showing an asterisk a later frame
  // takes away.
  it("withholds an unclosed italic while streaming and renders it at settle", () => {
    expect(rendered(applyEmphasis([text("a *b c")], true))).toEqual([
      ["a ", "plain"],
    ]);
    // Settled, it was never a delimiter. The character is prose and stays.
    expect(rendered(applyEmphasis([text("a *b c")], false))).toEqual([
      ["a *b c", "plain"],
    ]);
  });

  it("leaves a lone asterisk and a snake_case identifier alone", () => {
    // Followed by a space, so never an opener — which is what keeps the
    // streaming withhold above off ordinary prose.
    expect(rendered(applyEmphasis([text("5 * 3 = 15")], true))).toEqual([
      ["5 * 3 = 15", "plain"],
    ]);
    expect(
      rendered(applyEmphasis([text("the snake_case name")], true)),
    ).toEqual([["the snake_case name", "plain"]]);
  });

  it("marks a heading line and drops its hashes", () => {
    const got = applyEmphasis(
      [text("Intro.\n## Ontology basics\nThen…")],
      false,
    );
    expect(rendered(got)).toEqual([
      ["Intro.\n", "plain"],
      ["Ontology basics", "heading"],
      ["\nThen…", "plain"],
    ]);
  });

  // A heading is the whole line and outranks anything inside it.
  it("keeps a heading's mark over emphasis inside it", () => {
    const got = applyEmphasis([text("# **Ontology** basics")], false);
    expect(rendered(got)).toEqual([["Ontology basics", "heading"]]);
  });

  it("withholds a heading marker whose space has not arrived", () => {
    expect(rendered(applyEmphasis([text("Intro.\n##")], true))).toEqual([
      ["Intro.\n", "plain"],
    ]);
  });

  // One stray delimiter must not bold the rest of the answer.
  it("closes an open span at a newline", () => {
    const got = applyEmphasis([text("a **b\nc d")], false);
    expect(rendered(got)).toEqual([
      ["a ", "plain"],
      ["b", "strong"],
      ["\nc d", "plain"],
    ]);
  });

  it("leaves prose with no markdown in it untouched", () => {
    const parts = [text("Nothing to do here."), cite];
    expect(applyEmphasis(parts, false)).toEqual(parts);
  });
});
