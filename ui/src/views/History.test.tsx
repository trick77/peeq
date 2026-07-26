import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { History } from "./History";
import type { ActivityEvent } from "../api/types";

vi.mock("../api", () => ({
  listActivity: vi.fn(),
}));

import { listActivity } from "../api";
import { DOT } from "../sep";

function ev(overrides: Partial<ActivityEvent> = {}): ActivityEvent {
  return {
    id: 1,
    at: "2026-07-23 12:00:00",
    kind: "download",
    outcome: "ok",
    subject: "A clip",
    summary: "downloaded",
    ...overrides,
  };
}

// Anchored so it cannot match the "Downloads" filter chip, which contains "load".
const LOAD_MORE = /^Load \d+ more$/;

describe("History", () => {
  beforeEach(() => {
    vi.mocked(listActivity).mockReset();
    vi.mocked(listActivity).mockResolvedValue({
      events: [ev()],
      has_more: false,
      retained_max: 2000,
    });
  });

  it("renders the log without the old two-section scaffolding", async () => {
    render(<History live={[]} />);
    expect(await screen.findByText("A clip")).toBeInTheDocument();
    // The projection moved to Up next, so neither the old section headings nor
    // the now marker survive here.
    expect(screen.queryByText("Recent activity")).not.toBeInTheDocument();
    expect(screen.queryByText("Up next")).not.toBeInTheDocument();
    expect(screen.queryByText("now")).not.toBeInTheDocument();
  });

  it("marks the row by outcome", async () => {
    vi.mocked(listActivity).mockResolvedValue({
      events: [ev({ id: 2, outcome: "fail", summary: "download failed" })],
      has_more: false,
      retained_max: 2000,
    });
    render(<History live={[]} />);
    const row = (await screen.findByText("A clip")).closest(
      ".ag-row",
    ) as HTMLElement;
    expect(row).toHaveClass("fail");
  });

  it("joins summary and detail into one line", async () => {
    vi.mocked(listActivity).mockResolvedValue({
      events: [ev({ summary: "downloaded", detail: "512 MB" })],
      has_more: false,
      retained_max: 2000,
    });
    render(<History live={[]} />);
    await screen.findByText("A clip");
    // Asserted on the node rather than via findByText: DOT is built from en
    // spaces, which testing-library's whitespace normalizer collapses, so a
    // string match would pass even if the separator were a plain space.
    const detail = document.querySelector(".ag-detail") as HTMLElement;
    expect(detail.textContent).toBe(`Downloaded${DOT}512 MB`);
  });

  it("live-appends a new event by id without a reload", async () => {
    const { rerender } = render(<History live={[]} />);
    await screen.findByText("A clip");
    rerender(<History live={[ev({ id: 99, subject: "Fresh one" })]} />);
    expect(await screen.findByText("Fresh one")).toBeInTheDocument();
    // The first fetch's row is still there — live events prepend, never replace.
    expect(screen.getByText("A clip")).toBeInTheDocument();
  });

  it("does not double a live event that the fetch also returned", async () => {
    render(<History live={[ev({ id: 1 })]} />);
    await waitFor(() => expect(screen.getAllByText("A clip")).toHaveLength(1));
  });

  it("offers no way back when the server says there is nothing older", async () => {
    render(<History live={[]} />);
    await screen.findByText("A clip");
    // Matched exactly: the "Downloads" filter chip contains the word "load".
    expect(screen.queryByRole("button", { name: LOAD_MORE })).toBeNull();
  });

  // Keyset, not offset: paging back from the oldest id on screen means a live
  // event arriving mid-read can't shift the window and duplicate a row.
  it("pages back from the oldest row it holds", async () => {
    vi.mocked(listActivity).mockResolvedValueOnce({
      events: [ev({ id: 9, subject: "Newest" })],
      has_more: true,
      retained_max: 2000,
    });
    vi.mocked(listActivity).mockResolvedValueOnce({
      events: [ev({ id: 4, subject: "Older" })],
      has_more: false,
      retained_max: 2000,
    });
    const user = userEvent.setup();
    render(<History live={[]} />);
    await screen.findByText("Newest");
    await user.click(screen.getByRole("button", { name: LOAD_MORE }));
    expect(await screen.findByText("Older")).toBeInTheDocument();
    // Paged from the oldest id held, not from an offset.
    expect(vi.mocked(listActivity).mock.calls[1][0]).toBe(9);
    // Nothing older left, so the control retires rather than fetching nothing.
    expect(screen.queryByRole("button", { name: LOAD_MORE })).toBeNull();
    // The first page is still on screen — pages append, never replace.
    expect(screen.getByText("Newest")).toBeInTheDocument();
  });

  it("groups rows under a day separator, with a clock in the gutter", async () => {
    vi.mocked(listActivity).mockResolvedValue({
      events: [
        ev({ id: 1, at: "2026-07-23 12:00:00", subject: "Older day" }),
        ev({ id: 2, at: "2026-07-22 09:30:00", subject: "Earlier day" }),
      ],
      has_more: false,
      retained_max: 2000,
    });
    render(<History live={[]} />);
    await screen.findByText("Older day");
    // Two distinct days → two separators, each naming its date.
    const seps = Array.from(document.querySelectorAll(".ag-daysep span")).map(
      (s) => s.textContent,
    );
    expect(seps).toHaveLength(2);
    expect(seps[0]).toContain("23 Jul");
    expect(seps[1]).toContain("22 Jul");
    // The gutter carries a wall clock, not a relative time — the row's own
    // "ago" label lives on the right.
    const row = screen.getByText("Older day").closest(".ag-row") as HTMLElement;
    expect(row.querySelector(".ag-clock")?.textContent).toMatch(
      /^\d{2}:\d{2}$/,
    );
  });

  // Colour on the ring is what makes a failure findable without reading every
  // line; the row class is what carries it.
  it("marks the node with the outcome", async () => {
    vi.mocked(listActivity).mockResolvedValue({
      events: [
        ev({ id: 1, outcome: "fail", subject: "Broken" }),
        ev({ id: 2, outcome: "ok", subject: "Fine" }),
      ],
      has_more: false,
      retained_max: 2000,
    });
    render(<History live={[]} />);
    await screen.findByText("Broken");
    const bad = screen.getByText("Broken").closest(".ag-row") as HTMLElement;
    const good = screen.getByText("Fine").closest(".ag-row") as HTMLElement;
    expect(bad).toHaveClass("fail");
    expect(good).toHaveClass("ok");
    expect(bad.querySelector(".ag-node")).toBeTruthy();
  });

  // The workers already write the past-tense verb, so it leads the line as-is
  // rather than being duplicated by a second vocabulary in the frontend.
  it("leads the detail line with the worker's own past-tense word", async () => {
    vi.mocked(listActivity).mockResolvedValue({
      events: [
        ev({ id: 1, summary: "download failed", detail: "sign-in required" }),
      ],
      has_more: false,
      retained_max: 2000,
    });
    render(<History live={[]} />);
    await screen.findByText("A clip");
    expect(document.querySelector(".ag-kind")?.textContent).toBe(
      "Download failed",
    );
    expect(document.querySelector(".ag-detail")?.textContent).toBe(
      `Download failed${DOT}sign-in required`,
    );
  });

  // A count is not a verb. When the worker's summary opens with a number the
  // kind supplies the word, and the count moves into the rest of the line.
  it("supplies the kind's verb when the summary starts with a count", async () => {
    vi.mocked(listActivity).mockResolvedValue({
      events: [
        ev({
          id: 1,
          kind: "scan",
          subject: "Veritasium",
          summary: "3 new",
          detail: "streams tab missing",
        }),
      ],
      has_more: false,
      retained_max: 2000,
    });
    render(<History live={[]} />);
    await screen.findByText("Veritasium");
    expect(document.querySelector(".ag-kind")?.textContent).toBe("Scanned");
    expect(document.querySelector(".ag-detail")?.textContent).toBe(
      `Scanned${DOT}3 new${DOT}streams tab missing`,
    );
  });

  it("problems-only keeps failures and warnings, drops the healthy rows", async () => {
    vi.mocked(listActivity).mockResolvedValue({
      events: [
        ev({ id: 1, subject: "Fine one", outcome: "ok" }),
        ev({ id: 2, subject: "Broken one", outcome: "fail" }),
        ev({ id: 3, subject: "Iffy one", outcome: "warn" }),
      ],
      has_more: false,
      retained_max: 2000,
    });
    const user = userEvent.setup();
    render(<History live={[]} />);
    await screen.findByText("Fine one");
    await user.click(screen.getByRole("button", { name: "Problems only" }));
    expect(screen.queryByText("Fine one")).not.toBeInTheDocument();
    expect(screen.getByText("Broken one")).toBeInTheDocument();
    expect(screen.getByText("Iffy one")).toBeInTheDocument();
  });

  it("says so when a filter matches nothing, rather than looking empty", async () => {
    const user = userEvent.setup();
    render(<History live={[]} />);
    await screen.findByText("A clip");
    await user.click(screen.getByRole("button", { name: "Scans" }));
    expect(
      screen.getByText(/nothing matching that filter/i),
    ).toBeInTheDocument();
  });

  // The ceiling explains why the page stops where it does, so it belongs where
  // it is read before scrolling — the end of the chip row.
  it("states the retention ceiling in the chip row", async () => {
    render(<History live={[]} />);
    await screen.findByText("A clip");
    const note = document.querySelector(".chips .chips-note") as HTMLElement;
    expect(note).toBeTruthy();
    expect(note.textContent).toContain("2000");
  });

  it("links a channel-kind subject to its page", async () => {
    vi.mocked(listActivity).mockResolvedValue({
      events: [
        ev({ id: 5, kind: "scan", subject: "Veritasium", subject_id: "UCx" }),
      ],
      has_more: false,
      retained_max: 2000,
    });
    const onOpenChannel = vi.fn();
    const user = userEvent.setup();
    render(<History live={[]} onOpenChannel={onOpenChannel} />);
    await user.click(await screen.findByRole("button", { name: "Veritasium" }));
    expect(onOpenChannel).toHaveBeenCalledWith("UCx");
  });

  it("leaves a video subject as plain text", async () => {
    vi.mocked(listActivity).mockResolvedValue({
      events: [
        ev({ id: 6, kind: "download", subject: "A clip", subject_id: "v1" }),
      ],
      has_more: false,
      retained_max: 2000,
    });
    render(<History live={[]} onOpenChannel={vi.fn()} />);
    await screen.findByText("A clip");
    expect(screen.queryByRole("button", { name: "A clip" })).toBeNull();
  });

  it("surfaces a load failure instead of an empty page", async () => {
    vi.mocked(listActivity).mockRejectedValue(new Error("boom"));
    render(<History live={[]} />);
    expect(await screen.findByText("boom")).toBeInTheDocument();
  });

  // The names in the log point at the things they name. Channel rows have
  // always linked; video rows used to be dead text.
  describe("linking", () => {
    it("opens a downloaded video in the player from its name", async () => {
      vi.mocked(listActivity).mockResolvedValue({
        events: [ev({ id: 30, kind: "download", subject_id: "vid30" })],
        has_more: false,
        retained_max: 2000,
      });
      const onOpenVideo = vi.fn();
      const user = userEvent.setup();
      render(<History live={[]} onOpenVideo={onOpenVideo} />);
      await user.click(await screen.findByRole("button", { name: "A clip" }));
      expect(onOpenVideo).toHaveBeenCalledWith("vid30");
    });

    // A failed download names a video that may never have made it into the
    // library. It still links: the player says so plainly, which beats a name
    // that silently refuses to be clicked.
    it("links a failed download too", async () => {
      vi.mocked(listActivity).mockResolvedValue({
        events: [
          ev({
            id: 31,
            kind: "download",
            outcome: "fail",
            subject_id: "vid31",
            summary: "download failed",
          }),
        ],
        has_more: false,
        retained_max: 2000,
      });
      render(<History live={[]} onOpenVideo={vi.fn()} />);
      expect(
        await screen.findByRole("button", { name: "A clip" }),
      ).toBeInTheDocument();
    });

    // A scan names a channel, so it must go to the channel page even with a
    // video handler in scope.
    it("still sends a scan row to its channel, not the player", async () => {
      vi.mocked(listActivity).mockResolvedValue({
        events: [
          ev({
            id: 32,
            kind: "scan",
            subject: "Veritasium",
            subject_id: "UCx",
          }),
        ],
        has_more: false,
        retained_max: 2000,
      });
      const onOpenChannel = vi.fn();
      const onOpenVideo = vi.fn();
      const user = userEvent.setup();
      render(
        <History
          live={[]}
          onOpenChannel={onOpenChannel}
          onOpenVideo={onOpenVideo}
        />,
      );
      await user.click(
        await screen.findByRole("button", { name: "Veritasium" }),
      );
      expect(onOpenChannel).toHaveBeenCalledWith("UCx");
      expect(onOpenVideo).not.toHaveBeenCalled();
    });

    // Cleanup and yt-dlp rows name neither, so they stay plain text.
    it("leaves a row with no linkable subject as text", async () => {
      vi.mocked(listActivity).mockResolvedValue({
        events: [
          ev({
            id: 33,
            kind: "retention",
            subject: "Old files",
            subject_id: "x",
          }),
        ],
        has_more: false,
        retained_max: 2000,
      });
      render(
        <History live={[]} onOpenChannel={vi.fn()} onOpenVideo={vi.fn()} />,
      );
      await screen.findByText("Old files");
      expect(screen.queryByRole("button", { name: "Old files" })).toBeNull();
    });
  });

  // The log is paginated, so the box has to be a SERVER query — a client filter
  // would search only the rows already paged in and answer "nothing" for
  // something the log plainly contains.
  describe("search", () => {
    it("sends the query to the server, debounced", async () => {
      const { rerender } = render(<History live={[]} search="" />);
      await screen.findByText("A clip");
      expect(listActivity).toHaveBeenLastCalledWith(undefined, 20, undefined);

      rerender(<History live={[]} search="veritasium" />);
      await waitFor(
        () =>
          expect(listActivity).toHaveBeenLastCalledWith(
            undefined,
            20,
            "veritasium",
          ),
        { timeout: 2000 },
      );
    });

    it("carries the query into the next page back", async () => {
      vi.mocked(listActivity).mockResolvedValue({
        events: [ev({ id: 40, subject: "A clip" })],
        has_more: true,
        retained_max: 2000,
      });
      const user = userEvent.setup();
      render(<History live={[]} search="clip" />);
      await screen.findByText("A clip");
      await waitFor(
        () =>
          expect(listActivity).toHaveBeenLastCalledWith(undefined, 20, "clip"),
        { timeout: 2000 },
      );
      await user.click(screen.getByRole("button", { name: LOAD_MORE }));
      expect(listActivity).toHaveBeenLastCalledWith(40, 20, "clip");
    });

    // An SSE arrival has never been through the server query, so it must be
    // held to the same filter or it drops a non-matching row into the results.
    it("keeps a non-matching live event out of a filtered page", async () => {
      const { rerender } = render(<History live={[]} search="clip" />);
      await screen.findByText("A clip");
      await waitFor(
        () =>
          expect(listActivity).toHaveBeenLastCalledWith(undefined, 20, "clip"),
        { timeout: 2000 },
      );
      rerender(
        <History
          live={[ev({ id: 50, subject: "Something else", summary: "scanned" })]}
          search="clip"
        />,
      );
      await waitFor(() =>
        expect(screen.queryByText("Something else")).toBeNull(),
      );
      expect(screen.getByText("A clip")).toBeInTheDocument();
    });

    it("names the query when nothing matches", async () => {
      vi.mocked(listActivity).mockResolvedValue({
        events: [],
        has_more: false,
        retained_max: 2000,
      });
      render(<History live={[]} search="nope" />);
      expect(
        await screen.findByText(/Nothing in the log matches/),
      ).toBeInTheDocument();
      // Not the cold-start line — the log may be full, just not of this.
      expect(screen.queryByText(/this fills in as peeq scans/)).toBeNull();
    });

    // A changed query REPLACES the rows. Keeping the previous query's — which
    // include every older page it had scrolled to — prepends them above the
    // fresh newest page, so old rows sit on top of new ones and a day the log
    // already had gets a second separator further down.
    it("drops the previous query's rows instead of stacking them on top", async () => {
      const user = userEvent.setup();
      // The filtered query: one old row, with more behind it.
      vi.mocked(listActivity).mockResolvedValue({
        events: [ev({ id: 4, at: "2026-07-20 09:00:00", subject: "Old clip" })],
        has_more: true,
        retained_max: 2000,
      });
      const { rerender } = render(<History live={[]} search="clip" />);
      await screen.findByText("Old clip");
      vi.mocked(listActivity).mockResolvedValue({
        events: [
          ev({ id: 3, at: "2026-07-20 08:00:00", subject: "Older clip" }),
        ],
        has_more: false,
        retained_max: 2000,
      });
      await user.click(screen.getByRole("button", { name: LOAD_MORE }));
      await screen.findByText("Older clip");

      // Box cleared: the newest page is a later day's rows, and the two old
      // ones are not part of this query's answer.
      vi.mocked(listActivity).mockResolvedValue({
        events: [ev({ id: 9, at: "2026-07-23 12:00:00", subject: "New one" })],
        has_more: true,
        retained_max: 2000,
      });
      rerender(<History live={[]} search="" />);
      await screen.findByText("New one");
      await waitFor(() => expect(screen.queryByText("Old clip")).toBeNull());
      expect(screen.queryByText("Older clip")).toBeNull();
      // One row, so one day separator — never a stale day above the newest.
      expect(document.querySelectorAll(".ag-daysep").length).toBe(1);
    });

    // The mount-time merge the replace must not break: a live event that
    // arrived while the first page was in flight is newer than everything the
    // server sent, and stays.
    it("keeps a live arrival that beat the first fetch", async () => {
      let resolve!: (p: {
        events: ActivityEvent[];
        has_more: boolean;
        retained_max: number;
      }) => void;
      vi.mocked(listActivity).mockReturnValue(
        new Promise((r) => {
          resolve = r;
        }),
      );
      const { rerender } = render(<History live={[]} search="" />);
      rerender(<History live={[ev({ id: 99, subject: "Fresh one" })]} />);
      resolve({
        events: [ev({ id: 1, subject: "A clip" })],
        has_more: false,
        retained_max: 2000,
      });
      expect(await screen.findByText("A clip")).toBeInTheDocument();
      expect(screen.getByText("Fresh one")).toBeInTheDocument();
    });
  });
});

// These two pin the bugs a medium review of #161 turned up.
describe("History regressions", () => {
  beforeEach(() => {
    vi.mocked(listActivity).mockReset();
  });

  // dayLabel round-tripped `now` through toISOString(), which already ends in
  // "Z"; parseUTC then appended a second one, giving an Invalid Date. Both the
  // today and yesterday keys read "NaN-NaN-NaN", matched nothing, and the two
  // labels anyone actually reads never rendered. Fixed dates in the other test
  // could not catch it — only an event stamped relative to now can.
  it("names today and yesterday, not just their dates", async () => {
    const iso = (d: Date) => d.toISOString().slice(0, 19).replace("T", " ");
    vi.mocked(listActivity).mockResolvedValue({
      events: [
        ev({ id: 1, at: iso(new Date()), subject: "Just happened" }),
        ev({
          id: 2,
          at: iso(new Date(Date.now() - 86400_000)),
          subject: "A day back",
        }),
      ],
      has_more: false,
      retained_max: 2000,
    });
    render(<History live={[]} />);
    await screen.findByText("Just happened");
    const seps = Array.from(document.querySelectorAll(".ag-daysep span")).map(
      (s) => s.textContent,
    );
    expect(seps[0]).toBe("Today");
    expect(seps[1]).toContain("Yesterday");
  });

  // A transient failure paging backwards used to render in place of the whole
  // page, throwing away the log already on screen.
  it("keeps the loaded log when paging backwards fails", async () => {
    vi.mocked(listActivity).mockResolvedValueOnce({
      events: [ev({ id: 9, subject: "Already here" })],
      has_more: true,
      retained_max: 2000,
    });
    vi.mocked(listActivity).mockRejectedValueOnce(new Error("network down"));
    const user = userEvent.setup();
    render(<History live={[]} />);
    await screen.findByText("Already here");
    await user.click(screen.getByRole("button", { name: LOAD_MORE }));
    expect(await screen.findByText("network down")).toBeInTheDocument();
    // The rows that did load are still there, and so is the retry.
    expect(screen.getByText("Already here")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: LOAD_MORE })).toBeInTheDocument();
  });
});
