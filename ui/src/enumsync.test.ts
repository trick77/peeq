import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { CATEGORIES } from "./categories";

// The Go enum in backend/internal/videos/category.go is the authority;
// categories.ts mirrors it. Until this test existed, a label rename on
// either side drifted silently — the id/count checks could not see it.
//
// Note: this deliberately avoids the literal `new URL("../x", import.meta.url)`
// pattern — Vite statically recognizes that shape and rewrites it into an
// asset URL (http://localhost:.../@fs/...) under the jsdom test environment,
// which then fails fileURLToPath with "The URL must be of scheme file".
// Resolving the directory first sidesteps that transform.
const HERE = dirname(fileURLToPath(import.meta.url));
const GO_CATEGORY_FILE = resolve(HERE, "../../backend/internal/videos/category.go");

function goCategories(): Array<{ id: string; label: string }> {
  const source = readFileSync(GO_CATEGORY_FILE, "utf8");
  const block = source.match(/var Categories = \[\]Category\{([\s\S]*?)\n\}/);
  if (!block) {
    throw new Error("could not find the Categories block in category.go");
  }
  return [...block[1].matchAll(/\{"([^"]+)",\s*"([^"]+)"\}/g)].map((m) => ({
    id: m[1],
    label: m[2],
  }));
}

describe("category enum sync", () => {
  it("matches the Go enum entry for entry, in order", () => {
    // Given
    const go = goCategories();

    // Then: ids, labels, and order must all agree.
    expect(go.length).toBeGreaterThan(0);
    expect(CATEGORIES.map((c) => ({ id: c.id, label: c.label }))).toEqual(go);
  });
});
