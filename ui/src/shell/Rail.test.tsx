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

  // No rail item ever dims. Emptiness is said by the missing count pill and by
  // the page's own empty state; the item itself always reads at Channels'
  // strength, so a destination never changes weight under you.
  describe("count pill", () => {
    it("hides Up next's pill when there is no work, without dimming it", () => {
      const props = {
        active: "library" as const,
        onNavigate: () => {},
        pendingCount: 3,
        upNextCount: 0,
        upNextLive: false,
      };
      const upnext = navItem(props, "Up next");
      expect(upnext.classes).not.toContain("idle");
      expect(upnext.pill).toBeNull();
      // Inbox, which has work waiting, keeps its pill.
      expect(navItem(props, "Inbox").pill?.textContent).toBe("3");
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
    // that never falls reads as progress. The pause banner says why.
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

    it("marks the view you are already on as active", () => {
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

    // History is a log, never a to-do: it has no count.
    it("leaves History pill-less", () => {
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

    it("never dims any item, whatever the counts", () => {
      const items = navItems({
        active: "library",
        onNavigate: () => {},
        pendingCount: 0,
        upNextCount: 0,
        upNextLive: false,
      });
      expect(items.some((i) => i.classes.includes("idle"))).toBe(false);
    });
  });

  describe("collapsed", () => {
    function collapsedRail(props: Partial<Parameters<typeof Rail>[0]> = {}) {
      const host = document.createElement("div");
      host.innerHTML = renderToStaticMarkup(
        <Rail
          active="library"
          onNavigate={() => {}}
          collapsed
          onToggleCollapsed={() => {}}
          {...props}
        />,
      );
      return host;
    }

    it("drops the wordmark and the labels, keeping an accessible name", () => {
      const host = collapsedRail();
      expect(host.querySelector(".rail-brand b")).toBeNull();
      const inbox = Array.from(
        host.querySelectorAll("button.rail-nav-item"),
      ).find((b) => b.getAttribute("aria-label") === "Inbox");
      expect(inbox).toBeDefined();
      // The label is gone from the text, not merely hidden by CSS — so the
      // aria-label above is the only thing naming the button.
      expect(inbox?.textContent).toBe("");
      expect(host.textContent).not.toContain("Peeq");
    });

    it("keeps the wordmark and the labels when expanded", () => {
      const host = document.createElement("div");
      host.innerHTML = renderToStaticMarkup(
        <Rail
          active="library"
          onNavigate={() => {}}
          onToggleCollapsed={() => {}}
        />,
      );
      expect(host.querySelector(".rail-brand b")?.textContent).toBe("Peeq");
      expect(host.textContent).toContain("Inbox");
    });

    it("says a count it cannot show as a number", () => {
      const host = collapsedRail({ pendingCount: 3 });
      const inbox = Array.from(
        host.querySelectorAll("button.rail-nav-item"),
      ).find((b) => (b.getAttribute("aria-label") ?? "").startsWith("Inbox"));
      // The pill has nowhere to go beside a centred icon, so the dot carries
      // "something is in there" and the aria-label carries how much.
      expect(inbox?.getAttribute("aria-label")).toBe("Inbox, 3");
      expect(inbox?.querySelector(".rail-nav-count")).toBeNull();
      expect(inbox?.querySelector(".rail-nav-dot")).not.toBeNull();
    });

    it("hides the status dock, which does not fit", () => {
      const host = collapsedRail({ cookieStatus: "valid" });
      expect(host.querySelector(".cookie-status")).toBeNull();
    });

    it("reports its state on the toggle", () => {
      expect(
        collapsedRail()
          .querySelector(".rail-collapse")
          ?.getAttribute("aria-expanded"),
      ).toBe("false");
      const host = document.createElement("div");
      host.innerHTML = renderToStaticMarkup(
        <Rail
          active="library"
          onNavigate={() => {}}
          onToggleCollapsed={() => {}}
        />,
      );
      const toggle = host.querySelector(".rail-collapse");
      expect(toggle?.getAttribute("aria-expanded")).toBe("true");
      expect(toggle?.getAttribute("aria-label")).toBe("Hide sidebar");
    });

    it("leaves the active item's class untouched", () => {
      // Collapsed styling hangs off .rail.collapsed, never off a modifier on
      // the item — App and the tests both read this class as the active marker.
      const active = collapsedRail().querySelector(
        "button.rail-nav-item.active",
      );
      expect(Array.from(active?.classList ?? [])).toEqual([
        "rail-nav-item",
        "active",
      ]);
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

describe("Rail yt-dlp indicator", () => {
  function railWith(ytdlp?: Parameters<typeof Rail>[0]["ytdlp"]) {
    return renderToStaticMarkup(
      <Rail
        active="library"
        onNavigate={() => {}}
        pendingCount={0}
        ytdlp={ytdlp}
      />,
    );
  }

  it("names both versions when an update is waiting", () => {
    const html = railWith({
      version: "2026.07.01",
      latest: "2026.08.15",
      update_available: true,
    });
    expect(html).toContain("ytdlp-status warn");
    expect(html).toContain("update available");
    // Both halves matter: "an update exists" without saying from what is not
    // actionable, and the pair is what makes the size of the gap obvious.
    expect(html).toContain("2026.07.01");
    expect(html).toContain("2026.08.15");
  });

  it("stays silent when the installed version is current", () => {
    const html = railWith({
      version: "2026.08.15",
      latest: "2026.08.15",
      update_available: false,
    });
    expect(html).not.toContain("ytdlp-status");
  });

  // Before the fetch resolves the rail knows nothing, and must not flash an
  // indicator on every page load the way an optimistic default would.
  it("stays silent before the version has loaded", () => {
    expect(railWith(undefined)).not.toContain("ytdlp-status");
  });

  // update_available with no latest release is a contradiction the row cannot
  // render honestly — it would print "2026.07.01 → " with a dangling arrow.
  // Suppress the row rather than show half a comparison.
  it("stays silent when the latest release is unknown", () => {
    const html = railWith({ version: "2026.07.01", update_available: true });
    expect(html).not.toContain("ytdlp-status");
  });
});
