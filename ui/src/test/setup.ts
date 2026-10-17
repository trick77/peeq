// vitest setupFiles entry (vite.config.ts `test.setupFiles`): jest-dom's matchers on vitest's `expect`. Testing
// Library's cleanup runs by itself between tests (test.globals).
import "@testing-library/jest-dom/vitest";

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
