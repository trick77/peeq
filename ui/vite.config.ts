import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "../backend/web/dist",
    // ship empties the dir but for its .gitkeep before the build; vite's own
    // emptying would take the .gitkeep with it.
    emptyOutDir: false,
    rollupOptions: {
      output: {
        // React and the icon set go in a chunk of their own. Assets are served
        // immutable under a content hash, and the app's own code changes with
        // every release while these change a few times a year: kept together,
        // each deploy made every browser download React again.
        manualChunks(id: string) {
          if (
            /node_modules\/(react|react-dom|scheduler|lucide-react)\//.test(id)
          )
            return "vendor";
        },
      },
    },
  },
  server: {
    proxy: { "/api": "http://127.0.0.1:8080" },
    // Same img-src the Go server sends (backend/internal/httpapi/csp.go), so a
    // hotlinked YouTube image breaks under `make dev` too, not first in
    // production. noyoutubehosts.test.ts keeps the directive identical. Only
    // img-src: Go's default-src 'self' would block Vite's inline HMR preamble.
    headers: { "Content-Security-Policy": "img-src 'self' data: blob:" },
  },
  preview: {
    headers: { "Content-Security-Policy": "img-src 'self' data: blob:" },
  },
  // Task 14: view tests (Library/Player/Settings) render components and
  // fire DOM events (timeupdate, form submits) via @testing-library/react,
  // which needs a real DOM — jsdom, wired here plus the jest-dom matchers
  // in setupFiles. App.test.tsx's SSR-only renderToStaticMarkup test still
  // runs fine under jsdom.
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    coverage: {
      provider: "v8",
      // json-summary is what scripts/coverage-gate.sh reads (the project floor);
      // lcov is what diff-cover reads in scripts/patch-coverage.sh (patch
      // coverage); text-summary is for humans reading the CI log.
      reporter: ["text-summary", "json-summary", "lcov"],
      reportsDirectory: "../coverage/ui",
      // Without an explicit include, the v8 provider only instruments files that
      // some test actually imports. A source file nobody tests then contributes
      // nothing to either side of the ratio and is silently invisible — which
      // inflates the reported percentage and lets the floor be met while whole
      // modules go untested. Measured here: 31 files reported vs 37 on disk.
      include: ["src/**/*.{ts,tsx}"],
      // Pure type declarations, app bootstrap and test scaffolding — nothing
      // here is behaviour a test could meaningfully assert.
      exclude: [
        "src/main.tsx",
        "src/test/**",
        "src/api/types.ts",
        "**/*.test.{ts,tsx}",
      ],
    },
  },
});
