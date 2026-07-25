import { describe, it, expect } from "vitest";
import { DOT } from "./sep";

describe("DOT", () => {
  // The other assertions that use DOT interpolate it, so they would still pass
  // if it silently reverted to plain spaces. This one pins the actual spacing:
  // en spaces (U+2002) render ~6px at the sizes these lines use, matching the
  // `gap: 6px` the .dot spans get in .card .by / .playmeta .by / .chan-handle.
  it("separates with en spaces, not plain ones", () => {
    expect(DOT).toBe("\u2002\u00b7\u2002");
  });
});
