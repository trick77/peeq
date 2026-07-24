import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Inbox } from "./Inbox";
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
    published_at: "2026-07-20",
    discovered_at: "2026-07-21 09:00:00",
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

describe("Inbox", () => {
  beforeEach(() => {
    vi.mocked(listPending).mockReset();
    vi.mocked(downloadPending).mockReset();
    vi.mocked(ignorePending).mockReset();
    vi.mocked(listPending).mockResolvedValue([itemA, itemB]);
    vi.mocked(downloadPending).mockResolvedValue(undefined);
    vi.mocked(ignorePending).mockResolvedValue(undefined);
  });

  it("lists pending items with title and remote thumbnail", async () => {
    render(<Inbox />);
    expect(await screen.findByText("First pending video")).toBeInTheDocument();
    expect(screen.getByText("Second pending video")).toBeInTheDocument();
    const imgs = document.querySelectorAll(
      "img",
    ) as NodeListOf<HTMLImageElement>;
    expect(imgs).toHaveLength(2);
    expect(Array.from(imgs).map((i) => i.src)).toEqual(
      expect.arrayContaining([
        "https://img.example/v1.jpg",
        "https://img.example/v2.jpg",
      ]),
    );
  });

  it("renders the channel name, not the raw channel id", async () => {
    render(<Inbox />);
    await screen.findByText("First pending video");
    // Channel names appear both as a chip and as the card byline, so scope the
    // assertion to a card byline rather than expecting exactly one match.
    const cardA = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;
    expect(within(cardA).getByText("Channel One")).toBeInTheDocument();
    expect(within(cardA).queryByText("c1")).not.toBeInTheDocument();
  });

  it("falls back to channel_id when channel_name is empty", async () => {
    vi.mocked(listPending).mockResolvedValue([baseItem({ channel_name: "" })]);
    render(<Inbox />);
    // One channel → no chips row; the id shows on the card byline.
    const card = (await screen.findByText("First pending video")).closest(
      ".card",
    ) as HTMLElement;
    expect(within(card).getByText("c1")).toBeInTheDocument();
  });

  it("calls onCountChange with the item count after initial load", async () => {
    const onCountChange = vi.fn();
    render(<Inbox onCountChange={onCountChange} />);
    await screen.findByText("First pending video");
    await waitFor(() => {
      expect(onCountChange).toHaveBeenCalledWith(2);
    });
  });

  it("clicking Download now calls downloadPending and removes the row", async () => {
    const user = userEvent.setup();
    render(<Inbox />);
    await screen.findByText("First pending video");
    const row = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;
    await user.click(
      within(row).getByRole("button", { name: /download now/i }),
    );
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
    render(<Inbox />);
    await screen.findByText("Second pending video");
    const row = screen
      .getByText("Second pending video")
      .closest(".card") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /ignore/i }));
    await waitFor(() => {
      expect(ignorePending).toHaveBeenCalledWith("v2");
    });
    await waitFor(() => {
      expect(
        screen.queryByText("Second pending video"),
      ).not.toBeInTheDocument();
    });
    expect(screen.getByText("First pending video")).toBeInTheDocument();
  });

  it("calls onCountChange with the decremented count after Download now removes a row", async () => {
    const user = userEvent.setup();
    const onCountChange = vi.fn();
    render(<Inbox onCountChange={onCountChange} />);
    await screen.findByText("First pending video");
    onCountChange.mockClear();
    const row = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;
    await user.click(
      within(row).getByRole("button", { name: /download now/i }),
    );
    await waitFor(() => {
      expect(onCountChange).toHaveBeenCalledWith(1);
    });
  });

  it("clicking a channel name opens its page", async () => {
    const user = userEvent.setup();
    const onOpenChannel = vi.fn();
    render(<Inbox onOpenChannel={onOpenChannel} />);
    await screen.findByText("First pending video");

    const cardA = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;
    await user.click(
      within(cardA).getByRole("button", { name: "Channel One" }),
    );

    expect(onOpenChannel).toHaveBeenCalledWith("c1");
  });

  it("renders the channel byline as plain text (not a button) when onOpenChannel is absent", async () => {
    render(<Inbox />);
    await screen.findByText("First pending video");
    const cardA = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;
    const byline = cardA.querySelector(".by") as HTMLElement;
    expect(within(byline).queryByRole("button")).toBeNull();
  });

  it("calls onCountChange with the decremented count after Ignore removes a row", async () => {
    const user = userEvent.setup();
    const onCountChange = vi.fn();
    render(<Inbox onCountChange={onCountChange} />);
    await screen.findByText("Second pending video");
    onCountChange.mockClear();
    const row = screen
      .getByText("Second pending video")
      .closest(".card") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /ignore/i }));
    await waitFor(() => {
      expect(onCountChange).toHaveBeenCalledWith(1);
    });
  });

  it("shows an empty-state line when the inbox is empty", async () => {
    vi.mocked(listPending).mockResolvedValue([]);
    render(<Inbox />);
    expect(await screen.findByText("Your inbox is empty.")).toBeInTheDocument();
  });

  it("filters the grid to a single channel via its chip", async () => {
    const user = userEvent.setup();
    render(<Inbox />);
    await screen.findByText("First pending video");

    // Two channels → chips appear. Click Channel Two's chip.
    await user.click(screen.getByRole("button", { name: /Channel Two/ }));

    expect(screen.getByText("Second pending video")).toBeInTheDocument();
    expect(screen.queryByText("First pending video")).not.toBeInTheDocument();
  });

  it("Download all queues every visible item one at a time", async () => {
    const user = userEvent.setup();
    render(<Inbox />);
    await screen.findByText("First pending video");

    await user.click(screen.getByRole("button", { name: /^download all$/i }));

    await waitFor(() => {
      expect(downloadPending).toHaveBeenCalledWith("v1");
      expect(downloadPending).toHaveBeenCalledWith("v2");
    });
    await waitFor(() => {
      expect(screen.queryByText("First pending video")).not.toBeInTheDocument();
      expect(
        screen.queryByText("Second pending video"),
      ).not.toBeInTheDocument();
    });
  });

  it("Download all confirms before a large batch", async () => {
    const user = userEvent.setup();
    const many = Array.from({ length: 12 }, (_, i) =>
      baseItem({
        video_id: `m${i}`,
        title: `Bulk video ${i}`,
        channel_id: "c1",
        channel_name: "Channel One",
      }),
    );
    vi.mocked(listPending).mockResolvedValue(many);
    render(<Inbox />);
    await screen.findByText("Bulk video 0");

    // First click only arms the confirm — nothing is downloaded yet.
    await user.click(screen.getByRole("button", { name: /^download all$/i }));
    expect(downloadPending).not.toHaveBeenCalled();
    const confirm = await screen.findByRole("button", { name: /confirm/i });

    // Second click runs it.
    await user.click(confirm);
    await waitFor(() => {
      expect(downloadPending).toHaveBeenCalledTimes(12);
    });
  });

  it("fires onQueued after a Download now so the queue can seed", async () => {
    const user = userEvent.setup();
    const onQueued = vi.fn();
    render(<Inbox onQueued={onQueued} />);
    await screen.findByText("First pending video");
    const row = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;
    await user.click(
      within(row).getByRole("button", { name: /download now/i }),
    );
    await waitFor(() => expect(onQueued).toHaveBeenCalled());
  });

  it("fires onQueued once after a Download all batch", async () => {
    const user = userEvent.setup();
    const onQueued = vi.fn();
    render(<Inbox onQueued={onQueued} />);
    await screen.findByText("First pending video");
    await user.click(screen.getByRole("button", { name: /^download all$/i }));
    await waitFor(() => expect(downloadPending).toHaveBeenCalledTimes(2));
    expect(onQueued).toHaveBeenCalledTimes(1);
  });

  it("does not fire onQueued when the very first bulk item fails", async () => {
    const user = userEvent.setup();
    const onQueued = vi.fn();
    vi.mocked(downloadPending).mockRejectedValue(new Error("cookie required"));
    render(<Inbox onQueued={onQueued} />);
    await screen.findByText("First pending video");
    await user.click(screen.getByRole("button", { name: /^download all$/i }));
    expect(await screen.findByText("cookie required")).toBeInTheDocument();
    expect(onQueued).not.toHaveBeenCalled();
  });

  it("surfaces a failure from Download now and keeps the row", async () => {
    const user = userEvent.setup();
    vi.mocked(downloadPending).mockRejectedValue(new Error("disk is full"));
    render(<Inbox />);
    await screen.findByText("First pending video");
    const row = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;
    await user.click(
      within(row).getByRole("button", { name: /download now/i }),
    );

    expect(await screen.findByText("disk is full")).toBeInTheDocument();
    // The row stays put — a failed download is still a decision to make.
    expect(screen.getByText("First pending video")).toBeInTheDocument();
  });

  it("stops a bulk download at the first failure", async () => {
    const user = userEvent.setup();
    vi.mocked(downloadPending)
      .mockResolvedValueOnce(undefined)
      .mockRejectedValueOnce(new Error("cookie required"));
    render(<Inbox />);
    await screen.findByText("First pending video");

    await user.click(screen.getByRole("button", { name: /^download all$/i }));

    expect(await screen.findByText("cookie required")).toBeInTheDocument();
    // First succeeded and left; the one that failed stays behind.
    await waitFor(() => {
      expect(screen.queryByText("First pending video")).not.toBeInTheDocument();
    });
    expect(screen.getByText("Second pending video")).toBeInTheDocument();
  });

  describe("card date", () => {
    // formatAgo is relative to now, so the fixture dates are derived from the
    // real clock rather than pinning it: fake timers deadlock testing-library's
    // async polling, and a hard-coded date would rot into a different age.
    function daysAgoISO(n: number): string {
      return new Date(Date.now() - n * 86400000).toISOString().slice(0, 10);
    }

    it("renders the publish date in the byline, like the library card", async () => {
      vi.mocked(listPending).mockResolvedValue([
        baseItem({ published_at: daysAgoISO(4) }),
      ]);
      render(<Inbox />);
      await screen.findByText("First pending video");
      const cardA = screen
        .getByText("First pending video")
        .closest(".card") as HTMLElement;
      const by = cardA.querySelector(".by") as HTMLElement;
      expect(by.textContent).toContain("Channel One");
      expect(by.textContent).toContain("4 days ago");
      expect(by.querySelector(".dot")).toBeInTheDocument();
    });

    it("omits the date rather than showing discovered_at when unpublished", async () => {
      vi.mocked(listPending).mockResolvedValue([
        baseItem({ published_at: undefined, discovered_at: daysAgoISO(23) }),
      ]);
      render(<Inbox />);
      await screen.findByText("First pending video");
      const by = document.querySelector(".card .by") as HTMLElement;
      expect(by.textContent).toContain("Channel One");
      // The item was discovered 23 days ago, but a discovery date must NOT be
      // rendered as a publish date — the whole reason the fields stay apart.
      expect(by.textContent).not.toContain("ago");
      expect(by.querySelector(".dot")).not.toBeInTheDocument();
    });
  });

  describe("sort", () => {
    const older = baseItem({
      video_id: "v3",
      title: "Older video",
      duration_seconds: 10,
      published_at: "2026-07-01",
    });
    const newer = baseItem({
      video_id: "v4",
      title: "Anewer video",
      duration_seconds: 9000,
      published_at: "2026-07-23",
    });

    function titles(): string[] {
      return Array.from(document.querySelectorAll(".card h3")).map(
        (h) => h.textContent ?? "",
      );
    }

    it("defaults to newest first", async () => {
      vi.mocked(listPending).mockResolvedValue([older, newer]);
      render(<Inbox />);
      await screen.findByText("Older video");
      expect(titles()).toEqual(["Anewer video", "Older video"]);
    });

    it("reorders the grid when a different order is picked", async () => {
      const user = userEvent.setup();
      vi.mocked(listPending).mockResolvedValue([older, newer]);
      render(<Inbox />);
      await screen.findByText("Older video");

      await user.selectOptions(screen.getByLabelText("Sort"), "oldest");
      expect(titles()).toEqual(["Older video", "Anewer video"]);

      await user.selectOptions(screen.getByLabelText("Sort"), "longest");
      expect(titles()).toEqual(["Anewer video", "Older video"]);

      // Title order is independent of both date and duration, so it proves
      // the select is really driving the comparator.
      await user.selectOptions(screen.getByLabelText("Sort"), "title");
      expect(titles()).toEqual(["Anewer video", "Older video"]);
    });

    it("orders a dateless item by its discovery day", async () => {
      // A row the scanner has not healed yet must still land sensibly rather
      // than sinking below everything.
      vi.mocked(listPending).mockResolvedValue([
        older,
        baseItem({
          video_id: "v5",
          title: "Undated video",
          published_at: undefined,
          discovered_at: "2026-07-24 08:00:00",
        }),
      ]);
      render(<Inbox />);
      await screen.findByText("Undated video");
      expect(titles()).toEqual(["Undated video", "Older video"]);
    });
  });
});
