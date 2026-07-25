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
    expect(html).toContain("Up next");
    expect(html).toContain("History");
    expect(html).toContain("Settings");
    // The two pages Up next and History replaced are gone from the rail, not
    // merely relabelled somewhere further down it.
    expect(html).not.toContain(">Queue<");
    expect(html).not.toContain(">Activity<");
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

  // The rail greys Inbox/Up next out when there is genuinely nothing in them, so
  // the counts double as a to-do signal. `undefined` is NOT nothing — it means
  // the first fetch hasn't landed — or every cold load would flash dim.
  describe("idle state", () => {
    it("dims Up next and hides its pill when there is no work", () => {
      const props = {
        active: "library" as const,
        onNavigate: () => {},
        pendingCount: 3,
        upNextCount: 0,
        upNextLive: false,
      };
      const upnext = navItem(props, "Up next");
      expect(upnext.classes).toContain("idle");
      expect(upnext.pill).toBeNull();
      // Inbox, which has work waiting, is untouched.
      expect(navItem(props, "Inbox").classes).not.toContain("idle");
    });

    it("gives a running Up next the same accent pill as Inbox", () => {
      const props = {
        active: "library" as const,
        onNavigate: () => {},
        pendingCount: 4,
        upNextCount: 2,
        upNextLive: true,
      };
      const upnext = navItem(props, "Up next");
      expect(upnext.classes).not.toContain("idle");
      expect(upnext.pill?.className).toBe("rail-nav-count hot");
      expect(upnext.pill?.textContent).toBe("2");
      expect(navItem(props, "Inbox").pill?.className).toBe(
        "rail-nav-count hot",
      );
    });

    // The pill is a "something is moving" light. Work that is queued but frozen
    // — everything paused, YouTube blocked — shows no number, because a count
    // that never falls reads as progress. The item still must not dim: there IS
    // work, it just isn't going anywhere, and the pause banner says why.
    it("hides the pill when work is waiting but nothing is running", () => {
      const upnext = navItem(
        {
          active: "library",
          onNavigate: () => {},
          pendingCount: 0,
          upNextCount: 3,
          upNextLive: false,
        },
        "Up next",
      );
      expect(upnext.pill).toBeNull();
      expect(upnext.classes).not.toContain("idle");
    });

    it("never dims the view you are already on", () => {
      const upnext = navItem(
        {
          active: "upnext",
          onNavigate: () => {},
          pendingCount: 0,
          upNextCount: 0,
          upNextLive: false,
        },
        "Up next",
      );
      expect(upnext.classes).toContain("active");
      expect(upnext.classes).not.toContain("idle");
    });

    // History is a log, never a to-do: it has no count and must never dim.
    it("leaves History undimmed and pill-less", () => {
      const history = navItem(
        {
          active: "library",
          onNavigate: () => {},
          pendingCount: 0,
          upNextCount: 0,
          upNextLive: false,
        },
        "History",
      );
      expect(history.classes).not.toContain("idle");
      expect(history.pill).toBeNull();
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
