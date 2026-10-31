import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render } from "@testing-library/react";
import type { ComponentProps } from "react";
import { Rail } from "./Rail";

// Collapsing is not always the user's own click: App collapses the rail on its
// own when a trackpad is undocked from an iPad (TOUCH_QUERY flips). Whatever
// had focus then — the Search box mid-query, an Ask answer — must keep it.
// The rail used to pull focus onto its toggle on every collapse.

const props: ComponentProps<typeof Rail> = {
  active: "library",
  onNavigate: () => {},
  onToggleCollapsed: () => {},
  cookieStatus: "valid",
  ytdlp: { version: "1", latest: "2", update_available: true },
};

function setup() {
  const view = render(
    <>
      <Rail {...props} />
      <input aria-label="search" />
    </>,
  );
  const collapse = () =>
    view.rerender(
      <>
        <Rail {...props} collapsed />
        <input aria-label="search" />
      </>,
    );
  return { view, collapse };
}

const FOCUSABLE = "button, a[href], input, select, textarea, [tabindex]";

describe("Rail focus across a collapse", () => {
  afterEach(cleanup);

  it("leaves focus outside the rail where it is", () => {
    const { view, collapse } = setup();
    const search = view.getByLabelText("search");
    search.focus();
    collapse();
    expect(document.activeElement).toBe(search);
  });

  it("leaves focus on a destination where it is", () => {
    const { view, collapse } = setup();
    const library = view.getByRole("button", { name: "Library" });
    library.focus();
    collapse();
    // Same node, still focused: collapsing re-labels it, never remounts it.
    expect(library.isConnected).toBe(true);
    expect(document.activeElement).toBe(library);
  });

  it("keeps the toggle focused after it is used", () => {
    const { view, collapse } = setup();
    const toggle = view.getByRole("button", { name: "Collapse sidebar" });
    toggle.focus();
    collapse();
    expect(document.activeElement).toBe(toggle);
  });

  // Why the rail needs no focus rescue: nothing that can hold focus unmounts
  // when it collapses. A focusable added to the brand row or the foot (both
  // unmount) would strand keyboard focus on <body> — this fails first.
  it("unmounts nothing focusable when it collapses", () => {
    const { view, collapse } = setup();
    const aside = view.container.querySelector("aside.rail");
    const before = Array.from(aside?.querySelectorAll(FOCUSABLE) ?? []);
    expect(before.length).toBeGreaterThan(0);
    collapse();
    expect(before.filter((el) => !el.isConnected)).toEqual([]);
  });
});
