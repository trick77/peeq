import { describe, expect, it, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { useRef, useState } from "react";
import { useMenuPlacement } from "./useMenuPlacement";

// jsdom reports every rect as zero and never lays anything out, so the
// geometry the hook reads is stubbed here rather than produced. The point of
// these tests is the decision — "is there room below?" — not the measuring.
type Geometry = {
  triggerTop: number;
  triggerBottom: number;
  menuHeight: number;
};

function Harness({ geometry }: { geometry: Geometry | null }) {
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const menuRef = useRef<HTMLDivElement | null>(null);
  const [open, setOpen] = useState(false);
  const place = useMenuPlacement(open, triggerRef, menuRef);

  function stamp(el: HTMLElement | null) {
    if (!el || !geometry) return;
    // The menu is measured before it is placed, so offsetHeight is what it
    // wants, not where it landed.
    Object.defineProperty(el, "offsetHeight", {
      configurable: true,
      value: geometry.menuHeight,
    });
  }

  return (
    <div>
      <button
        type="button"
        ref={(el) => {
          triggerRef.current = el;
          if (el && geometry) {
            el.getBoundingClientRect = () =>
              ({
                top: geometry.triggerTop,
                bottom: geometry.triggerBottom,
              }) as DOMRect;
          }
        }}
        onClick={() => setOpen(true)}
      >
        open
      </button>
      {open ? (
        <div
          data-testid="menu"
          data-place={place}
          ref={(el) => {
            menuRef.current = el;
            stamp(el);
          }}
        />
      ) : null}
    </div>
  );
}

function placement() {
  return screen.getByTestId("menu").getAttribute("data-place");
}

describe("useMenuPlacement", () => {
  const realHeight = window.innerHeight;

  beforeEach(() => {
    // An iPad in portrait, roughly.
    Object.defineProperty(window, "innerHeight", {
      configurable: true,
      value: 780,
    });
  });

  afterEach(() => {
    Object.defineProperty(window, "innerHeight", {
      configurable: true,
      value: realHeight,
    });
  });

  it("when the menu fits below the trigger_then it opens downward", () => {
    // Trigger near the top: 780 - 120 leaves ample room under it.
    render(
      <Harness
        geometry={{ triggerTop: 90, triggerBottom: 120, menuHeight: 190 }}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "open" }));
    expect(placement()).toBe("down");
  });

  it("when the trigger sits low on the screen_then the menu flips up", () => {
    // The reported bug: the action row at y≈670 on an iPad, a 190px menu, and
    // only 110px of screen left under it.
    render(
      <Harness
        geometry={{ triggerTop: 640, triggerBottom: 670, menuHeight: 190 }}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "open" }));
    expect(placement()).toBe("up");
  });

  it("when the menu outgrows the viewport_then it takes the roomier side", () => {
    // Nothing fits either way, so the max-height clamp will scroll it. Placing
    // it where there is more room still shows more rows: below the trigger
    // here, since it sits above the middle of the screen.
    render(
      <Harness
        geometry={{ triggerTop: 300, triggerBottom: 330, menuHeight: 900 }}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "open" }));
    expect(placement()).toBe("down");
  });

  it("when nothing can be measured_then it keeps the old downward behaviour", () => {
    // Plain jsdom: every rect is zero. This is the case every existing menu
    // test hits, and it must not move.
    render(<Harness geometry={null} />);
    fireEvent.click(screen.getByRole("button", { name: "open" }));
    expect(placement()).toBe("down");
  });
});
