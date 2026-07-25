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

  it("fences the danger action off with a separator above it", async () => {
    const user = userEvent.setup();
    setup();
    await user.click(screen.getByRole("button", { name: "Actions for X" }));
    const menu = screen.getByRole("menu");
    expect(
      Array.from(menu.children).map((el) => el.getAttribute("role")),
    ).toEqual(["menuitem", "separator", "menuitem"]);
  });

  it("omits the separator when there is no danger action", async () => {
    const user = userEvent.setup();
    setup({
      actions: [
        { label: "Subscribe", icon: "star", onClick: vi.fn() },
        { label: "Open", icon: "link", onClick: vi.fn() },
      ],
    });
    await user.click(screen.getByRole("button", { name: "Actions for X" }));
    expect(screen.queryByRole("separator")).not.toBeInTheDocument();
  });

  it("omits the separator when the danger action leads the menu", async () => {
    const user = userEvent.setup();
    setup({
      actions: [
        { label: "Delete", icon: "trash", danger: true, onClick: vi.fn() },
      ],
    });
    await user.click(screen.getByRole("button", { name: "Actions for X" }));
    expect(screen.queryByRole("separator")).not.toBeInTheDocument();
  });

  it("renders an href action as a link, and closes when it is followed", async () => {
    const user = userEvent.setup();
    const onPick = vi.fn();
    setup({
      actions: [
        {
          label: "Download file",
          icon: "download",
          href: "#file.mp4",
          download: true,
          flag: "failed",
          onClick: onPick,
        },
      ],
    });
    await user.click(screen.getByRole("button", { name: "Actions for X" }));
    const item = screen.getByRole("menuitem", { name: /Download file/ });
    expect(item.tagName).toBe("A");
    expect(item).toHaveAttribute("href", "#file.mp4");
    expect(item).toHaveAttribute("download");
    expect(item).toHaveTextContent("failed");
    await user.click(item);
    expect(onPick).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(screen.queryByRole("menuitem")).not.toBeInTheDocument(),
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
