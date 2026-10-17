import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// The browser must never load anything from a YouTube image host. Posters and
// channel art come through peeq's own endpoints, which fetch them on the serial
// queue and cache them; a signed i.ytimg.com URL in an <img> also expires and
// renders broken. The server backs this with an img-src 'self' CSP — this test
// catches the mistake before it ships rather than in the console.
const FORBIDDEN = /ytimg\.com|ggpht\.com|googleusercontent\.com/;

const srcDir = dirname(fileURLToPath(import.meta.url));

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sourceFiles(path);
    if (!/\.(ts|tsx|css)$/.test(name) || /\.test\.tsx?$/.test(name)) return [];
    return [path];
  });
}

describe("no YouTube image hosts", () => {
  it("no shipped source file names one", () => {
    const hits = sourceFiles(srcDir)
      .filter((file) =>
        readFileSync(file, "utf8")
          .split("\n")
          // Comments may explain the rule; only code can break it.
          .some(
            (line) => !/^\s*(\/\/|\/?\*)/.test(line) && FORBIDDEN.test(line),
          ),
      )
      .map((file) => relative(srcDir, file));
    expect(hits).toEqual([]);
  });
});
