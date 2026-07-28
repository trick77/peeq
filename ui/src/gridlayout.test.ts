import { readdirSync, readFileSync } from "node:fs";
import { dirname, resolve, sep } from "node:path";
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

// columnRules returns every grid-template-columns declaration that applies to
// `selector`, in source order: the base rule first, then any step-downs inside
// @container/@media. Collecting them ALL is the point — a responsive override
// is the easiest place for a bare `1fr` to survive a fix to the base rule.
function columnRules(selector: string): string[] {
  const re = new RegExp(
    `\\${selector}\\s*\\{[^}]*?grid-template-columns:\\s*([^;]+);`,
    "g",
  );
  return [...CSS.matchAll(re)].map((m) => m[1].trim());
}

function gridRules(): string[] {
  return columnRules(".grid");
}

// expectClampedTracks is the shared assertion: every track's floor is pinned at
// 0 so no column can size to its own content.
function expectClampedTracks(rules: string[]) {
  expect(rules.length).toBeGreaterThan(0);
  for (const rule of rules) {
    expect(rule).toContain("minmax(0");
    // A `1fr` OUTSIDE a minmax() is the defect — it is shorthand for
    // minmax(auto, 1fr), and that `auto` is the min-content floor. Drop the
    // minmax groups (whose own `1fr` max is correct and required) and assert
    // nothing bare survives, so `minmax(0, 1fr) 1fr` cannot slip through.
    expect(rule.replace(/minmax\([^)]*\)/g, "")).not.toContain("1fr");
  }
}

describe("library card grid", () => {
  it("clamps every track's floor so the columns cannot size to their own content", () => {
    expectClampedTracks(gridRules());
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
    // would silently win or lose depending on source order. The inner
    // `(?:[^{}]*\{[^{}]*\})*?` skips any sibling rules that precede `.grid`
    // inside the block, so the guard does not depend on it being first.
    const mediaDriven = (css: string) => [
      ...css.matchAll(
        /@media[^{]*\{(?:[^{}]*\{[^{}]*\})*?[^{}]*\.grid\s*\{[^}]*grid-template-columns/g,
      ),
    ];
    // A positive control first: `toHaveLength(0)` passes for free on a regex
    // that matches nothing, so prove it still bites — once with `.grid` first
    // in the block and once with a sibling rule ahead of it.
    expect(
      mediaDriven(
        "@media (max-width: 620px) { .grid { grid-template-columns: 1fr; } }",
      ),
    ).toHaveLength(1);
    expect(
      mediaDriven(
        "@media (max-width: 620px) { .chips { gap: 4px; } .grid { grid-template-columns: 1fr; } }",
      ),
    ).toHaveLength(1);

    expect(mediaDriven(CSS)).toHaveLength(0);
  });

  it("gives every .grid a .gridwrap to query", () => {
    // The step-downs are NAMED container queries, so a `.grid` with no
    // `.gridwrap` ancestor matches neither of them and sits at three columns at
    // every width — three ~110px cards on a 375px phone. Reading index.css can
    // never catch that, because the miss is in the markup: this walks the views
    // instead and requires any file that renders a `grid` class to render a
    // `gridwrap` too. It is a smoke check, not a proof of nesting, but it fails
    // the whole class of "new grid, forgot the wrapper".
    const views = readdirSync(HERE, { recursive: true, encoding: "utf8" })
      .filter((f) => f.endsWith(".tsx") && !f.endsWith(".test.tsx"))
      .map(
        (f) =>
          [
            f.split(sep).join("/"),
            readFileSync(resolve(HERE, f), "utf8"),
          ] as const,
      );
    expect(views.length).toBeGreaterThan(0);

    // The class attribute may be a plain string or a template literal, so match
    // the `grid` token inside any className value rather than a fixed shape.
    // The lookarounds keep `toc-grid` and `playgrid` — different components with
    // their own columns — from being dragged in.
    const renders = (src: string, cls: string) =>
      new RegExp(
        String.raw`className=\{?[\`"][^\`"]*(?<![-\w])${cls}(?![-\w])`,
      ).test(src);

    const consumers = views.filter(([, src]) => renders(src, "grid"));
    // A positive control, deliberately NOT an exact set: the wrapper assertion
    // below passes vacuously if `renders` ever stops matching, so the three
    // known consumers must be found. Pinning the set exactly would instead fail
    // — with an opaque array diff, in a test about wrappers — the first time
    // someone legitimately adds a fourth card grid, which is the moment this
    // guard is most useful and least welcome to argue with.
    expect(consumers.map(([f]) => f).sort()).toEqual(
      expect.arrayContaining([
        "views/Inbox.tsx",
        "views/Library.tsx",
        "views/channel/ArchiveTab.tsx",
      ]),
    );
    // Matched against the rendered class, not the file text: the prose above
    // each wrapper names `.gridwrap`, so a bare `includes` would pass on a file
    // that had lost the element and kept the comment.
    for (const [file, src] of consumers) {
      expect(`${file}: ${renders(src, "gridwrap")}`).toBe(`${file}: true`);
    }
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

// The Settings panels hit the identical defect the card grid already fixed, so
// they are guarded the same way rather than left to be rediscovered: `.presets`
// held a `white-space: nowrap` format selector ~60 characters long and `.row2`
// holds text inputs with a ~170px intrinsic minimum. Both sized their own tracks
// and pushed out through `.sect`'s border.
describe("settings panel grids", () => {
  for (const selector of [".presets", ".row2"]) {
    it(`clamps every ${selector} track, including the single-column step-down`, () => {
      const rules = columnRules(selector);
      // Two rules: the base one and the <=620px override. A bare `1fr` in the
      // override alone still overflows, just only on a narrow window — which is
      // exactly the width nobody re-checks after fixing the base rule.
      expect(rules.length).toBe(2);
      expectClampedTracks(rules);
    });
  }

  it("lets the grid items shrink below their content too", () => {
    // The minmax above frees the TRACK; each item keeps its own min-width: auto
    // floor until this clears it, and both are needed before `.preset code`'s
    // text-overflow: ellipsis can ever engage.
    expect(CSS).toMatch(/^\.preset\s*\{[^}]*min-width:\s*0/m);
    // Scoped to .row2's children on purpose — `.ctrl` is used throughout
    // Settings and must not pick up a global min-width.
    expect(CSS).toMatch(/\.row2\s*>\s*\.ctrl\s*\{[^}]*min-width:\s*0/);
  });
});
