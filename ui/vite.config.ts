/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: { outDir: "../backend/web/dist", emptyOutDir: true },
  server: { proxy: { "/api": "http://127.0.0.1:8080" } },
  // Task 14: view tests (Library/Player/Settings) render components and
  // fire DOM events (timeupdate, form submits) via @testing-library/react,
  // which needs a real DOM — jsdom, wired here plus the jest-dom matchers
  // in setupFiles. App.test.tsx's SSR-only renderToStaticMarkup test still
  // runs fine under jsdom.
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test-setup.ts"],
  },
});
