import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { PillStrip } from "./PillStrip";

// jsdom computes no layout, so scrollWidth/clientWidth are 0 and the component
// would never show a chevron on its own. These helpers fake the geometry on the
// scroll element and fire a scroll event to re-run the edge calc — the only way
// to exercise the chevron/scroll paths headlessly.
function scrollEl() {
  return document.querySelector(".pillstrip-scroll") as HTMLElement;
}
function setGeometry(el: HTMLElement, scrollLeft: number) {
  Object.defineProperty(el, "clientWidth", { configurable: true, value: 100 });
  Object.defineProperty(el, "scrollWidth", { configurable: true, value: 300 });
  el.scrollLeft = scrollLeft;
  fireEvent.scroll(el);
}

describe("PillStrip", () => {
  it("shows no chevrons when the content fits", () => {
    render(
      <PillStrip>
        <div>pills</div>
      </PillStrip>,
    );
    // clientWidth/scrollWidth both 0 in jsdom -> nothing overflows.
    expect(
      screen.queryByRole("button", { name: /Scroll filters/ }),
    ).not.toBeInTheDocument();
  });

  it("offers only the right chevron at the start of an overflowing strip", () => {
    render(
      <PillStrip>
        <div>pills</div>
      </PillStrip>,
    );
    setGeometry(scrollEl(), 0);
    expect(
      screen.getByRole("button", { name: "Scroll filters right" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Scroll filters left" }),
    ).not.toBeInTheDocument();
  });

  it("offers only the left chevron once scrolled to the end", () => {
    render(
      <PillStrip>
        <div>pills</div>
      </PillStrip>,
    );
    setGeometry(scrollEl(), 200); // 200 + 100 >= 300: nothing more to the right
    expect(
      screen.getByRole("button", { name: "Scroll filters left" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Scroll filters right" }),
    ).not.toBeInTheDocument();
  });

  it("offers both chevrons in the middle and scrolls when clicked", () => {
    render(
      <PillStrip>
        <div>pills</div>
      </PillStrip>,
    );
    const el = scrollEl();
    const scrollBy = vi.fn();
    el.scrollBy = scrollBy;
    setGeometry(el, 50); // room on both sides

    const right = screen.getByRole("button", { name: "Scroll filters right" });
    const left = screen.getByRole("button", { name: "Scroll filters left" });
    expect(right).toBeInTheDocument();
    expect(left).toBeInTheDocument();

    fireEvent.click(right);
    expect(scrollBy).toHaveBeenLastCalledWith(
      expect.objectContaining({ left: 80, behavior: "smooth" }), // +0.8 * 100
    );
    fireEvent.click(left);
    expect(scrollBy).toHaveBeenLastCalledWith(
      expect.objectContaining({ left: -80, behavior: "smooth" }),
    );
  });
});
