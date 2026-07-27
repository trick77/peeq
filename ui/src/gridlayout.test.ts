import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// The card grid's column rules are pinned by reading index.css, not by rendering
// — jsdom does no layout, so a component test cannot see a track resolve to the
// wrong width, which is exactly how this regressed unnoticed.
//
// The bug: `repeat(3, 1fr)` is `repeat(3, minmax(auto, 1fr))`, and that `auto`
// floor is each item's min-content width. `.card.video-card .by` is a
// `white-space: nowrap` row, so every column grew to fit its OWN byline. On an
// iPad the three tracks measured 303 / 332 / 282px: three different thumbnail
// sizes in one row, titles starting at three different heights, and the grid
// overflowing the page rather than stepping down a column.
//
// Two rules keep it fixed and both are easy to undo by accident, so both are
// asserted here. See the block comment above `.grid` in index.css.
//
// Resolving the directory before joining follows enumsync.test.ts: the literal
// `new URL("../x", import.meta.url)` shape is statically rewritten by Vite into
// an asset URL under jsdom, which then fails fileURLToPath.
const HERE = dirname(fileURLToPath(import.meta.url));
const CSS = readFileSync(resolve(HERE, "./index.css"), "utf8");

// gridRules returns every grid-template-columns declaration that applies to
// `.grid`, in source order: the base rule first, then the @container step-downs.
function gridRules(): string[] {
  return [
    ...CSS.matchAll(/\.grid\s*\{[^}]*?grid-template-columns:\s*([^;]+);/g),
  ].map((m) => m[1].trim());
}

describe("library card grid", () => {
  it("clamps every track's floor so the columns cannot size to their own content", () => {
    const rules = gridRules();
    expect(rules.length).toBeGreaterThan(0);
    for (const rule of rules) {
      expect(rule).toContain("minmax(0");
      // A `1fr` OUTSIDE a minmax() is the defect — it is shorthand for
      // minmax(auto, 1fr), and that `auto` is the min-content floor. Drop the
      // minmax groups (whose own `1fr` max is correct and required) and assert
      // nothing bare survives, so `minmax(0, 1fr) 1fr` cannot slip through.
      expect(rule.replace(/minmax\([^)]*\)/g, "")).not.toContain("1fr");
    }
  });

  it("steps the column count off the grid's own width, not the viewport", () => {
    // The grid never gets the viewport: the 248px rail and .page's 34px padding
    // take 316px before it sees a pixel, so a viewport breakpoint fires at the
    // wrong moment — at 1194px the old `max-width: 1040px` rule did not fire and
    // three cards fought over 878px.
    expect(CSS).toMatch(/\.gridwrap\s*\{[^}]*container-type:\s*inline-size/);

    const steps = [
      ...CSS.matchAll(/@container\s+gridwrap\s*\(max-width:\s*(\d+)px\)/g),
    ].map((m) => Number(m[1]));
    expect(steps).toEqual([939, 619]);

    // And no @media rule may still be driving .grid's columns — a leftover one
    // would silently win or lose depending on source order.
    const mediaDriven = [
      ...CSS.matchAll(/@media[^{]*\{\s*\.grid\s*\{[^}]*grid-template-columns/g),
    ];
    expect(mediaDriven).toHaveLength(0);
  });

  it("lets a card shrink below its content so it stays inside its clamped track", () => {
    // Without min-width: 0 the card overflows the track that minmax(0, 1fr) just
    // stopped it from widening — the nowrap byline pushes straight back out.
    expect(CSS).toMatch(/^\.card\s*\{[^}]*min-width:\s*0/m);
    expect(CSS).toMatch(
      /\.card\.video-card\s+\.by\s*\{[^}]*overflow:\s*hidden/,
    );
  });
});
