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

  it("renders past events under the Recent activity section", async () => {
    render(<Activity live={[]} {...noProps} />);
    expect(await screen.findByText("A clip")).toBeInTheDocument();
    expect(screen.getByText("Recent activity")).toBeInTheDocument();
    // The folded "now" marker is gone; nothing is running or queued here, so
    // there is no "Up next" section either.
    expect(screen.queryByText("Up next")).not.toBeInTheDocument();
    expect(screen.queryByText("now")).not.toBeInTheDocument();
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

  it("renders upcoming items as planned, in the Up next section", async () => {
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
    expect(screen.getByText("Up next")).toBeInTheDocument();
    expect(row.classList.contains("planned")).toBe(true);
    expect(row.textContent).toMatch(/in \d+d/);
    expect(row.textContent).not.toMatch(/ago/);
  });

  it("puts Recent activity above Up next when both render", async () => {
    // The log leads the page; the projection follows. Asserted on DOM order
    // rather than presence, since both sections render either way.
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [
        {
          at: "2099-01-01 00:00:00",
          kind: "scan",
          approx: false,
          subject: "Veritasium",
          summary: "channel scan",
        },
      ],
      truncated: 0,
    });
    render(<Activity live={[]} {...noProps} />);
    await screen.findByText("Up next");
    const titles = Array.from(document.querySelectorAll(".ag-sec-title")).map(
      (n) => n.textContent,
    );
    expect(titles).toEqual(["Recent activity", "Up next"]);
  });

  it("shows an overdue planned item as 'soon', never 'ago'", async () => {
    // A scheduled instant already in the past (the worker hasn't reached it —
    // e.g. YouTube is paused) is still future work; it must read "soon".
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [
        {
          at: "2020-01-01 00:00:00",
          kind: "scan",
          approx: false,
          subject: "Overdue scan",
          summary: "channel scan",
        },
      ],
      truncated: 0,
    });
    render(<Activity live={[]} {...noProps} />);
    const row = (await screen.findByText("Overdue scan")).closest(
      ".ag-row",
    ) as HTMLElement;
    expect(row.textContent).toContain("soon");
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

  it("problems-only hides ok events, the future half, and running work", async () => {
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
    render(
      <Activity
        live={[]}
        jobs={[
          {
            job_id: 8,
            video_id: "v8",
            title: "Healthy download",
            state: "running",
            priority: 10,
            attempts: 0,
          } as Job,
        ]}
        summaries={[]}
      />,
    );
    await screen.findByText("Broken clip");
    // A healthy running download shows under "All"…
    expect(screen.getByText("Healthy download")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /problems only/i }));

    expect(screen.getByText("Broken clip")).toBeInTheDocument();
    expect(screen.queryByText("Fine clip")).not.toBeInTheDocument();
    expect(screen.queryByText("Scheduled scan")).not.toBeInTheDocument();
    // …but not under "Problems only" — in-progress work is not a problem.
    expect(screen.queryByText("Healthy download")).not.toBeInTheDocument();
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

  it("caps history at the newest 10 and hints at the earlier rows", async () => {
    // Server returns newest-first; the agenda keeps only the newest 10.
    const events = Array.from({ length: 12 }, (_, i) =>
      ev({ id: 100 - i, subject: `Clip ${i}` }),
    );
    vi.mocked(listActivity).mockResolvedValue({
      events,
      has_more: false,
      retained_max: 2000,
    });
    render(<Activity live={[]} {...noProps} />);
    await screen.findByText("Clip 0");
    // Newest 10 shown (Clip 0–9); the oldest two are hidden behind a "+N" edge.
    expect(screen.getByText("Clip 9")).toBeInTheDocument();
    expect(screen.queryByText("Clip 10")).not.toBeInTheDocument();
    expect(screen.getByText("+2 earlier")).toBeInTheDocument();
  });

  it("caps planned at the nearest 10 and hints at the rest", async () => {
    const items: UpcomingItem[] = Array.from({ length: 12 }, (_, i) => ({
      kind: "scan",
      approx: true,
      subject: `Scan ${i}`,
    }));
    vi.mocked(listUpcoming).mockResolvedValue({ items, truncated: 0 });
    render(<Activity live={[]} {...noProps} />);
    await screen.findByText("Scan 0");
    // Soonest 10 shown (Scan 0–9); the rest fold into the "+N more scheduled" edge.
    expect(screen.getByText("Scan 9")).toBeInTheDocument();
    expect(screen.queryByText("Scan 10")).not.toBeInTheDocument();
    expect(screen.getByText("+2 more scheduled")).toBeInTheDocument();
  });

  it("folds the server's own projection cap into the scheduled hint", async () => {
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [{ kind: "scan", approx: true, subject: "Only scan" }],
      truncated: 5,
    });
    render(<Activity live={[]} {...noProps} />);
    await screen.findByText("Only scan");
    expect(screen.getByText("+5 more scheduled")).toBeInTheDocument();
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

  it("links a channel name in Up next to its page", async () => {
    const onOpenChannel = vi.fn();
    const user = userEvent.setup();
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [
        {
          at: "2026-07-25 08:00:00",
          kind: "scan",
          approx: false,
          subject_id: "UCea",
          subject: "Everyday Astronaut",
          summary: "channel scan",
        } as UpcomingItem,
      ],
      truncated: 0,
    });
    render(
      <Activity live={[]} {...noProps} onOpenChannel={onOpenChannel} />,
    );

    await user.click(
      await screen.findByRole("button", { name: "Everyday Astronaut" }),
    );

    expect(onOpenChannel).toHaveBeenCalledWith("UCea");
  });

  it("links a channel name in Recent activity too", async () => {
    // Both halves of the agenda or neither: a linked name above the line and
    // dead text below it is exactly the drift this avoids.
    const onOpenChannel = vi.fn();
    const user = userEvent.setup();
    vi.mocked(listActivity).mockResolvedValue({
      events: [
        ev({
          id: 9,
          kind: "scan",
          subject_id: "UCea",
          subject: "Everyday Astronaut",
          summary: "checked on request",
          detail: "nothing new",
        }),
      ],
      has_more: false,
      retained_max: 2000,
    });
    render(
      <Activity live={[]} {...noProps} onOpenChannel={onOpenChannel} />,
    );

    await user.click(
      await screen.findByRole("button", { name: "Everyday Astronaut" }),
    );

    expect(onOpenChannel).toHaveBeenCalledWith("UCea");
  });

  it("leaves a video subject as plain text", async () => {
    // Download and summary rows name a video; the agenda is a log of work, not
    // a video browser, so those must not become links.
    const onOpenChannel = vi.fn();
    vi.mocked(listActivity).mockResolvedValue({
      events: [ev({ id: 10, kind: "download", subject: "A clip" })],
      has_more: false,
      retained_max: 2000,
    });
    render(
      <Activity live={[]} {...noProps} onOpenChannel={onOpenChannel} />,
    );

    expect(await screen.findByText("A clip")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "A clip" }),
    ).not.toBeInTheDocument();
  });

  it("renders a channel name as plain text when no navigation is wired", async () => {
    vi.mocked(listActivity).mockResolvedValue({
      events: [
        ev({ id: 11, kind: "scan", subject_id: "UCea", subject: "EA" }),
      ],
      has_more: false,
      retained_max: 2000,
    });
    render(<Activity live={[]} {...noProps} />);

    expect(await screen.findByText("EA")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "EA" })).not.toBeInTheDocument();
  });
});
