import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { InDepthCard, InDepthBody } from "./InDepthCard";

const TEXT =
  "Does it work? Yes, at 45 °C.\n\n### Flow temperature decides it [4:12]\n\nEvery degree counts.";

describe("InDepthCard", () => {
  it("rests shut with a read time, and opens to the text", async () => {
    render(<InDepthCard text={TEXT} seek={() => {}} />);
    const header = screen.getByRole("button", { name: /In depth/ });
    expect(header).toHaveAttribute("aria-expanded", "false");
    expect(header).toHaveTextContent("1 min read");
    expect(screen.queryByText("Every degree counts.")).toBeNull();

    await userEvent.click(header);
    expect(header).toHaveAttribute("aria-expanded", "true");
    expect(header).not.toHaveTextContent("min read");
    expect(
      screen.getByText("Does it work? Yes, at 45 °C."),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: /Flow temperature decides it/ }),
    ).toBeInTheDocument();
  });

  it("seeks to a section's stamp", async () => {
    const seek = vi.fn();
    render(<InDepthCard text={TEXT} seek={seek} />);
    await userEvent.click(screen.getByRole("button", { name: /In depth/ }));
    await userEvent.click(screen.getByRole("button", { name: "4:12" }));
    expect(seek).toHaveBeenCalledWith(252);
  });
});

describe("InDepthBody", () => {
  // No video to move: the stamp is text, not a button that does nothing.
  it("renders stamps as text without a seek", () => {
    render(<InDepthBody text={TEXT} />);
    expect(screen.getByText("4:12").tagName).toBe("SPAN");
    expect(screen.queryByRole("button")).toBeNull();
  });
});
