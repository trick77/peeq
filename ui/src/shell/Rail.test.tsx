import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { Rail } from "./Rail";
import { CookieStatus } from "./CookieStatus";

function renderRail() {
  return renderToStaticMarkup(
    <Rail active="library" onNavigate={() => {}} pendingCount={0} />,
  );
}

describe("Rail", () => {
  it("renders every nav item", () => {
    const html = renderRail();
    expect(html).toContain("Library");
    expect(html).toContain("Now playing");
    expect(html).toContain("Search");
    // Anchored to the element boundaries: a bare toContain("Add") would also
    // pass for "Add a video", so it could not pin the rename.
    expect(html).toContain(">Add<");
    expect(html).not.toContain("Add a video");
    expect(html).toContain("Decide");
    expect(html).toContain("Queue");
    expect(html).toContain("Activity");
    expect(html).toContain("Settings");
  });

  it("marks the active view", () => {
    const html = renderRail();
    expect(html).toMatch(/rail-nav-item active"[^>]*>[\s\S]*?Library/);
  });
});

describe("CookieStatus", () => {
  it('shows "active" for status valid', () => {
    const html = renderToStaticMarkup(<CookieStatus status="valid" />);
    expect(html).toContain("active");
    expect(html).not.toContain("warn");
  });

  it("shows a warning for status absent", () => {
    const html = renderToStaticMarkup(<CookieStatus status="absent" />);
    expect(html).toContain("cookie-status warn");
  });
});
