import { describe, it, expect, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { RowMenu } from "./RowMenu";

function setup(overrides: Partial<Parameters<typeof RowMenu>[0]> = {}) {
  const onSub = vi.fn();
  const onDelete = vi.fn();
  render(
    <RowMenu
      label="Actions for X"
      actions={[
        { label: "Subscribe", icon: "star", onClick: onSub },
        { label: "Delete", icon: "trash", danger: true, onClick: onDelete },
      ]}
      {...overrides}
    />,
  );
  return { onSub, onDelete };
}

describe("RowMenu", () => {
  it("opens on the trigger and lists its actions", async () => {
    const user = userEvent.setup();
    setup();
    // Closed to start: no menu items in the DOM.
    expect(screen.queryByRole("menuitem")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Actions for X" }));
    expect(screen.getByRole("menuitem", { name: "Subscribe" })).toBeVisible();
    expect(screen.getByRole("menuitem", { name: "Delete" })).toBeVisible();
  });

  it("running an action calls its handler and closes the menu", async () => {
    const user = userEvent.setup();
    const { onSub } = setup();
    await user.click(screen.getByRole("button", { name: "Actions for X" }));
    await user.click(screen.getByRole("menuitem", { name: "Subscribe" }));
    expect(onSub).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(screen.queryByRole("menuitem")).not.toBeInTheDocument(),
    );
  });

  it("marks a danger action so it can be styled destructively", async () => {
    const user = userEvent.setup();
    setup();
    await user.click(screen.getByRole("button", { name: "Actions for X" }));
    expect(screen.getByRole("menuitem", { name: "Delete" })).toHaveClass(
      "danger",
    );
  });

  it("Escape closes the menu and returns focus to the trigger", async () => {
    const user = userEvent.setup();
    setup();
    const trigger = screen.getByRole("button", { name: "Actions for X" });
    await user.click(trigger);
    await user.keyboard("{Escape}");
    await waitFor(() =>
      expect(screen.queryByRole("menuitem")).not.toBeInTheDocument(),
    );
    expect(trigger).toHaveFocus();
  });

  it("an outside click closes the menu", async () => {
    const user = userEvent.setup();
    setup();
    await user.click(screen.getByRole("button", { name: "Actions for X" }));
    expect(screen.getByRole("menuitem", { name: "Subscribe" })).toBeVisible();
    await user.click(document.body);
    await waitFor(() =>
      expect(screen.queryByRole("menuitem")).not.toBeInTheDocument(),
    );
  });

  it("arrow keys move focus between items", async () => {
    const user = userEvent.setup();
    setup();
    await user.click(screen.getByRole("button", { name: "Actions for X" }));
    // Opens focused on the first item.
    expect(screen.getByRole("menuitem", { name: "Subscribe" })).toHaveFocus();
    await user.keyboard("{ArrowDown}");
    expect(screen.getByRole("menuitem", { name: "Delete" })).toHaveFocus();
    // Wraps back to the first on the next ArrowDown.
    await user.keyboard("{ArrowDown}");
    expect(screen.getByRole("menuitem", { name: "Subscribe" })).toHaveFocus();
    // ArrowUp wraps to the last.
    await user.keyboard("{ArrowUp}");
    expect(screen.getByRole("menuitem", { name: "Delete" })).toHaveFocus();
  });
});
