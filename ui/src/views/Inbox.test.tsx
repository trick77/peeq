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

  it("lists pending items with the thumbnail proxied through the backend", async () => {
    render(<Inbox />);
    expect(await screen.findByText("First pending video")).toBeInTheDocument();
    expect(screen.getByText("Second pending video")).toBeInTheDocument();
    const imgs = document.querySelectorAll(
      "img",
    ) as NodeListOf<HTMLImageElement>;
    expect(imgs).toHaveLength(2);
    // The card loads /api/pending/{id}/thumbnail, not the raw i.ytimg.com URL:
    // peeq fetches and caches the poster server-side so the browser never talks
    // to YouTube's CDN.
    expect(Array.from(imgs).map((i) => i.getAttribute("src"))).toEqual(
      expect.arrayContaining([
        "/api/pending/v1/thumbnail",
        "/api/pending/v2/thumbnail",
      ]),
    );
  });

  it("links the title to the video on YouTube, in a new tab", async () => {
    render(<Inbox />);
    const link = await screen.findByRole("link", {
      name: "First pending video",
    });
    expect(link).toHaveAttribute("href", "https://youtube.com/watch?v=v1");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });

  it("builds the YouTube link from the video id when the ledger has no url", async () => {
    vi.mocked(listPending).mockResolvedValue([baseItem({ url: "" })]);
    render(<Inbox />);
    const link = await screen.findByRole("link", {
      name: "First pending video",
    });
    expect(link).toHaveAttribute("href", "https://www.youtube.com/watch?v=v1");
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
    // Scope to the card byline (the chip row also shows the fallback id now).
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

  // The pair sits ON the poster, not under the title, and is always rendered
  // — no hover, no focus, no pointer of any kind involved in getting to it.
  // Library's .acts overlay reveals on hover and is therefore unreachable on a
  // touch screen; this asserts Inbox did not inherit that.
  it("puts Download and Ignore on the thumbnail, visible without a pointer", async () => {
    render(<Inbox />);
    await screen.findByText("First pending video");
    const card = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;

    const download = within(card).getByRole("button", { name: /^download$/i });
    const ignore = within(card).getByRole("button", { name: /ignore/i });

    expect(download.closest(".thumb")).not.toBeNull();
    expect(ignore.closest(".thumb")).not.toBeNull();
    expect(card.querySelector(".card-foot")).toBeNull();
  });

  // Ignore lost its visible label when it became a 32px square, so the only
  // thing naming it is aria-label. Without that it is an unlabelled button
  // that deletes the video.
  it("keeps Ignore named for assistive tech now that it is icon-only", async () => {
    render(<Inbox />);
    await screen.findByText("First pending video");
    const card = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;

    const ignore = within(card).getByRole("button", { name: "Ignore" });
    expect(ignore).toHaveAttribute("aria-label", "Ignore");
    expect(ignore.textContent?.trim()).toBe("");
  });

  it("clicking Download calls downloadPending and removes the row", async () => {
    const user = userEvent.setup();
    render(<Inbox />);
    await screen.findByText("First pending video");
    const row = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /^download$/i }));
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

  it("calls onCountChange with the decremented count after Download removes a row", async () => {
    const user = userEvent.setup();
    const onCountChange = vi.fn();
    render(<Inbox onCountChange={onCountChange} />);
    await screen.findByText("First pending video");
    onCountChange.mockClear();
    const row = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /^download$/i }));
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

  // Removing a card reflows the grid under a cursor that never moved, and the
  // browser then paints :hover on whatever control lands beneath it — the next
  // card's Download comes up lit as if it were the one just pressed. The lock
  // suppresses that paint; only a real pointer move lifts it, because a click
  // without one is the accidental second download this guards against.
  describe("hover lock", () => {
    it("locks the grid's hover paint after Download removes a card", async () => {
      const user = userEvent.setup();
      render(<Inbox />);
      await screen.findByText("First pending video");
      const grid = document.querySelector(".inbox-grid") as HTMLElement;
      expect(grid.classList.contains("is-hover-locked")).toBe(false);

      const card = screen
        .getByText("First pending video")
        .closest(".card") as HTMLElement;
      await user.click(within(card).getByRole("button", { name: /download/i }));

      await waitFor(() => {
        expect(grid.classList.contains("is-hover-locked")).toBe(true);
      });
    });

    it("locks it after Ignore too — the card goes the same way", async () => {
      const user = userEvent.setup();
      render(<Inbox />);
      await screen.findByText("First pending video");
      const card = screen
        .getByText("First pending video")
        .closest(".card") as HTMLElement;
      await user.click(within(card).getByRole("button", { name: /ignore/i }));

      await waitFor(() => {
        const grid = document.querySelector(".inbox-grid") as HTMLElement;
        expect(grid.classList.contains("is-hover-locked")).toBe(true);
      });
    });

    it("lifts the lock once the pointer actually moves", async () => {
      const user = userEvent.setup();
      render(<Inbox />);
      await screen.findByText("First pending video");
      const card = screen
        .getByText("First pending video")
        .closest(".card") as HTMLElement;
      await user.click(within(card).getByRole("button", { name: /download/i }));
      const grid = document.querySelector(".inbox-grid") as HTMLElement;
      await waitFor(() => {
        expect(grid.classList.contains("is-hover-locked")).toBe(true);
      });

      await user.pointer({ target: grid, coords: { x: 10, y: 10 } });

      await waitFor(() => {
        expect(grid.classList.contains("is-hover-locked")).toBe(false);
      });
    });

    it("keeps the lock through a Download all batch", async () => {
      const user = userEvent.setup();
      render(<Inbox />);
      await screen.findByText("First pending video");
      await user.click(screen.getByRole("button", { name: /download all/i }));

      await waitFor(() => {
        expect(screen.queryByText("Second pending video")).toBeNull();
      });
      const grid = document.querySelector(".inbox-grid") as HTMLElement;
      expect(grid.classList.contains("is-hover-locked")).toBe(true);
    });
  });

  it("shows an empty-state line when the inbox is empty", async () => {
    vi.mocked(listPending).mockResolvedValue([]);
    render(<Inbox />);
    expect(await screen.findByText("Your inbox is empty.")).toBeInTheDocument();
  });

  // The flicker: an empty `items` used to mean both "the inbox is empty" and
  // "the inbox has not arrived", so the mount paint claimed the first — with no
  // toolbar — and the response replaced it a moment later with search, chips
  // and a full grid.
  describe("first paint", () => {
    it("claims nothing about the inbox until the fetch settles", async () => {
      // Given: a request that has not answered yet.
      let settle: (items: PendingItem[]) => void = () => {};
      vi.mocked(listPending).mockReturnValue(
        new Promise((resolve) => {
          settle = resolve;
        }),
      );

      // When
      render(<Inbox />);

      // Then: no verdict on an inbox nobody has heard from — and no toolbar to
      // pop in behind it either.
      expect(screen.getByText("Loading…")).toBeInTheDocument();
      expect(screen.queryByText("Your inbox is empty.")).toBeNull();
      expect(screen.queryByLabelText("Search the inbox")).toBeNull();

      // When: the answer lands.
      settle([itemA, itemB]);

      // Then: toolbar and grid arrive in the same paint the "Loading…" leaves.
      expect(
        await screen.findByText("First pending video"),
      ).toBeInTheDocument();
      expect(screen.getByLabelText("Search the inbox")).toBeInTheDocument();
      expect(screen.queryByText("Loading…")).toBeNull();
    });

    it("reports a failed load instead of an empty inbox", async () => {
      // Given
      vi.mocked(listPending).mockRejectedValue(new Error("pending is down"));

      // When
      render(<Inbox />);

      // Then: the error replaces the "Loading…" line, and the page does not
      // also claim the inbox is empty — nothing is known about it.
      expect(await screen.findByText("pending is down")).toBeInTheDocument();
      expect(screen.queryByText("Loading…")).toBeNull();
      expect(screen.queryByText("Your inbox is empty.")).toBeNull();
    });
  });

  describe("the rail's count", () => {
    // The rail draws no pill for undefined, and that is not the same claim as
    // "the inbox is empty". A failed refetch that left the last good number in
    // place had the badge asserting a count nothing could vouch for.
    it("goes unknown when the fetch fails, rather than staying stale", async () => {
      // Given: a good load, so there is a number to go stale.
      const onCountChange = vi.fn();
      const { unmount } = render(<Inbox onCountChange={onCountChange} />);
      await screen.findByText("First pending video");
      expect(onCountChange).toHaveBeenLastCalledWith(2);
      unmount();

      // When: the next visit's fetch fails.
      vi.mocked(listPending).mockRejectedValue(new Error("pending is down"));
      render(<Inbox onCountChange={onCountChange} />);
      await screen.findByText("pending is down");

      // Then: unknown, not two.
      expect(onCountChange).toHaveBeenLastCalledWith(undefined);
    });

    // Navigating away mid-fetch used to land the response on a component that
    // is gone — React 18 stopped warning about it, so it was silent, but the
    // count still got pushed from a page the user had already left.
    it("says nothing once the view has unmounted mid-fetch", async () => {
      // Given: a request that has not answered yet.
      const onCountChange = vi.fn();
      let settle: (items: PendingItem[]) => void = () => {};
      vi.mocked(listPending).mockReturnValue(
        new Promise((resolve) => {
          settle = resolve;
        }),
      );
      const { unmount } = render(<Inbox onCountChange={onCountChange} />);

      // When: the user leaves, and only then does the answer land.
      unmount();
      settle([itemA, itemB]);
      await Promise.resolve();

      // Then
      expect(onCountChange).not.toHaveBeenCalled();
    });
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

  it("renders no channel pills when the items carry no channel", async () => {
    vi.mocked(listPending).mockResolvedValue([
      baseItem({ channel_id: "", channel_name: "" }),
    ]);
    render(<Inbox />);
    await screen.findByText("First pending video");
    expect(screen.queryByRole("button", { name: /All channels/ })).toBeNull();
  });

  it("shows the channel pills even with a single channel", async () => {
    vi.mocked(listPending).mockResolvedValue([itemA]);
    render(<Inbox />);
    await screen.findByText("First pending video");
    expect(
      screen.getByRole("button", { name: /All channels\s*1/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Channel One\s*1/ }),
    ).toBeInTheDocument();
  });

  it("scopes the channel chip counts to the search, like the Library", async () => {
    // "second" matches only itemB (Channel Two). The counts must answer "how
    // many would I see if I clicked this under the current search", and a
    // channel with no match drops off the row entirely.
    render(<Inbox search="second" />);
    await screen.findByText("Second pending video");

    // All channels reads the search-filtered total (1), not the full 2.
    expect(
      screen.getByRole("button", { name: /All channels\s*1/ }),
    ).toBeInTheDocument();
    // The matching channel stays with its scoped count; the other is gone.
    expect(
      screen.getByRole("button", { name: /Channel Two\s*1/ }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Channel One/ })).toBeNull();
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

  // Ghost has no fill and no border, and `busy` disables the button — which
  // .ui-btn:disabled fades to 0.6. Left ghost, the in-flight batch would be
  // faint grey text and a spinner floating in the toolbar, at the moment the
  // control most needs to be visible.
  it("Download all takes a fill back while the batch is in flight", async () => {
    // Given: a download that never settles, so the busy state can be read.
    const user = userEvent.setup();
    vi.mocked(downloadPending).mockReturnValue(new Promise(() => {}));
    render(<Inbox />);
    await screen.findByText("First pending video");
    const button = screen.getByRole("button", { name: /^download all$/i });
    expect(button).toHaveClass("ui-btn--ghost");

    // When
    await user.click(button);

    // Then: quiet at rest, filled while it works.
    await waitFor(() => expect(button).toBeDisabled());
    expect(button).toHaveClass("ui-btn--secondary");
    expect(button).not.toHaveClass("ui-btn--ghost");
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

  it("fires onQueued after a Download so the queue can seed", async () => {
    const user = userEvent.setup();
    const onQueued = vi.fn();
    render(<Inbox onQueued={onQueued} />);
    await screen.findByText("First pending video");
    const row = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /^download$/i }));
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

  it("surfaces a failure from Download and keeps the row", async () => {
    const user = userEvent.setup();
    vi.mocked(downloadPending).mockRejectedValue(new Error("disk is full"));
    render(<Inbox />);
    await screen.findByText("First pending video");
    const row = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /^download$/i }));

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

    // Nothing in the inbox has been downloaded, so the ONLY date these items
    // have is when they aired. The default must be air date, newest first.
    it("defaults to newest air date first", async () => {
      vi.mocked(listPending).mockResolvedValue([older, newer]);
      render(<Inbox />);
      await screen.findByText("Older video");
      expect(titles()).toEqual(["Anewer video", "Older video"]);
      expect((screen.getByLabelText("Sort") as HTMLSelectElement).value).toBe(
        "newest",
      );
    });

    // The Library's added-date orderings are meaningless here: an inbox item
    // has never been downloaded, so it has no added date to rank by. The
    // dropdown must not offer them at all.
    it("offers only the orderings an undownloaded item can satisfy", async () => {
      vi.mocked(listPending).mockResolvedValue([older, newer]);
      render(<Inbox />);
      await screen.findByText("Older video");

      const select = screen.getByLabelText("Sort") as HTMLSelectElement;
      const values = Array.from(select.options).map((o) => o.value);
      expect(values).toEqual(["newest", "oldest", "longest", "title"]);
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
  // The inbox arrives whole and unpaged, so the box filters client-side — the
  // same pipeline the channel chips and the sort already run through.
  describe("search", () => {
    it("narrows the grid by title", async () => {
      render(<Inbox search="second" />);
      expect(
        await screen.findByText("Second pending video"),
      ).toBeInTheDocument();
      expect(screen.queryByText("First pending video")).toBeNull();
    });

    it("matches a channel name too", async () => {
      render(<Inbox search="channel one" />);
      expect(
        await screen.findByText("First pending video"),
      ).toBeInTheDocument();
      expect(screen.queryByText("Second pending video")).toBeNull();
    });

    // Download all acts on what search and the chips selected, and stays
    // available whenever at least one item is on screen — even a single card is
    // quicker to clear from the toolbar than from its own row. It leaves only
    // when the result is empty.
    it("keeps Download all down to one item, gone only at zero", async () => {
      const { rerender } = render(<Inbox search="pending" />);
      await screen.findByText("First pending video");
      expect(
        screen.getByRole("button", { name: /download all/i }),
      ).toBeInTheDocument();

      // A single match still offers Download all.
      rerender(<Inbox search="second" />);
      await screen.findByText("Second pending video");
      expect(
        screen.getByRole("button", { name: /download all/i }),
      ).toBeInTheDocument();

      // Only an empty result removes it.
      rerender(<Inbox search="zzz" />);
      await waitFor(() =>
        expect(
          screen.queryByRole("button", { name: /download all/i }),
        ).toBeNull(),
      );
    });

    it("says nothing matches rather than showing an empty grid", async () => {
      render(<Inbox search="zzz" />);
      expect(
        await screen.findByText(/Nothing in the inbox matches/),
      ).toBeInTheDocument();
    });
  });
});
