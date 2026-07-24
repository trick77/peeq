import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ConfirmDialog } from "./ConfirmDialog";

function setup(overrides: Partial<Parameters<typeof ConfirmDialog>[0]> = {}) {
  const onConfirm = vi.fn();
  const onCancel = vi.fn();
  const props = {
    open: true,
    title: "Delete channel?",
    confirmLabel: "Delete channel",
    onConfirm,
    onCancel,
    ...overrides,
  };
  const utils = render(
    <ConfirmDialog {...props}>
      Delete <b>Some Channel</b> and its videos?
    </ConfirmDialog>,
  );
  return { onConfirm, onCancel, ...utils };
}

describe("ConfirmDialog", () => {
  it("renders nothing while closed", () => {
    setup({ open: false });
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("shows the title and body when open", () => {
    setup();
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveTextContent("Delete channel?");
    expect(dialog).toHaveTextContent("Delete Some Channel and its videos?");
  });

  it("the confirm button calls onConfirm", async () => {
    const user = userEvent.setup();
    const { onConfirm } = setup();
    await user.click(screen.getByRole("button", { name: "Delete channel" }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it("Cancel calls onCancel", async () => {
    const user = userEvent.setup();
    const { onCancel } = setup();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("Escape cancels", async () => {
    const user = userEvent.setup();
    const { onCancel } = setup();
    await user.keyboard("{Escape}");
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("a click on the backdrop cancels, but a click inside the panel does not", async () => {
    const user = userEvent.setup();
    const { onCancel } = setup();
    // Clicking the panel (via its title) must not cancel.
    await user.click(screen.getByText("Delete channel?"));
    expect(onCancel).not.toHaveBeenCalled();
    // Clicking the overlay outside the panel cancels. The overlay is the
    // dialog's parent; press on it directly.
    const overlay = screen.getByRole("dialog").parentElement as HTMLElement;
    await user.pointer({ target: overlay, keys: "[MouseLeft]" });
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("busy disables the confirm button but leaves Cancel available", () => {
    setup({ busy: true });
    expect(
      screen.getByRole("button", { name: "Delete channel" }),
    ).toBeDisabled();
    // Cancel stays enabled: a user must always be able to back out.
    expect(screen.getByRole("button", { name: "Cancel" })).toBeEnabled();
  });
});
