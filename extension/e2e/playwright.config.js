import { defineConfig } from "@playwright/test";

// Chrome extensions load only into a persistent context, which each test
// creates itself — so there is no `use.browserName` here and no shared
// browser. Workers are limited to 1: every test launches its own Chrome with
// its own profile directory, and running several at once is slow without
// buying isolation we don't already have.
export default defineConfig({
  testDir: ".",
  testMatch: "*.spec.js",
  workers: 1,
  fullyParallel: false,
  reporter: process.env.CI ? "list" : "line",
  // Launching Chrome and waiting for an MV3 service worker is slower than a
  // unit test but still seconds, not minutes.
  timeout: 60_000,
  expect: { timeout: 10_000 },
});
