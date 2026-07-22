import { describe, expect, it } from "vitest";
import { CATEGORIES, CATEGORY_BY_ID, UNCATEGORIZED } from "./categories";

describe("categories", () => {
  it("has 15 entries including ai and uncategorized", () => {
    expect(CATEGORIES).toHaveLength(15);
    expect(CATEGORY_BY_ID["ai"].label).toBe("Artificial Intelligence");
    expect(CATEGORY_BY_ID[UNCATEGORIZED].label).toBe("Uncategorized");
  });
  it("every entry has a color", () => {
    for (const c of CATEGORIES) expect(c.color).toMatch(/^#/);
  });
});
