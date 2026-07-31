import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { tocGridStyle } from "./ui";

// The chapter list reads down its columns, and that is half CSS and half
// markup: the grid flows column-major only if it is also told how many rows to
// fill, and nothing in a rendered test can see it — jsdom does no layout, so a
// grid that had silently gone back to row-major would still pass every
// component test. So the CSS is read, the same way gridlayout.test.ts reads the
// card grid's tracks.
const HERE = dirname(fileURLToPath(import.meta.url));
const CSS = readFileSync(resolve(HERE, "./index.css"), "utf8");
const read = (f: string) => readFileSync(resolve(HERE, f), "utf8");

// The base .toc.toc-grid block, up to its closing brace.
const base = /\.toc\.toc-grid\s*\{([^}]*)\}/.exec(CSS)?.[1] ?? "";
// …and the one inside the narrow-viewport media query.
const narrow =
  /@media[^{]*\{\s*\.toc\.toc-grid\s*\{([^}]*)\}/.exec(CSS)?.[1] ?? "";

describe("chapter grid", () => {
  it("flows its two columns top-to-bottom", () => {
    expect(base).toMatch(/grid-auto-flow:\s*column/);
    expect(base).toMatch(/grid-template-columns:\s*1fr 1fr/);
    // Column-major fills the EXPLICIT rows first, so an implicit row count
    // would put one item per column and open a new column for each. The count
    // comes from the caller through the custom property.
    expect(base).toMatch(/grid-template-rows:\s*repeat\(var\(--toc-rows/);
  });

  it("goes back to one column, in order, when it is too narrow for two", () => {
    expect(narrow).toMatch(/grid-template-columns:\s*1fr/);
    // Both of these, or a single column would still be flowing column-major
    // against a leftover row count — which reads as one item, then a break.
    expect(narrow).toMatch(/grid-auto-flow:\s*row/);
    expect(narrow).toMatch(/grid-template-rows:\s*none/);
  });

  it("splits the chapters in half, the longer column first", () => {
    expect(tocGridStyle(12)).toEqual({ "--toc-rows": 6 });
    expect(tocGridStyle(11)).toEqual({ "--toc-rows": 6 });
    expect(tocGridStyle(1)).toEqual({ "--toc-rows": 1 });
    // A card with no chapters renders no grid, but a zero row count would be an
    // invalid repeat() and take the whole rule down with it.
    expect(tocGridStyle(0)).toEqual({ "--toc-rows": 1 });
  });

  it("is sized by every card that renders it", () => {
    // The property has a fallback, so a call site that forgot the style would
    // render a one-row grid rather than break — silent, and only visible on a
    // real page. Both chapter cards are checked by name.
    for (const f of ["./views/player/ContentsCard.tsx", "./views/Share.tsx"]) {
      const src = read(f);
      expect(`${f}: ${/toc toc-grid/.test(src)}`).toBe(`${f}: true`);
      expect(`${f}: ${/style=\{tocGridStyle\(/.test(src)}`).toBe(`${f}: true`);
    }
  });
});
