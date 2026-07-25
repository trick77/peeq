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

describe("History", () => {
  beforeEach(() => {
    vi.mocked(listActivity).mockReset();
    vi.mocked(listActivity).mockResolvedValue({
      events: [ev()],
      has_more: false,
      retained_max: 2000,
    });
  });

  it("renders the log as one flat feed", async () => {
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

  it("caps the log at the newest 10 and hints at the earlier rows", async () => {
    vi.mocked(listActivity).mockResolvedValue({
      events: Array.from({ length: 14 }, (_, i) =>
        ev({ id: i + 1, subject: `Clip ${i + 1}` }),
      ),
      has_more: false,
      retained_max: 2000,
    });
    render(<History live={[]} />);
    expect(await screen.findByText("+4 earlier")).toBeInTheDocument();
    expect(screen.queryByText("Clip 11")).not.toBeInTheDocument();
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
});
