// test-setup.ts — vitest setupFiles entry (see vite.config.ts `test.setupFiles`).
// Wires jest-dom's extra matchers (toBeInTheDocument, etc.) into vitest's
// `expect`, and cleans up the DOM between tests so components mounted by
// one test don't leak into the next.
import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup } from "@testing-library/react";

// jsdom ships no ResizeObserver, which PillStrip constructs on mount. A no-op
// stub is enough: jsdom computes no layout, so the observer would never fire a
// meaningful callback anyway — the component just needs the constructor to
// exist so it can mount.
if (!("ResizeObserver" in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}

afterEach(() => {
  cleanup();
});
