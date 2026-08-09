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
    summary_status: "done",
    auto_summary: true,
    summary_gave_up: false,
    has_subtitles: true,
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

  // The pair sits in a bar UNDER the poster, not on it: on the poster it
  // covered the title creators bake into a thumbnail and displaced .dur. The
  // bar is a sibling of .thumb inside .inbox-poster, which is what lets the two
  // touch. Both buttons are always rendered — no hover, no focus, no pointer of
  // any kind involved in getting to them. Library's .acts overlay reveals on
  // hover and is therefore unreachable on a touch screen; this asserts Inbox
  // did not inherit that.
  it("puts Download and Ignore in a bar below the poster, visible without a pointer", async () => {
    render(<Inbox />);
    await screen.findByText("First pending video");
    const card = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;

    const download = within(card).getByRole("button", { name: /^download$/i });
    const ignore = within(card).getByRole("button", { name: /ignore/i });

    expect(download.closest(".thumb")).toBeNull();
    expect(ignore.closest(".thumb")).toBeNull();
    expect(download.closest(".inbox-actbar")).not.toBeNull();
    expect(ignore.closest(".inbox-actbar")).not.toBeNull();

    const poster = card.querySelector(".inbox-poster") as HTMLElement;
    expect(poster.querySelector(":scope > .thumb")).not.toBeNull();
    expect(poster.querySelector(":scope > .inbox-actbar")).not.toBeNull();
    expect(card.querySelector(".card-foot")).toBeNull();
  });

  // Ignore carries its name as visible text, not as an aria-label. A bare trash
  // can asked the reader to guess whether it drops the video from the inbox or
  // deletes something already downloaded; off the poster there is room to say.
  // The assertion is that the accessible name comes from the content — an
  // aria-label would silently override the label a sighted user reads.
  it("names Ignore with visible text rather than an aria-label", async () => {
    render(<Inbox />);
    await screen.findByText("First pending video");
    const card = screen
      .getByText("First pending video")
      .closest(".card") as HTMLElement;

    const ignore = within(card).getByRole("button", { name: "Ignore" });
    expect(ignore.textContent?.trim()).toBe("Ignore");
    expect(ignore).not.toHaveAttribute("aria-label");
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

  // The row used to follow first-seen order, so it reshuffled as the grid
  // drained. The Library's category row is the master: a fixed order that does
  // not depend on what the grid happens to hold.
  it("orders the channel chips by name, after All channels", async () => {
    vi.mocked(listPending).mockResolvedValue([
      baseItem({ video_id: "v3", channel_id: "c3", channel_name: "Zulu" }),
      itemB, // Channel Two — seen before Channel One
      // Lowercase, and it must still sort ahead of "Beta": a plain codepoint
      // comparison would put every capital first and land it after Zulu.
      baseItem({ video_id: "v4", channel_id: "c4", channel_name: "alpha" }),
      baseItem({ video_id: "v5", channel_id: "c5", channel_name: "Beta" }),
      itemA, // Channel One
    ]);
    render(<Inbox />);
    await screen.findByText("Second pending video");

    const labels = Array.from(
      document.querySelectorAll(".catchips .catchip"),
    ).map((el) => el.textContent?.replace(/\s*\d+$/, "").trim());
    expect(labels).toEqual([
      "All channels",
      "alpha",
      "Beta",
      "Channel One",
      "Channel Two",
      "Zulu",
    ]);
  });

  // Same name, so the comparator's first arm ties. Without the id tiebreak the
  // pair falls back to Array.sort's stability — first-seen order, which is what
  // the sort exists to remove.
  it("breaks a tie between same-named channels on the id", async () => {
    const user = userEvent.setup();
    vi.mocked(listPending).mockResolvedValue([
      baseItem({
        video_id: "v6",
        channel_id: "c9",
        channel_name: "Twin",
        title: "Nine video",
      }),
      baseItem({
        video_id: "v7",
        channel_id: "c8",
        channel_name: "Twin",
        title: "Eight video",
      }),
    ]);
    render(<Inbox />);
    await screen.findByText("Nine video");

    // c8 was seen second, but its chip comes first.
    const chips =
      document.querySelectorAll<HTMLButtonElement>(".catchips .catchip");
    expect(Array.from(chips).length).toBe(3); // All channels + the two twins
    await user.click(chips[1]);
    expect(screen.getByText("Eight video")).toBeInTheDocument();
    expect(screen.queryByText("Nine video")).not.toBeInTheDocument();
  });

  // Overflow is the Library's: one line that scrolls sideways under chevrons,
  // never a wrapped block of rows.
  it("puts the channel chips in a PillStrip rather than letting them wrap", async () => {
    render(<Inbox />);
    await screen.findByText("First pending video");

    const strip = document.querySelector(".pillstrip");
    expect(strip).not.toBeNull();
    // `lead` drops the negative top margin: this is the page's first chip row.
    expect(strip).toHaveClass("lead");
    expect(strip?.querySelector(".pillstrip-scroll .catchips")).not.toBeNull();
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

  // Ghost has no fill and no border, so it reads as bare label text rather
  // than as a button — and `busy` disables the button, which .ui-btn:disabled
  // fades to 0.6. Gone ghost, an in-flight batch would be faint grey text and
  // a spinner floating in the toolbar, at the moment the control most needs to
  // be visible. The fill is the invariant, at rest and in flight alike.
  it("Download all keeps its fill at rest and while the batch is in flight", async () => {
    // Given: a download that never settles, so the busy state can be read.
    const user = userEvent.setup();
    vi.mocked(downloadPending).mockReturnValue(new Promise(() => {}));
    render(<Inbox />);
    await screen.findByText("First pending video");
    const button = screen.getByRole("button", { name: /^download all$/i });
    expect(button).toHaveClass("ui-btn--secondary");
    expect(button).not.toHaveClass("ui-btn--ghost");

    // When
    await user.click(button);

    // Then: still filled while it works.
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

    // The Library's import-date orderings — its default there — are meaningless
    // here: an inbox item has never been downloaded, so it has no import date to
    // rank by. The dropdown must not offer them at all, which is also why this
    // list keeps opening on air date while the Library no longer does.
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

// The summary marker and the card's click target — the two things the Inbox
// gained when peeq started reading videos before you decide on them.
describe("Inbox summaries", () => {
  beforeEach(() => {
    vi.mocked(listPending).mockReset();
    vi.mocked(downloadPending).mockReset();
    vi.mocked(ignorePending).mockReset();
    vi.mocked(downloadPending).mockResolvedValue(undefined);
    vi.mocked(ignorePending).mockResolvedValue(undefined);
  });

  // Three states, and the third is why the API sends two fields. An empty
  // summary_status means "not reached yet" on an opted-in channel and "never
  // will be" on an opted-out one; only the second should be silent.
  it("marks a read video, a pending one, and stays silent otherwise", async () => {
    vi.mocked(listPending).mockResolvedValue([
      baseItem({ video_id: "done", title: "Read already" }),
      baseItem({
        video_id: "wait",
        title: "Still reading",
        summary_status: "",
        auto_summary: true,
      }),
      baseItem({
        video_id: "off",
        title: "Channel opted out",
        summary_status: "",
        auto_summary: false,
      }),
      baseItem({
        video_id: "music",
        title: "No speech",
        summary_status: "no_transcript",
        auto_summary: true,
        has_subtitles: false,
      }),
    ]);
    render(<Inbox onOpen={vi.fn()} />);

    const card = async (title: string) =>
      (await screen.findByText(title)).closest("article") as HTMLElement;

    // The read state is a button, not a label: the summary is the offer, and
    // the marker is where it gets made.
    expect(
      within(await card("Read already")).getByRole("button", {
        name: "Read summary",
      }),
    ).toBeTruthy();
    // The pending one is not, because there is nothing to press yet.
    expect(
      within(await card("Still reading")).getByText("Summarizing…"),
    ).toBeTruthy();
    expect(
      within(await card("Still reading")).queryByRole("button", {
        name: /summary/i,
      }),
    ).toBeNull();
    // A channel that opted out promises nothing, so the card says nothing.
    expect(
      within(await card("Channel opted out")).queryByText("Summarizing…"),
    ).toBeNull();
    expect(
      within(await card("Channel opted out")).queryByText(/summary/i),
    ).toBeNull();
    // Neither does one whose captions turned out to be music: there is no
    // action behind it and no progress left to report.
    expect(
      within(await card("No speech")).queryByText("Summarizing…"),
    ).toBeNull();
    expect(within(await card("No speech")).queryByText(/summary/i)).toBeNull();
  });

  // The music case: captions on disk that produced no summary. It used to be
  // offered as "Read transcript", and is not offered at all any more — a
  // summary is the only thing the Inbox stops you to read, and raw caption
  // text is not one. The .vtt behind the card makes no difference, which is
  // the whole point: both halves of 'no_transcript' now behave alike.
  it("does not offer a video whose captions produced no summary", async () => {
    vi.mocked(listPending).mockResolvedValue([
      baseItem({
        video_id: "music",
        title: "A music video",
        summary_status: "no_transcript",
        has_subtitles: true,
      }),
    ]);
    const onOpen = vi.fn();
    render(<Inbox onOpen={onOpen} />);

    const card = (await screen.findByText("A music video")).closest(
      "article",
    ) as HTMLElement;
    await userEvent.click(card.querySelector(".thumb") as HTMLElement);

    expect(within(card).queryByText(/transcript/i)).toBeNull();
    expect(within(card).queryByText(/summary/i)).toBeNull();
    expect(onOpen).not.toHaveBeenCalled();
    expect(card.className).toContain("is-inert");
  });

  // The card opens exactly when it says it opens. A no_transcript video with
  // no captions has nothing behind it — its page would add nothing the card is
  // not already showing — so it must neither draw a marker nor answer a click.
  it("does not open a video with nothing to read", async () => {
    vi.mocked(listPending).mockResolvedValue([
      baseItem({
        video_id: "silent",
        title: "No captions ever",
        summary_status: "no_transcript",
        has_subtitles: false,
      }),
    ]);
    const onOpen = vi.fn();
    render(<Inbox onOpen={onOpen} />);

    const card = (await screen.findByText("No captions ever")).closest(
      "article",
    ) as HTMLElement;
    await userEvent.click(card.querySelector(".thumb") as HTMLElement);

    expect(onOpen).not.toHaveBeenCalled();
    expect(card.className).toContain("is-inert");
  });

  // Same rule, the state that used to slip through it: a failed summary drew no
  // marker and still opened a page whose only news was that it would be retried.
  //
  // Still true while a retry is coming — and with the ladder at 15m then 4h,
  // that is most of the time a card wears this status.
  it("does not open a video whose summary failed and will be retried", async () => {
    vi.mocked(listPending).mockResolvedValue([
      baseItem({
        video_id: "bad",
        title: "Failed",
        summary_status: "error",
        summary_gave_up: false,
      }),
    ]);
    const onOpen = vi.fn();
    render(<Inbox onOpen={onOpen} />);

    const card = (await screen.findByText("Failed")).closest(
      "article",
    ) as HTMLElement;
    await userEvent.click(card.querySelector(".thumb") as HTMLElement);

    expect(onOpen).not.toHaveBeenCalled();
    expect(card.className).toContain("is-inert");
  });

  // Once nothing will retry it, silence is the wrong answer: an inert unmarked
  // card is indistinguishable from one with no captions, so the video sinks
  // into the list with no sign that anything went wrong.
  describe("a summary that gave up", () => {
    const gaveUp = () =>
      baseItem({
        video_id: "bad",
        title: "Failed",
        summary_status: "error",
        summary_gave_up: true,
      });

    it("says so on the card", async () => {
      vi.mocked(listPending).mockResolvedValue([gaveUp()]);
      render(<Inbox onOpen={vi.fn()} />);

      expect(await screen.findByText("Summary failed")).toBeInTheDocument();
    });

    // The marker leads to the Player, because that is where Reprocess lives and
    // there is nowhere else to send you. A mark naming a failure and leading
    // nowhere would be the dead end the marker rule exists to prevent.
    it("opens the player, where the repair is", async () => {
      vi.mocked(listPending).mockResolvedValue([gaveUp()]);
      const onOpen = vi.fn();
      render(<Inbox onOpen={onOpen} />);

      const card = (await screen.findByText("Failed")).closest(
        "article",
      ) as HTMLElement;
      await userEvent.click(card.querySelector(".thumb") as HTMLElement);

      expect(onOpen).toHaveBeenCalledWith("bad");
      expect(card.className).not.toContain("is-inert");
    });

    // The marker is itself the control, not just a label the card happens to
    // sit under — pressing it has to work on its own.
    it("opens from the marker itself", async () => {
      vi.mocked(listPending).mockResolvedValue([gaveUp()]);
      const onOpen = vi.fn();
      render(<Inbox onOpen={onOpen} />);

      await userEvent.click(
        await screen.findByRole("button", { name: /Summary failed/ }),
      );

      expect(onOpen).toHaveBeenCalledWith("bad");
    });

    // Same rule the readable marker follows: a host that gave the card nowhere
    // to go gets the fact stated, never a control that could only do nothing.
    it("states the fact without a control when there is nowhere to go", async () => {
      vi.mocked(listPending).mockResolvedValue([gaveUp()]);
      render(<Inbox />);

      expect(await screen.findByText("Summary failed")).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: /Summary failed/ }),
      ).toBeNull();
    });

    // A video whose summary arrived and whose LATER step died keeps its summary
    // status, so it must still offer the summary. The job gave up, but there is
    // something to read, and saying so is the card's whole job.
    it("still offers a summary whose key points or index failed", async () => {
      vi.mocked(listPending).mockResolvedValue([
        baseItem({
          video_id: "kp",
          title: "Half done",
          summary_status: "done",
          summary_gave_up: true,
        }),
      ]);
      render(<Inbox onOpen={vi.fn()} />);

      expect(await screen.findByText("Read summary")).toBeInTheDocument();
      expect(screen.queryByText("Summary failed")).toBeNull();
    });
  });

  it("opens the summary from the marker", async () => {
    vi.mocked(listPending).mockResolvedValue([itemA]);
    const onOpen = vi.fn();
    render(<Inbox onOpen={onOpen} />);
    await screen.findByText("First pending video");

    await userEvent.click(screen.getByRole("button", { name: "Read summary" }));

    expect(onOpen).toHaveBeenCalledWith("v1");
    // Once, not twice: the card's own click handler has to stand down for a
    // press that lands on a button inside it.
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  // A host that gives the card nowhere to go still gets the fact, as text —
  // better than a button that does nothing when pressed.
  it("falls back to a label when there is nothing to open", async () => {
    vi.mocked(listPending).mockResolvedValue([itemA]);
    render(<Inbox />);
    await screen.findByText("First pending video");

    expect(screen.getByText("Summary")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /summary/i })).toBeNull();
  });

  // A video the caption fetcher has not reached has no videos row, so its page
  // would 404. That is the common case on a fresh inbox, not an edge — and the
  // card must not offer a pointer to it either.
  it("does not open a video peeq has not read yet", async () => {
    vi.mocked(listPending).mockResolvedValue([
      baseItem({ video_id: "unread", summary_status: "", auto_summary: true }),
    ]);
    const onOpen = vi.fn();
    render(<Inbox onOpen={onOpen} />);

    const title = await screen.findByText("First pending video");
    const card = title.closest("article") as HTMLElement;
    await userEvent.click(card.querySelector(".thumb") as HTMLElement);

    expect(onOpen).not.toHaveBeenCalled();
    expect(card.className).toContain("is-inert");
  });

  it("opens the video when the card is clicked", async () => {
    vi.mocked(listPending).mockResolvedValue([itemA]);
    const onOpen = vi.fn();
    render(<Inbox onOpen={onOpen} />);

    const title = await screen.findByText("First pending video");
    const poster = (title.closest("article") as HTMLElement).querySelector(
      ".thumb",
    ) as HTMLElement;
    await userEvent.click(poster);

    expect(onOpen).toHaveBeenCalledWith("v1");
  });

  // The guard that makes a whole-card click target safe. Without it, pressing
  // Ignore would also navigate to the page of the video you just dismissed.
  it("leaves the action bar its own clicks", async () => {
    vi.mocked(listPending).mockResolvedValue([itemA]);
    const onOpen = vi.fn();
    render(<Inbox onOpen={onOpen} />);
    await screen.findByText("First pending video");

    await userEvent.click(screen.getByRole("button", { name: /Ignore/ }));

    expect(ignorePending).toHaveBeenCalledWith("v1");
    expect(onOpen).not.toHaveBeenCalled();
  });

  it("leaves the channel link its own click", async () => {
    vi.mocked(listPending).mockResolvedValue([itemA]);
    const onOpen = vi.fn();
    const onOpenChannel = vi.fn();
    render(<Inbox onOpen={onOpen} onOpenChannel={onOpenChannel} />);
    await screen.findByText("First pending video");

    await userEvent.click(screen.getByRole("button", { name: "Channel One" }));

    expect(onOpenChannel).toHaveBeenCalledWith("c1");
    expect(onOpen).not.toHaveBeenCalled();
  });
});

// The Inbox owns the on-screen order — search, channel chip and sort all shape
// it — so it is the only thing that can tell a video's page where it sits.
describe("Inbox order reporting", () => {
  beforeEach(() => {
    vi.mocked(listPending).mockReset();
    vi.mocked(downloadPending).mockReset();
    vi.mocked(ignorePending).mockReset();
    vi.mocked(downloadPending).mockResolvedValue(undefined);
    vi.mocked(ignorePending).mockResolvedValue(undefined);
  });

  it("reports the visible ids in the order they are shown", async () => {
    vi.mocked(listPending).mockResolvedValue([itemA, itemB]);
    const onOrderChange = vi.fn();
    render(<Inbox onOrderChange={onOrderChange} />);

    await screen.findByText("First pending video");
    await waitFor(() =>
      expect(onOrderChange).toHaveBeenCalledWith(["v1", "v2"]),
    );
  });

  // A page that re-derived the order from the API would say "2 of 14" while the
  // grid behind it showed one result. Searching has to narrow it here too.
  it("reports only what a search leaves visible", async () => {
    vi.mocked(listPending).mockResolvedValue([itemA, itemB]);
    const onOrderChange = vi.fn();
    render(<Inbox onOrderChange={onOrderChange} search="Second" />);

    await screen.findByText("Second pending video");
    await waitFor(() => expect(onOrderChange).toHaveBeenCalledWith(["v2"]));
  });
});
