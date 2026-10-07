import { describe, it, expect } from "vitest";
import { parseInDepth, readMinutes } from "./inDepth";

describe("parseInDepth", () => {
  it("splits the lead from headed sections and reads their stamps", () => {
    const got = parseInDepth(
      "Lead one.\n\nLead two.\n\n### Flow temperature decides it [4:12]\n\nBody A.\n\n" +
        "### A late point [1:02:05]\nBody B first.\n\nBody B second.",
    );
    expect(got.lead).toEqual(["Lead one.", "Lead two."]);
    expect(got.sections).toEqual([
      {
        heading: "Flow temperature decides it",
        stamp: "4:12",
        seconds: 252,
        body: ["Body A."],
      },
      {
        heading: "A late point",
        stamp: "1:02:05",
        seconds: 3725,
        body: ["Body B first.", "Body B second."],
      },
    ]);
  });

  // A heading the model wrote without a stamp is still a heading; only the
  // seek is missing.
  it("keeps a heading with no stamp", () => {
    const got = parseInDepth("Lead.\n\n### No stamp here\n\nBody.");
    expect(got.sections[0]).toEqual({
      heading: "No stamp here",
      stamp: null,
      seconds: null,
      body: ["Body."],
    });
  });

  // A model that ignored the format: everything reads as the lead, which is
  // the short summary's look.
  it("renders unformatted text as plain paragraphs", () => {
    const got = parseInDepth("One.\n\nTwo.");
    expect(got).toEqual({ lead: ["One.", "Two."], sections: [] });
  });
});

describe("readMinutes", () => {
  it("rounds up at 230 words a minute, never below one", () => {
    expect(readMinutes("word")).toBe(1);
    expect(readMinutes(Array(231).fill("w").join(" "))).toBe(2);
    expect(readMinutes(Array(650).fill("w").join(" "))).toBe(3);
  });
});
