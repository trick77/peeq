import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { App } from "./App";

describe("App", () => {
  it("renders peeq", () => {
    const html = renderToStaticMarkup(<App />);
    expect(html).toContain("peeq");
  });
});
