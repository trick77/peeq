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
    if (!/\.(ts|tsx|css|html|webmanifest)$/.test(name)) return [];
    if (/\.test\.tsx?$/.test(name)) return [];
    return [path];
  });
}

// Everything the browser is handed: the app source, the HTML shell and the
// static files copied verbatim into the build.
const uiDir = resolve(srcDir, "..");
const shipped = [
  ...sourceFiles(srcDir),
  join(uiDir, "index.html"),
  ...sourceFiles(join(uiDir, "public")),
];

// withoutComments drops comments that stand on their own, so a comment
// explaining the rule is not a hit: block comments starting a line (JSX
// `{/* */}` included) and whole-line `//` comments. Deliberately nothing
// after code on the same line — a `//` or `/*` there may sit inside a string
// or template literal that really does build a YouTube URL.
function withoutComments(source: string): string {
  return source
    .replace(/^\s*\{?\/\*[\s\S]*?\*\/\}?/gm, "")
    .replace(/^\s*\/\/.*$/gm, "");
}

describe("no YouTube image hosts", () => {
  it("no shipped file names one", () => {
    const hits = shipped
      .filter((file) =>
        FORBIDDEN.test(withoutComments(readFileSync(file, "utf8"))),
      )
      .map((file) => relative(uiDir, file));
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
    // A `//` inside a literal after code is not a comment.
    expect(
      FORBIDDEN.test(
        withoutComments("const u = `${base} //i.ytimg.com/vi/${id}/0.jpg`;"),
      ),
    ).toBe(true);
  });

  it("the Vite servers send the same image policy as the Go server", () => {
    const goPolicy = /const imgSrcPolicy = "([^"]+)"/.exec(
      readFileSync(
        resolve(srcDir, "../../backend/internal/httpapi/csp.go"),
        "utf8",
      ),
    )?.[1];
    const vitePolicies = [
      ...readFileSync(resolve(srcDir, "../vite.config.ts"), "utf8").matchAll(
        /"Content-Security-Policy":\s*"([^"]+)"/g,
      ),
    ].map((m) => m[1]);
    expect(goPolicy).toBeTruthy();
    // server (make dev) and preview (vite preview of the build).
    expect(vitePolicies).toEqual([goPolicy, goPolicy]);
  });
});
