import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Pending } from "./Pending";
import type { PendingItem } from "../api/types";

function baseItem(overrides: Partial<PendingItem> = {}): PendingItem {
  return {
    video_id: "v1",
    channel_id: "c1",
    channel_name: "Channel One",
    title: "First pending video",
    duration_seconds: 125,
    url: "https://youtube.com/watch?v=v1",
    thumbnail_url: "https://img.example/v1.jpg",
    ...overrides,
  };
}

const itemA = baseItem();
const itemB = baseItem({
  video_id: "v2",
  channel_id: "c2",
  channel_name: "Channel Two",
  title: "Second pending video",
  thumbnail_url: "https://img.example/v2.jpg",
});

vi.mock("../api/pending", () => ({
  listPending: vi.fn(),
  downloadPending: vi.fn(),
  ignorePending: vi.fn(),
}));

import { listPending, downloadPending, ignorePending } from "../api/pending";

describe("Pending", () => {
  beforeEach(() => {
    vi.mocked(listPending).mockReset();
    vi.mocked(downloadPending).mockReset();
    vi.mocked(ignorePending).mockReset();
    vi.mocked(listPending).mockResolvedValue([itemA, itemB]);
    vi.mocked(downloadPending).mockResolvedValue(undefined);
    vi.mocked(ignorePending).mockResolvedValue(undefined);
  });

  it("lists pending items with title and remote thumbnail", async () => {
    render(<Pending />);
    expect(await screen.findByText("First pending video")).toBeInTheDocument();
    expect(screen.getByText("Second pending video")).toBeInTheDocument();
    const imgs = document.querySelectorAll("img") as NodeListOf<HTMLImageElement>;
    expect(imgs).toHaveLength(2);
    expect(Array.from(imgs).map((i) => i.src)).toEqual(
      expect.arrayContaining(["https://img.example/v1.jpg", "https://img.example/v2.jpg"]),
    );
  });

  it("renders the channel name, not the raw channel id", async () => {
    render(<Pending />);
    await screen.findByText("First pending video");
    expect(screen.getByText("Channel One")).toBeInTheDocument();
    expect(screen.getByText("Channel Two")).toBeInTheDocument();
    expect(screen.queryByText("c1")).not.toBeInTheDocument();
    expect(screen.queryByText("c2")).not.toBeInTheDocument();
  });

  it("falls back to channel_id when channel_name is empty", async () => {
    vi.mocked(listPending).mockResolvedValue([baseItem({ channel_name: "" })]);
    render(<Pending />);
    expect(await screen.findByText("c1")).toBeInTheDocument();
  });

  it("calls onCountChange with the item count after initial load", async () => {
    const onCountChange = vi.fn();
    render(<Pending onCountChange={onCountChange} />);
    await screen.findByText("First pending video");
    await waitFor(() => {
      expect(onCountChange).toHaveBeenCalledWith(2);
    });
  });

  it("clicking Download now calls downloadPending and removes the row", async () => {
    const user = userEvent.setup();
    render(<Pending />);
    await screen.findByText("First pending video");
    const row = screen.getByText("First pending video").closest(".card") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /download now/i }));
    await waitFor(() => {
      expect(downloadPending).toHaveBeenCalledWith("v1");
    });
    await waitFor(() => {
      expect(screen.queryByText("First pending video")).not.toBeInTheDocument();
    });
    expect(screen.getByText("Second pending video")).toBeInTheDocument();
  });

  it("clicking Ignore calls ignorePending and removes the row", async () => {
    const user = userEvent.setup();
    render(<Pending />);
    await screen.findByText("Second pending video");
    const row = screen.getByText("Second pending video").closest(".card") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /ignore/i }));
    await waitFor(() => {
      expect(ignorePending).toHaveBeenCalledWith("v2");
    });
    await waitFor(() => {
      expect(screen.queryByText("Second pending video")).not.toBeInTheDocument();
    });
    expect(screen.getByText("First pending video")).toBeInTheDocument();
  });

  it("calls onCountChange with the decremented count after Download now removes a row", async () => {
    const user = userEvent.setup();
    const onCountChange = vi.fn();
    render(<Pending onCountChange={onCountChange} />);
    await screen.findByText("First pending video");
    onCountChange.mockClear();
    const row = screen.getByText("First pending video").closest(".card") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /download now/i }));
    await waitFor(() => {
      expect(onCountChange).toHaveBeenCalledWith(1);
    });
  });

  it("clicking a channel name opens its page", async () => {
    const user = userEvent.setup();
    const onOpenChannel = vi.fn();
    render(<Pending onOpenChannel={onOpenChannel} />);
    await screen.findByText("First pending video");

    await user.click(screen.getByRole("button", { name: "Channel One" }));

    expect(onOpenChannel).toHaveBeenCalledWith("c1");
  });

  it("renders the channel name as plain text (not a button) when onOpenChannel is absent", async () => {
    render(<Pending />);
    await screen.findByText("First pending video");
    expect(screen.getByText("Channel One").closest("button")).toBeNull();
  });

  it("calls onCountChange with the decremented count after Ignore removes a row", async () => {
    const user = userEvent.setup();
    const onCountChange = vi.fn();
    render(<Pending onCountChange={onCountChange} />);
    await screen.findByText("Second pending video");
    onCountChange.mockClear();
    const row = screen.getByText("Second pending video").closest(".card") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /ignore/i }));
    await waitFor(() => {
      expect(onCountChange).toHaveBeenCalledWith(1);
    });
  });
});

