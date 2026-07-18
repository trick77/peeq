// test-setup.ts — vitest setupFiles entry (see vite.config.ts `test.setupFiles`).
// Wires jest-dom's extra matchers (toBeInTheDocument, etc.) into vitest's
// `expect`, and cleans up the DOM between tests so components mounted by
// one test don't leak into the next.
import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

afterEach(() => {
  cleanup();
});
