import { describe, expect, it } from "vitest";
import { CATEGORIES, CATEGORY_BY_ID, UNCATEGORIZED } from "./categories";

describe("categories", () => {
  it("has 23 entries including ai and uncategorized", () => {
    expect(CATEGORIES).toHaveLength(23);
    expect(CATEGORY_BY_ID["ai"].label).toBe("Artificial Intelligence");
    expect(CATEGORY_BY_ID[UNCATEGORIZED].label).toBe("Uncategorized");
  });
  it("every entry has a color", () => {
    for (const c of CATEGORIES) expect(c.color).toMatch(/^#/);
  });
  it("gives every entry its own color, since the dot is the only cue", () => {
    const colors = CATEGORIES.map((c) => c.color);
    expect(new Set(colors).size).toBe(colors.length);
  });
  it("keeps music out of entertainment's label now that it has its own id", () => {
    expect(CATEGORY_BY_ID["music"].label).toBe("Music");
    expect(CATEGORY_BY_ID["entertainment"].label).toBe("Entertainment");
  });
});
