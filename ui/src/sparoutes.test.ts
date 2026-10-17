import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const srcDir = dirname(fileURLToPath(import.meta.url));

// The Go server serves the shell only on the paths in web/embed.go's routes
// allowlist; anything else is a 404. A view added to parsePath without a line
// there would load in-app but 404 on a cold deep link or a refresh.
describe("server SPA routes", () => {
  it("match parsePath's first segments", () => {
    const goSrc = readFileSync(
      resolve(srcDir, "../../backend/web/embed.go"),
      "utf8",
    );
    const block = /var routes = map\[string\]bool\{([^}]*)\}/.exec(goSrc)?.[1];
    expect(block).toBeTruthy();
    const goRoutes = [...(block ?? "").matchAll(/"([^"]*)":\s*true/g)].map(
      (m) => m[1],
    );

    const routeSrc = readFileSync(resolve(srcDir, "route.ts"), "utf8");
    const parse = routeSrc.slice(
      routeSrc.indexOf("export function parsePath"),
      routeSrc.indexOf("export function toPath"),
    );
    const tsRoutes = [...parse.matchAll(/case "([^"]+)":/g)].map((m) => m[1]);
    // case undefined is the bare "/".
    expect([...goRoutes].sort()).toEqual(["", ...tsRoutes].sort());
  });
});
