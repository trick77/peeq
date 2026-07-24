import { describe, expect, it } from "vitest";
import { formatAgo } from "./format";

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
