import { describe, expect, it } from "vitest";
import { formatAge, formatAgo } from "./format";

// A fixed "now" so the relative output is deterministic. daysSince (which
// formatAgo builds on) floors whole days, so each case picks a day offset
// squarely inside its bucket rather than on a boundary.
const NOW = new Date("2026-07-24T12:00:00Z");
const daysAgo = (n: number) =>
  new Date(NOW.getTime() - n * 86400000).toISOString();

describe("formatAgo", () => {
  it("returns empty string for a missing timestamp", () => {
    expect(formatAgo(undefined, NOW)).toBe("");
  });

  it("says 'today' for now and for a future date", () => {
    expect(formatAgo(NOW.toISOString(), NOW)).toBe("today");
    expect(formatAgo(daysAgo(-3), NOW)).toBe("today");
  });

  it("counts days with singular/plural under a month", () => {
    expect(formatAgo(daysAgo(1), NOW)).toBe("1 day ago");
    expect(formatAgo(daysAgo(3), NOW)).toBe("3 days ago");
    expect(formatAgo(daysAgo(29), NOW)).toBe("29 days ago");
  });

  it("rounds to months between 30 and 364 days", () => {
    expect(formatAgo(daysAgo(30), NOW)).toBe("1 month ago");
    expect(formatAgo(daysAgo(150), NOW)).toBe("5 months ago");
  });

  it("caps months at 11 so it never reads '12 months ago'", () => {
    // ~345+ days rounds to 12 months without the cap; it must stay 11 until
    // the 365-day year threshold takes over.
    expect(formatAgo(daysAgo(355), NOW)).toBe("11 months ago");
    expect(formatAgo(daysAgo(364), NOW)).toBe("11 months ago");
  });

  it("rounds to years at a year and beyond", () => {
    expect(formatAgo(daysAgo(365), NOW)).toBe("1 year ago");
    expect(formatAgo(daysAgo(740), NOW)).toBe("2 years ago");
  });
});

describe("formatAge", () => {
  // The channel header renders formatAge into a stat cell that must not
  // collapse, and the library card eyebrow puts it beside a formatAgo value.
  // Both depend on the em dash, so pin it: a later pass at making the two
  // helpers "consistent" must not quietly turn it into an empty string.
  it("returns an em dash, not an empty string, for unknown input", () => {
    expect(formatAge(undefined, NOW)).toBe("—");
    expect(formatAge("not-a-date", NOW)).toBe("—");
  });

  it("says 'today' for now and for a future date", () => {
    expect(formatAge(NOW.toISOString(), NOW)).toBe("today");
    expect(formatAge(daysAgo(-3), NOW)).toBe("today");
  });

  it("renders the same buckets as formatAgo, abbreviated", () => {
    expect(formatAge(daysAgo(1), NOW)).toBe("1 d ago");
    expect(formatAge(daysAgo(10), NOW)).toBe("10 d ago");
    expect(formatAge(daysAgo(90), NOW)).toBe("3 mo ago");
    expect(formatAge(daysAgo(400), NOW)).toBe("1 y ago");
  });
});
