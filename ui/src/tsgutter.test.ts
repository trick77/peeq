import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { formatDuration } from "./format";

// Every list that puts a timestamp in front of a line — chapters, highlights,
// transcript cues, search matches, answer sources — reserves a fixed gutter for
// it so the lines all start on one left edge. That alignment is pure layout,
// and jsdom does none, so a rule that went back to min-width (which lets an
// h:mm:ss stamp push its own line across, and only that line) would pass every
// component test. The CSS is read instead, the way gridlayout.test.ts and
// tocgrid.test.ts read theirs.
const HERE = dirname(fileURLToPath(import.meta.url));
const CSS = readFileSync(resolve(HERE, "./index.css"), "utf8");

// The widest stamp the mono stack renders at each of these sizes, measured in a
// browser: "2:16:36" is 52.7px at 12.5px, 50.6px at 12px, 48.5px at 11.5px. A
// gutter narrower than its own row's stamp is the bug this guards.
const GUTTERS = [
  { sel: ".toc .ts", size: 12.5, stamp: 52.7 },
  { sel: ".hl .ts", size: 12.5, stamp: 52.7 },
  { sel: ".transcript .ts", size: 11.5, stamp: 48.5 },
  { sel: ".match .ts", size: 12, stamp: 50.6 },
  { sel: ".srcrow .ts", size: 12, stamp: 50.6 },
];

const block = (sel: string) =>
  new RegExp(`${sel.replace(/\./g, "\\.")}\\s*\\{([^}]*)\\}`).exec(CSS)?.[1] ??
  "";

describe("timestamp gutters", () => {
  it.each(GUTTERS)("$sel reserves one width for every row", ({ sel }) => {
    const rule = block(sel);
    expect(`${sel}: found`).toBe(`${sel}: ${rule ? "found" : "missing"}`);
    // min-width is the regression: it sizes the gutter per row, so the long
    // stamps indent their own line and the list loses its left edge.
    expect(`${sel}: ${/\bmin-width:/.test(rule)}`).toBe(`${sel}: false`);
    expect(`${sel}: ${/\bwidth:\s*\d+px/.test(rule)}`).toBe(`${sel}: true`);
  });

  it.each(GUTTERS)("$sel is wide enough for h:mm:ss", ({ sel, stamp }) => {
    const px = Number(/\bwidth:\s*(\d+)px/.exec(block(sel))?.[1] ?? 0);
    expect(`${sel}: ${px >= stamp}`).toBe(`${sel}: true`);
  });

  it("is h:mm:ss that the gutters have to fit", () => {
    // The measurements above are of formatDuration's own output; if the format
    // ever grew a field, they would all be short by one.
    expect(formatDuration(2 * 3600 + 16 * 60 + 36)).toBe("2:16:36");
  });
});
