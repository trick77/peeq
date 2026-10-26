import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SubcategoryRow } from "./SubcategoryRow";
import type { Subcategory } from "../../api/types";

vi.mock("../../api/channels", () => ({
  listSubcategories: vi.fn(),
  setChannelSubcategories: vi.fn(),
}));
import { listSubcategories, setChannelSubcategories } from "../../api/channels";

const sub = (
  id: string,
  label: string,
  category: string,
  category_label: string,
): Subcategory => ({
  id,
  label,
  category,
  category_label,
});
const cycling = sub("sports/cycling", "Cycling", "sports", "Sports & Fitness");
const running = sub("sports/running", "Running", "sports", "Sports & Fitness");
const wearables = sub(
  "tech/wearables",
  "Wearables",
  "tech",
  "Technology & Gadgets",
);

describe("SubcategoryRow", () => {
  beforeEach(() => {
    vi.mocked(listSubcategories)
      .mockReset()
      .mockResolvedValue([cycling, running, wearables]);
    vi.mocked(setChannelSubcategories)
      .mockReset()
      .mockResolvedValue({ subcategories: [], subcategories_manual: true });
  });

  it("shows the labels and who set them", () => {
    render(
      <SubcategoryRow
        channelId="UC1"
        subcategories={[cycling, wearables]}
        manual={false}
        onSaved={() => {}}
      />,
    );
    expect(screen.getByText("Cycling, Wearables")).toBeInTheDocument();
    expect(screen.getByText("by peeq")).toBeInTheDocument();
  });

  it("says when there are none yet", () => {
    render(
      <SubcategoryRow
        channelId="UC1"
        subcategories={[]}
        manual={false}
        onSaved={() => {}}
      />,
    );
    expect(screen.getByText("None yet")).toBeInTheDocument();
    expect(screen.queryByText("by peeq")).not.toBeInTheDocument();
  });

  it("edits in place: primary first, empty slots dropped", async () => {
    const onSaved = vi.fn();
    render(
      <SubcategoryRow
        channelId="UC1"
        subcategories={[cycling]}
        manual={false}
        onSaved={onSaved}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    const second = await screen.findByLabelText("Subcategory 2");
    // One option per slot: wait until the vocabulary has filled them.
    await screen.findAllByRole("option", { name: "Wearables", hidden: true });
    await userEvent.selectOptions(second, "tech/wearables");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(setChannelSubcategories).toHaveBeenCalledWith("UC1", [
      "sports/cycling",
      "tech/wearables",
    ]);
    expect(onSaved).toHaveBeenCalled();
    // A peeq-set channel has nothing to hand back.
    expect(
      screen.queryByRole("button", { name: "Let peeq decide" }),
    ).not.toBeInTheDocument();
  });

  it("refuses a repeated pick", async () => {
    render(
      <SubcategoryRow
        channelId="UC1"
        subcategories={[cycling]}
        manual={false}
        onSaved={() => {}}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    const second = await screen.findByLabelText("Subcategory 2");
    await screen.findAllByRole("option", { name: "Running", hidden: true });
    await userEvent.selectOptions(second, "sports/cycling");
    expect(screen.getByText("Each subcategory once.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("hands a manual pick back to peeq with an empty list", async () => {
    const onSaved = vi.fn();
    render(
      <SubcategoryRow
        channelId="UC1"
        subcategories={[running]}
        manual={true}
        onSaved={onSaved}
      />,
    );
    expect(screen.getByText("set by you")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Edit" }));
    await userEvent.click(
      await screen.findByRole("button", { name: "Let peeq decide" }),
    );
    expect(setChannelSubcategories).toHaveBeenCalledWith("UC1", []);
    expect(onSaved).toHaveBeenCalled();
  });
});
