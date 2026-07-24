import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { CATEGORIES } from "./categories";

// The Go enum in backend/internal/videos/category.go is the authority;
// categories.ts mirrors its ids and order. Labels are deliberately NOT compared:
// the Go label is the descriptive, classifier-facing form (shown in the classify
// prompt and matched against the model's reply in NormalizeCategory), while the
// TS label is the short display form on the category pills. `id` is the sync key
// between the two. This test still catches an id rename, a reorder, or a
// dropped/added category on either side.
//
// Note: this deliberately avoids the literal `new URL("../x", import.meta.url)`
// pattern — Vite statically recognizes that shape and rewrites it into an
// asset URL (http://localhost:.../@fs/...) under the jsdom test environment,
// which then fails fileURLToPath with "The URL must be of scheme file".
// Resolving the directory first sidesteps that transform.
const HERE = dirname(fileURLToPath(import.meta.url));
const GO_CATEGORY_FILE = resolve(
  HERE,
  "../../backend/internal/videos/category.go",
);

function goCategories(): Array<{ id: string }> {
  const source = readFileSync(GO_CATEGORY_FILE, "utf8");
  const block = source.match(/var Categories = \[\]Category\{([\s\S]*?)\n\}/);
  if (!block) {
    throw new Error("could not find the Categories block in category.go");
  }
  // Each Go entry is {id, label, hint}. Only the id is mirrored (see the header
  // note); the label and hint are matched (either may be non-empty) and dropped.
  return [...block[1].matchAll(/\{"([^"]+)",\s*"([^"]+)",\s*"([^"]*)"\}/g)].map(
    (m) => ({ id: m[1] }),
  );
}

describe("category enum sync", () => {
  it("matches the Go enum ids, in order", () => {
    // Given
    const go = goCategories();

    // Then: ids and order must agree (labels intentionally diverge).
    expect(go.length).toBeGreaterThan(0);
    expect(CATEGORIES.map((c) => ({ id: c.id }))).toEqual(go);
  });
});
