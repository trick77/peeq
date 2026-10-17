import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// The browser must never load anything from a YouTube image host. Posters and
// channel art come through peeq's own endpoints, which fetch them on the serial
// queue and cache them; a signed i.ytimg.com URL in an <img> also expires and
// renders broken. The server backs this with an img-src 'self' CSP — this test
// catches the mistake before it ships rather than in the console.
const FORBIDDEN =
  /ytimg\.com|img\.youtube\.com|ggpht\.com|googleusercontent\.com/;

const srcDir = dirname(fileURLToPath(import.meta.url));

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return sourceFiles(path);
    if (!/\.(ts|tsx|css)$/.test(name) || /\.test\.tsx?$/.test(name)) return [];
    return [path];
  });
}

// withoutComments drops block comments (JSX `{/* */}` included) and line
// comments, so a comment explaining the rule is not a hit. A `//` only starts a
// comment at line start or after whitespace: the one in "https://" does not.
function withoutComments(source: string): string {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/(^|\s)\/\/.*$/gm, "$1");
}

describe("no YouTube image hosts", () => {
  it("no shipped source file names one", () => {
    const hits = sourceFiles(srcDir)
      .filter((file) =>
        FORBIDDEN.test(withoutComments(readFileSync(file, "utf8"))),
      )
      .map((file) => relative(srcDir, file));
    expect(hits).toEqual([]);
  });

  it("strips comments but still sees code", () => {
    expect(
      FORBIDDEN.test(
        withoutComments("{/* never i.ytimg.com */}\n// ggpht.com"),
      ),
    ).toBe(false);
    expect(
      FORBIDDEN.test(
        withoutComments(
          'const u = "https://img.youtube.com/vi/x/0.jpg"; // ok',
        ),
      ),
    ).toBe(true);
  });

  it("the dev server sends the same image policy as the Go server", () => {
    const goPolicy = /const imgSrcPolicy = "([^"]+)"/.exec(
      readFileSync(
        resolve(srcDir, "../../backend/internal/httpapi/csp.go"),
        "utf8",
      ),
    )?.[1];
    const vitePolicy = /"Content-Security-Policy":\s*"([^"]+)"/.exec(
      readFileSync(resolve(srcDir, "../vite.config.ts"), "utf8"),
    )?.[1];
    expect(goPolicy).toBeTruthy();
    expect(vitePolicy).toBe(goPolicy);
  });
});
