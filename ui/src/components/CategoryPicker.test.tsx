import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { CategoryPicker } from "./CategoryPicker";
import { CATEGORIES, UNCATEGORIZED } from "../categories";

describe("CategoryPicker", () => {
  it("shows the current category and offers every real one", () => {
    render(<CategoryPicker category="ai" onPick={vi.fn()} />);

    const trigger = screen.getByRole("button", {
      name: /Category: AI/,
    });
    fireEvent.click(trigger);

    const items = screen.getAllByRole("menuitemradio");
    expect(items).toHaveLength(CATEGORIES.length - 1);
    // Uncategorized is a state the app assigns, never one to choose.
    expect(
      items.some((i) => i.textContent?.toLowerCase().includes("uncategorized")),
    ).toBe(false);
    expect(
      items.find((i) => i.textContent === "AI")?.getAttribute("aria-checked"),
    ).toBe("true");
  });

  it("reports the picked category and closes", () => {
    const onPick = vi.fn();
    render(<CategoryPicker category="ai" onPick={onPick} />);

    fireEvent.click(screen.getByRole("button", { name: /Category: AI/ }));
    fireEvent.click(screen.getByRole("menuitemradio", { name: /Gaming/ }));

    expect(onPick).toHaveBeenCalledWith("gaming");
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("does not write when the current category is re-picked", () => {
    const onPick = vi.fn();
    render(<CategoryPicker category="gaming" onPick={onPick} />);

    fireEvent.click(screen.getByRole("button", { name: /Category: Gaming/ }));
    fireEvent.click(screen.getByRole("menuitemradio", { name: /Gaming/ }));

    expect(onPick).not.toHaveBeenCalled();
  });

  // The Library card hides an unset category; the Player must not, because it
  // is the only place a no-transcript video can ever get one.
  it("stays visible and pickable when the video is uncategorized", () => {
    const onPick = vi.fn();
    render(<CategoryPicker category={UNCATEGORIZED} onPick={onPick} />);

    const trigger = screen.getByRole("button", { name: /No category/ });
    expect(trigger.textContent).toContain("Uncategorized");

    fireEvent.click(trigger);
    fireEvent.click(screen.getByRole("menuitemradio", { name: /Space/ }));
    expect(onPick).toHaveBeenCalledWith("space");
  });

  it("closes on Escape without picking anything", () => {
    const onPick = vi.fn();
    render(<CategoryPicker category="ai" onPick={onPick} />);

    fireEvent.click(screen.getByRole("button", { name: /Category: AI/ }));
    expect(screen.getByRole("menu")).toBeTruthy();

    fireEvent.keyDown(document, { key: "Escape" });

    expect(screen.queryByRole("menu")).toBeNull();
    expect(onPick).not.toHaveBeenCalled();
  });

  it("closes on an outside click", () => {
    render(
      <div>
        <CategoryPicker category="ai" onPick={vi.fn()} />
        <button type="button">elsewhere</button>
      </div>,
    );

    fireEvent.click(screen.getByRole("button", { name: /Category: AI/ }));
    fireEvent.mouseDown(screen.getByRole("button", { name: "elsewhere" }));

    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("moves through the menu with the arrow keys", () => {
    render(<CategoryPicker category="ai" onPick={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: /Category: AI/ }));

    // The checked row takes focus on open, so ArrowDown lands on the next one.
    const items = screen.getAllByRole("menuitemradio");
    expect(document.activeElement).toBe(items[0]);

    fireEvent.keyDown(screen.getByRole("menu"), { key: "ArrowDown" });
    expect(document.activeElement).toBe(items[1]);

    fireEvent.keyDown(screen.getByRole("menu"), { key: "ArrowUp" });
    expect(document.activeElement).toBe(items[0]);
  });

  it("renders an unknown category id as unset rather than blank", () => {
    render(<CategoryPicker category="from-a-newer-build" onPick={vi.fn()} />);

    expect(
      screen.getByRole("button", { name: /No category/ }).textContent,
    ).toContain("Uncategorized");
  });

  it("hands its menu a placement for the flip-up CSS to key on", () => {
    render(<CategoryPicker category="ai" onPick={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: /Category: AI/ }));
    // jsdom measures nothing, so the value is always the "down" fallback.
    // What is under test is that the attribute is wired at all.
    expect(screen.getByRole("menu")).toHaveAttribute("data-place", "down");
  });
});
