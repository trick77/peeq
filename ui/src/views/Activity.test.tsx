import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Activity } from "./Activity";
import type {
  ActivityEvent,
  Job,
  SummaryJob,
  UpcomingItem,
} from "../api/types";

vi.mock("../api", () => ({
  listActivity: vi.fn(),
  listUpcoming: vi.fn(),
}));

import { listActivity, listUpcoming } from "../api";

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

const noProps = {
  jobs: [] as Job[],
  summaries: [] as SummaryJob[],
};

describe("Activity", () => {
  beforeEach(() => {
    vi.mocked(listActivity).mockReset();
    vi.mocked(listUpcoming).mockReset();
    vi.mocked(listActivity).mockResolvedValue({
      events: [ev()],
      has_more: false,
      retained_max: 2000,
    });
    vi.mocked(listUpcoming).mockResolvedValue({ items: [], truncated: 0 });
  });

  it("renders past events and the now marker", async () => {
    render(<Activity live={[]} {...noProps} />);
    expect(await screen.findByText("A clip")).toBeInTheDocument();
    expect(screen.getByText("now")).toBeInTheDocument();
  });

  it("marks the row by outcome", async () => {
    vi.mocked(listActivity).mockResolvedValue({
      events: [ev({ id: 2, outcome: "fail", summary: "download failed" })],
      has_more: false,
      retained_max: 2000,
    });
    render(<Activity live={[]} {...noProps} />);
    const row = (await screen.findByText("A clip")).closest(
      ".ag-row",
    ) as HTMLElement;
    expect(row.classList.contains("fail")).toBe(true);
  });

  it("renders upcoming items as planned, above the now marker", async () => {
    const items: UpcomingItem[] = [
      // Far-future instant so the label is deterministically "in …" whatever the
      // wall clock is when the test runs — a scheduled task must never read "ago".
      {
        at: "2099-01-01 00:00:00",
        kind: "scan",
        approx: false,
        subject: "Veritasium",
        summary: "channel scan",
      },
    ];
    vi.mocked(listUpcoming).mockResolvedValue({ items, truncated: 0 });
    render(<Activity live={[]} {...noProps} />);
    const row = (await screen.findByText("Veritasium")).closest(
      ".ag-row",
    ) as HTMLElement;
    expect(row.classList.contains("planned")).toBe(true);
    expect(row.textContent).toMatch(/in \d+d/);
    expect(row.textContent).not.toMatch(/ago/);
  });

  it("labels an ordered (untimed) upcoming item 'up next', never 'ago'", async () => {
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [{ kind: "download", approx: true, subject: "Tears of Steel" }],
      truncated: 0,
    });
    render(<Activity live={[]} {...noProps} />);
    const row = (await screen.findByText("Tears of Steel")).closest(
      ".ag-row",
    ) as HTMLElement;
    expect(row.textContent).toContain("up next");
    expect(row.textContent).not.toMatch(/ago/);
  });

  it("shows a running download at the now marker", async () => {
    render(
      <Activity
        live={[]}
        jobs={[
          {
            job_id: 5,
            video_id: "v5",
            title: "Downloading now",
            state: "running",
            priority: 10,
            attempts: 0,
          } as Job,
        ]}
        progressByJobId={{ 5: { percent: 40, speed: "", eta: "01:00" } }}
        summaries={[]}
      />,
    );
    const row = (await screen.findByText("Downloading now")).closest(
      ".ag-row",
    ) as HTMLElement;
    expect(row.classList.contains("running")).toBe(true);
    expect(row.textContent).toMatch(/40%/);
  });

  it("problems-only hides ok events and the future half", async () => {
    const user = userEvent.setup();
    vi.mocked(listActivity).mockResolvedValue({
      events: [
        ev({ id: 3, outcome: "ok", subject: "Fine clip" }),
        ev({
          id: 4,
          outcome: "fail",
          subject: "Broken clip",
          summary: "failed",
        }),
      ],
      has_more: false,
      retained_max: 2000,
    });
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [{ kind: "scan", approx: false, subject: "Scheduled scan" }],
      truncated: 0,
    });
    render(<Activity live={[]} {...noProps} />);
    await screen.findByText("Broken clip");

    await user.click(screen.getByRole("button", { name: /problems only/i }));

    expect(screen.getByText("Broken clip")).toBeInTheDocument();
    expect(screen.queryByText("Fine clip")).not.toBeInTheDocument();
    expect(screen.queryByText("Scheduled scan")).not.toBeInTheDocument();
  });

  it("live-appends a new event by id without a reload", async () => {
    const { rerender } = render(<Activity live={[]} {...noProps} />);
    await screen.findByText("A clip");
    expect(screen.queryByText("Fresh scan")).not.toBeInTheDocument();

    rerender(
      <Activity
        live={[
          ev({ id: 99, kind: "scan", subject: "Fresh scan", summary: "3 new" }),
        ]}
        {...noProps}
      />,
    );
    expect(await screen.findByText("Fresh scan")).toBeInTheDocument();
  });

  it("loads an older page on demand", async () => {
    const user = userEvent.setup();
    vi.mocked(listActivity).mockResolvedValueOnce({
      events: [ev({ id: 10, subject: "Newest" })],
      has_more: true,
      retained_max: 2000,
    });
    render(<Activity live={[]} {...noProps} />);
    await screen.findByText("Newest");

    vi.mocked(listActivity).mockResolvedValueOnce({
      events: [ev({ id: 9, subject: "Older" })],
      has_more: false,
      retained_max: 2000,
    });
    await user.click(screen.getByRole("button", { name: /load older/i }));

    await waitFor(() => {
      expect(screen.getByText("Older")).toBeInTheDocument();
    });
    // The second call paged back from the last shown id.
    expect(listActivity).toHaveBeenLastCalledWith(10);
  });

  it("refetches the projection when the running set changes, avoiding a double-render", async () => {
    vi.mocked(listActivity).mockResolvedValue({
      events: [],
      has_more: false,
      retained_max: 2000,
    });
    // Pending download in the projection → shows once above the now line.
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [{ kind: "download", approx: true, subject: "Tears of Steel" }],
      truncated: 0,
    });
    const { rerender } = render(<Activity live={[]} {...noProps} />);
    expect(await screen.findByText("Tears of Steel")).toBeInTheDocument();

    // The worker claims it: the projection no longer lists it, and App now hands
    // it down as a running job (rendered at the now marker). The refetch keyed on
    // the changed `jobs` must clear the stale projected row so it isn't shown twice.
    vi.mocked(listUpcoming).mockResolvedValue({ items: [], truncated: 0 });
    rerender(
      <Activity
        live={[]}
        jobs={[
          {
            job_id: 1,
            video_id: "ts",
            title: "Tears of Steel",
            state: "running",
            priority: 10,
            attempts: 0,
          } as Job,
        ]}
        summaries={[]}
      />,
    );

    await waitFor(() => {
      expect(screen.getAllByText("Tears of Steel")).toHaveLength(1);
    });
  });
});
