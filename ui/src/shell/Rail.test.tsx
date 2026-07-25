import { describe, it, expect } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { Rail } from "./Rail";
import { CookieStatus } from "./CookieStatus";

function renderRail() {
  return renderToStaticMarkup(
    <Rail active="library" onNavigate={() => {}} pendingCount={0} />,
  );
}

// navItems parses the rendered rail into one entry per nav button. Assertions
// about a single item have to be made against that item's own element: the
// markup is one long string, so a regex tying a class to a label further along
// it happily matches across two different buttons.
function navItems(props: Parameters<typeof Rail>[0]) {
  const host = document.createElement("div");
  host.innerHTML = renderToStaticMarkup(<Rail {...props} />);
  return Array.from(host.querySelectorAll("button.rail-nav-item")).map((b) => ({
    label: (b.textContent ?? "").replace(/\d+$/, "").trim(),
    classes: Array.from(b.classList),
    pill: b.querySelector(".rail-nav-count"),
  }));
}

function navItem(props: Parameters<typeof Rail>[0], label: string) {
  const item = navItems(props).find((i) => i.label === label);
  if (!item) throw new Error(`no rail item labelled ${label}`);
  return item;
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
    expect(html).toContain("Inbox");
    expect(html).toContain("Queue");
    expect(html).toContain("Activity");
    expect(html).toContain("Settings");
  });

  it("marks the active view", () => {
    const html = renderRail();
    expect(html).toMatch(/rail-nav-item active"[^>]*>[\s\S]*?Library/);
  });

  it("puts Channels directly under Library", () => {
    const labels = navItems({
      active: "library",
      onNavigate: () => {},
    }).map((i) => i.label);
    expect(labels.slice(0, 2)).toEqual(["Library", "Channels"]);
  });

  // The rail greys Inbox/Queue out when there is genuinely nothing in them, so
  // the counts double as a to-do signal. `undefined` is NOT nothing — it means
  // the first fetch hasn't landed — or every cold load would flash dim.
  describe("idle state", () => {
    it("dims Queue and hides its pill when the queue is empty", () => {
      const props = {
        active: "library" as const,
        onNavigate: () => {},
        pendingCount: 3,
        queueCount: 0,
      };
      const queue = navItem(props, "Queue");
      expect(queue.classes).toContain("idle");
      expect(queue.pill).toBeNull();
      // Inbox, which has work waiting, is untouched.
      expect(navItem(props, "Inbox").classes).not.toContain("idle");
    });

    it("gives a non-empty Queue the same accent pill as Inbox", () => {
      const props = {
        active: "library" as const,
        onNavigate: () => {},
        pendingCount: 4,
        queueCount: 2,
      };
      const queue = navItem(props, "Queue");
      expect(queue.classes).not.toContain("idle");
      expect(queue.pill?.className).toBe("rail-nav-count hot");
      expect(queue.pill?.textContent).toBe("2");
      expect(navItem(props, "Inbox").pill?.className).toBe(
        "rail-nav-count hot",
      );
    });

    it("never dims the view you are already on", () => {
      const queue = navItem(
        {
          active: "queue",
          onNavigate: () => {},
          pendingCount: 0,
          queueCount: 0,
        },
        "Queue",
      );
      expect(queue.classes).toContain("active");
      expect(queue.classes).not.toContain("idle");
    });

    it("does not dim counts that have not loaded yet", () => {
      const items = navItems({ active: "library", onNavigate: () => {} });
      expect(items.some((i) => i.classes.includes("idle"))).toBe(false);
    });
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
