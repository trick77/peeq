import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { TabBar } from "./TabBar";
import { MORE_ITEMS, TAB_ITEMS } from "./nav";

function renderBar(props: Partial<Parameters<typeof TabBar>[0]> = {}) {
  const onNavigate = vi.fn();
  render(<TabBar active="library" onNavigate={onNavigate} {...props} />);
  return { onNavigate };
}

describe("TabBar", () => {
  it("shows four destinations plus More", () => {
    renderBar();
    for (const item of TAB_ITEMS) {
      expect(screen.getByRole("button", { name: item.label })).toBeTruthy();
    }
    expect(screen.getByRole("button", { name: "More" })).toBeTruthy();
    // The five that did not fit are one tap deeper, not on the bar.
    expect(screen.queryByRole("button", { name: "Settings" })).toBeNull();
  });

  it("reaches the rest through More", () => {
    const { onNavigate } = renderBar();
    fireEvent.click(screen.getByRole("button", { name: "More" }));
    for (const item of MORE_ITEMS) {
      expect(screen.getByRole("button", { name: item.label })).toBeTruthy();
    }
    fireEvent.click(screen.getByRole("button", { name: "Settings" }));
    expect(onNavigate).toHaveBeenCalledWith("settings");
    // Choosing closes it: a sheet left open would cover the page it just
    // navigated to.
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("closes the sheet on Escape", () => {
    renderBar();
    fireEvent.click(screen.getByRole("button", { name: "More" }));
    expect(screen.getByRole("dialog")).toBeTruthy();
    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("lights More while the open page lives inside it", () => {
    renderBar({ active: "settings" });
    expect(
      screen.getByRole("button", { name: "More" }).classList.contains("active"),
    ).toBe(true);
  });

  it("says a count the dot cannot", () => {
    renderBar({ pendingCount: 3 });
    expect(screen.getByRole("button", { name: "Inbox, 3" })).toBeTruthy();
  });

  it("shows no Up next count while nothing is running", () => {
    // Same rule the rail follows: a number that never falls reads as progress.
    renderBar({ upNextCount: 2, upNextLive: false });
    expect(screen.getByRole("button", { name: "Up next" })).toBeTruthy();
    renderBar({ upNextCount: 2, upNextLive: true });
    expect(screen.getByRole("button", { name: "Up next, 2" })).toBeTruthy();
  });
});
