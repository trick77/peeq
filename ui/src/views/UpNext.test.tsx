import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UpNext } from "./UpNext";
import type { Job, SummaryJob } from "../api/types";

vi.mock("../api", () => ({
  listUpcoming: vi.fn(),
}));

import { listUpcoming } from "../api";

function job(overrides: Partial<Job> = {}): Job {
  return {
    job_id: 1,
    video_id: "v1",
    state: "running",
    priority: 0,
    attempts: 0,
    ...overrides,
  } as Job;
}

function summary(overrides: Partial<SummaryJob> = {}): SummaryJob {
  return {
    id: 1,
    video_id: "s1",
    state: "running",
    ...overrides,
  } as SummaryJob;
}

// A scheduled item far enough out to land in a stable bucket.
function soon(minutes: number) {
  return new Date(Date.now() + minutes * 60_000)
    .toISOString()
    .slice(0, 19)
    .replace("T", " ");
}

const noop = () => {};

describe("UpNext", () => {
  beforeEach(() => {
    vi.mocked(listUpcoming).mockReset();
    vi.mocked(listUpcoming).mockResolvedValue({ items: [], truncated: 0 });
  });

  it("names what happens next when there is no work and no schedule", async () => {
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
    expect(
      await screen.findByText(/subscribe to a channel/i),
    ).toBeInTheDocument();
    // A lane heading must not show for an empty lane.
    expect(screen.queryByText("Downloading")).not.toBeInTheDocument();
    expect(screen.queryByText("Summarising")).not.toBeInTheDocument();
  });

  // The schedule starts empty, so "nothing scheduled" is only true once the
  // fetch has settled. Claiming it earlier tells someone with subscribed
  // channels to go subscribe to one.
  it("does not claim nothing is scheduled before the fetch settles", () => {
    vi.mocked(listUpcoming).mockReturnValue(new Promise(() => {}));
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
    expect(screen.getByText("Loading…")).toBeInTheDocument();
    expect(
      screen.queryByText(/subscribe to a channel/i),
    ).not.toBeInTheDocument();
  });

  // A failed fetch means the page has no schedule to speak for — it must not
  // report the absence as "you have nothing scheduled".
  it("says so when the schedule cannot be loaded", async () => {
    vi.mocked(listUpcoming).mockRejectedValue(new Error("boom"));
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
    expect(
      await screen.findByText(/Couldn’t load the schedule/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/subscribe to a channel/i),
    ).not.toBeInTheDocument();
  });

  // A stall is a different silence from idle, and saying only "nothing running"
  // would read as healthy while the queue is frozen.
  it("says peeq is paused, and points at the Resume button that exists", async () => {
    render(
      <UpNext jobs={[]} summaries={[]} onCancel={noop} stalled="youtube" />,
    );
    expect(await screen.findByText(/peeq is paused/i)).toBeInTheDocument();
    expect(screen.getByText(/resume it above/i)).toBeInTheDocument();
  });

  // Each stall has a different way out, and only the kill-switch has a Resume
  // button — telling someone with a full disk to "resume above" would point at
  // an affordance the banner never renders for them.
  it("names the way out for a full disk, not a Resume button", async () => {
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} stalled="disk" />);
    expect(await screen.findByText(/free up space/i)).toBeInTheDocument();
    expect(screen.queryByText(/resume it above/i)).not.toBeInTheDocument();
  });

  it("sends a dead cookie to Settings, not to a Resume button", async () => {
    render(
      <UpNext jobs={[]} summaries={[]} onCancel={noop} stalled="cookie" />,
    );
    expect(
      await screen.findByText(/replace it in settings/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/resume it above/i)).not.toBeInTheDocument();
  });

  it("leads the download lane with the running job, its bar and its eta", async () => {
    render(
      <UpNext
        jobs={[job({ job_id: 9, title: "A Long Video", channel_name: "Chan" })]}
        progressByJobId={{ 9: { percent: 62, speed: "8MiB/s", eta: "00:41" } }}
        summaries={[]}
        onCancel={noop}
      />,
    );
    expect(screen.getByText("Downloading")).toBeInTheDocument();
    const row = screen
      .getByText("A Long Video")
      .closest(".un-row") as HTMLElement;
    expect(row).toHaveClass("hero");
    expect(within(row).getByText("Chan")).toBeInTheDocument();
    // The eta leads the row; the percent and rate sit in the detail line.
    expect(row.querySelector(".un-lead")?.textContent).toBe("00:41");
    expect(row.querySelector(".un-detail")?.textContent).toContain("62%");
    expect(row.querySelector(".un-detail")?.textContent).toContain("8MiB/s");
    expect(row.querySelector(".un-bar > i")).toHaveStyle({ width: "62%" });
  });

  // yt-dlp only reports a speed and an eta once a download is properly under
  // way, so the first ticks arrive with the percent alone. Neither separator
  // may show up on its own then — a trailing "3% ·" reads like a truncation.
  it("shows only the percent while speed and eta are still unknown", () => {
    render(
      <UpNext
        jobs={[job({ job_id: 4, title: "Just started" })]}
        progressByJobId={{ 4: { percent: 3, speed: "", eta: "" } }}
        summaries={[]}
        onCancel={noop}
      />,
    );
    const row = screen
      .getByText("Just started")
      .closest(".un-row") as HTMLElement;
    expect(row.querySelector(".un-detail")?.textContent).toBe("3%");
    // The lead column answers WHEN. With bytes moving but no ETA yet there is
    // no honest answer, so it stays empty — saying "starting" over a bar that
    // is already filling would contradict the row's own progress.
    expect(row.querySelector(".un-lead")?.textContent).toBe("");
  });

  // "starting" is reserved for the one moment it is true: nothing has come back
  // from yt-dlp at all.
  it("says starting only while there is no progress at all", () => {
    render(
      <UpNext
        jobs={[job({ job_id: 6, title: "Not begun" })]}
        summaries={[]}
        onCancel={noop}
      />,
    );
    const row = screen.getByText("Not begun").closest(".un-row") as HTMLElement;
    expect(row.querySelector(".un-lead")?.textContent).toBe("starting");
  });

  // Replaces Queue's "no bar" assertion: the bar is always in the slot now, so
  // that the row's height doesn't jump when the first progress event lands.
  it("gives a job with no progress yet an idle stub bar", () => {
    render(
      <UpNext
        jobs={[job({ job_id: 5, title: "No ticks yet" })]}
        summaries={[]}
        onCancel={noop}
      />,
    );
    const row = screen
      .getByText("No ticks yet")
      .closest(".un-row") as HTMLElement;
    expect(row.querySelector(".un-bar")).toHaveClass("stub");
    expect(row.querySelector(".un-bar > i")).toHaveStyle({ width: "0%" });
  });

  // Ranks across two independent lanes would imply a comparison that doesn't
  // exist — a waiting summary does not hold up a download.
  it("reads a waiting download as 'then', not as a rank", () => {
    render(
      <UpNext
        jobs={[job({ job_id: 2, state: "pending", title: "Waiting one" })]}
        summaries={[]}
        onCancel={noop}
      />,
    );
    const row = screen
      .getByText("Waiting one")
      .closest(".un-row") as HTMLElement;
    expect(row.querySelector(".un-lead")?.textContent).toBe("then");
    expect(row).not.toHaveClass("hero");
    expect(row.querySelector(".un-bar")).toBeNull();
  });

  it("cancels a download by its job id", async () => {
    const user = userEvent.setup();
    const onCancel = vi.fn();
    render(
      <UpNext
        jobs={[job({ job_id: 7, title: "Cancelme" })]}
        summaries={[]}
        onCancel={onCancel}
      />,
    );
    await user.click(screen.getByRole("button", { name: /cancel/i }));
    expect(onCancel).toHaveBeenCalledWith(7);
  });

  it("cancels a waiting download too", async () => {
    const user = userEvent.setup();
    const onCancel = vi.fn();
    render(
      <UpNext
        jobs={[job({ job_id: 8, state: "pending", title: "Queued one" })]}
        summaries={[]}
        onCancel={onCancel}
      />,
    );
    await user.click(screen.getByRole("button", { name: /cancel/i }));
    expect(onCancel).toHaveBeenCalledWith(8);
  });

  it("shows a summary's step as a segmented bar, with no actions", () => {
    render(
      <UpNext
        jobs={[]}
        summaries={[summary({ id: 3, video_id: "s3", title: "Being done" })]}
        summaryPhaseByVideoId={{ s3: "embedding" }}
        onCancel={noop}
      />,
    );
    expect(screen.getByText("Summarising")).toBeInTheDocument();
    const row = screen
      .getByText("Being done")
      .closest(".un-row") as HTMLElement;
    expect(within(row).getByText("Embedding")).toBeInTheDocument();
    // Four segments in the same slot the download bar uses, and the step is
    // named in words rather than as a bare "3/4".
    expect(row.querySelectorAll(".un-step")).toHaveLength(4);
    expect(row.querySelector(".un-lead")?.textContent).toBe("step 3 of 4");
    // The summarize lane offers no cancel — summaries run unattended.
    expect(within(row).queryByRole("button")).toBeNull();
  });

  it("falls back to a state-derived phase word without an SSE phase yet", () => {
    render(
      <UpNext
        jobs={[]}
        summaries={[
          summary({
            id: 4,
            video_id: "s4",
            title: "Just started",
            state: "running",
          }),
          summary({
            id: 5,
            video_id: "s5",
            title: "Still waiting",
            state: "pending",
          }),
        ]}
        onCancel={noop}
      />,
    );
    const running = screen
      .getByText("Just started")
      .closest(".un-row") as HTMLElement;
    const pending = screen
      .getByText("Still waiting")
      .closest(".un-row") as HTMLElement;
    expect(running.querySelector(".un-lead")?.textContent).toBe("step 1 of 4");
    expect(within(pending).getByText("Waiting")).toBeInTheDocument();
    expect(pending.querySelector(".un-lead")?.textContent).toBe("then");
  });

  it("groups the timed schedule by how far off it is", async () => {
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [
        {
          kind: "scan",
          approx: false,
          at: soon(20),
          subject: "Veritasium",
          summary: "channel scan",
        },
        {
          kind: "channel_meta",
          approx: false,
          at: soon(300),
          subject: "Kurzgesagt",
          summary: "metadata refresh",
        },
      ],
      truncated: 0,
    });
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
    expect(await screen.findByText("Within the hour")).toBeInTheDocument();
    expect(screen.getByText("Later today")).toBeInTheDocument();
    expect(screen.getByText("Veritasium")).toBeInTheDocument();
    expect(screen.getByText("Kurzgesagt")).toBeInTheDocument();
  });

  // The lanes render queued jobs from live state with real progress, so an
  // untimed projection row for the same job would be a visible duplicate. The
  // endpoint stopped sending them; this guards the client against an older one.
  it("never renders an approximate projection row", async () => {
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [
        {
          kind: "download",
          approx: true,
          subject: "Tears of Steel",
          summary: "download",
        },
        {
          kind: "scan",
          approx: false,
          at: soon(20),
          subject: "Veritasium",
          summary: "channel scan",
        },
      ],
      truncated: 0,
    });
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
    expect(await screen.findByText("Veritasium")).toBeInTheDocument();
    expect(screen.queryByText("Tears of Steel")).not.toBeInTheDocument();
  });

  it("links a scheduled channel name to its page", async () => {
    const onOpenChannel = vi.fn();
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [
        {
          kind: "scan",
          approx: false,
          at: soon(20),
          subject: "Veritasium",
          subject_id: "UCx",
          summary: "channel scan",
        },
      ],
      truncated: 0,
    });
    const user = userEvent.setup();
    render(
      <UpNext
        jobs={[]}
        summaries={[]}
        onCancel={noop}
        onOpenChannel={onOpenChannel}
      />,
    );
    await user.click(await screen.findByRole("button", { name: "Veritasium" }));
    expect(onOpenChannel).toHaveBeenCalledWith("UCx");
  });

  it("refetches the schedule when the live job set changes", async () => {
    const { rerender } = render(
      <UpNext jobs={[]} summaries={[]} onCancel={noop} />,
    );
    await waitFor(() => expect(listUpcoming).toHaveBeenCalledTimes(1));
    rerender(
      <UpNext jobs={[job({ job_id: 1 })]} summaries={[]} onCancel={noop} />,
    );
    await waitFor(() => expect(listUpcoming).toHaveBeenCalledTimes(2));
  });

  // App's 3-second poll hands down a fresh array every tick while either lane
  // has work. Keying the refetch on array identity would hit
  // /api/activity/upcoming every 3 seconds for the whole length of a download,
  // for a projection that only moves when a job changes state.
  it("does not refetch when an unchanged job set arrives as a new array", async () => {
    const { rerender } = render(
      <UpNext jobs={[job({ job_id: 1 })]} summaries={[]} onCancel={noop} />,
    );
    await waitFor(() => expect(listUpcoming).toHaveBeenCalledTimes(1));
    // Same job, same state — a new array object, as the poll produces.
    rerender(
      <UpNext jobs={[job({ job_id: 1 })]} summaries={[]} onCancel={noop} />,
    );
    rerender(
      <UpNext jobs={[job({ job_id: 1 })]} summaries={[]} onCancel={noop} />,
    );
    expect(listUpcoming).toHaveBeenCalledTimes(1);

    // A state transition IS a reason to refetch.
    rerender(
      <UpNext
        jobs={[job({ job_id: 1, state: "pending" })]}
        summaries={[]}
        onCancel={noop}
      />,
    );
    await waitFor(() => expect(listUpcoming).toHaveBeenCalledTimes(2));
  });

  it("hints at scheduled work beyond the server's cap", async () => {
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [
        {
          kind: "scan",
          approx: false,
          at: soon(20),
          subject: "Veritasium",
          summary: "channel scan",
        },
      ],
      truncated: 4,
    });
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
    expect(await screen.findByText("+4 more scheduled")).toBeInTheDocument();
  });
});

describe("UpNext schedule rows", () => {
  beforeEach(() => {
    vi.mocked(listUpcoming).mockReset();
    vi.mocked(listUpcoming).mockResolvedValue({ items: [], truncated: 0 });
  });

  // The kind used to be a second line under every channel name, so a real
  // schedule read as "Channel scan" fifteen times. It is a glyph now, in its
  // own column, leaving one line per entry.
  it("names a scheduled row's kind with a glyph, not a repeated sentence", async () => {
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [
        {
          kind: "scan",
          approx: false,
          at: soon(20),
          subject: "Veritasium",
          summary: "channel scan",
        },
      ],
      truncated: 0,
    });
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
    await screen.findByText("Veritasium");
    const row = screen
      .getByText("Veritasium")
      .closest(".un-row") as HTMLElement;
    expect(row.querySelector(".un-kind")).toBeTruthy();
    // The words are gone from the row, but the kind is still named for anyone
    // not reading it visually.
    expect(row.textContent).not.toContain("Channel scan");
    expect(within(row).getByLabelText("Scan")).toBeInTheDocument();
  });
});

describe("UpNext filters", () => {
  beforeEach(() => {
    vi.mocked(listUpcoming).mockReset();
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [
        {
          kind: "scan",
          approx: false,
          at: soon(20),
          subject: "Veritasium",
          summary: "channel scan",
        },
      ],
      truncated: 0,
    });
  });

  it("shows everything under All, which is the default", async () => {
    render(
      <UpNext
        jobs={[job({ job_id: 1, title: "A download" })]}
        summaries={[summary({ id: 2, video_id: "s2", title: "A summary" })]}
        onCancel={noop}
      />,
    );
    expect(await screen.findByText("Veritasium")).toBeInTheDocument();
    expect(screen.getByText("A download")).toBeInTheDocument();
    expect(screen.getByText("A summary")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "All" })).toHaveClass("on");
  });

  it("narrows to one kind, hiding the other lane and the schedule", async () => {
    const user = userEvent.setup();
    render(
      <UpNext
        jobs={[job({ job_id: 1, title: "A download" })]}
        summaries={[summary({ id: 2, video_id: "s2", title: "A summary" })]}
        onCancel={noop}
      />,
    );
    await screen.findByText("Veritasium");
    await user.click(screen.getByRole("button", { name: "Downloads" }));
    expect(screen.getByText("A download")).toBeInTheDocument();
    expect(screen.queryByText("A summary")).not.toBeInTheDocument();
    expect(screen.queryByText("Veritasium")).not.toBeInTheDocument();
  });

  it("filters the schedule by its own kind", async () => {
    const user = userEvent.setup();
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
    await screen.findByText("Veritasium");
    await user.click(screen.getByRole("button", { name: "Metadata" }));
    expect(screen.queryByText("Veritasium")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Scans" }));
    expect(await screen.findByText("Veritasium")).toBeInTheDocument();
  });

  // An early return without the chips would strand anyone who filtered to a
  // kind with nothing in it — the page would say "nothing" with no way back.
  it("keeps the chips when the filter matches nothing", async () => {
    const user = userEvent.setup();
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
    await screen.findByText("Veritasium");
    await user.click(screen.getByRole("button", { name: "Summaries" }));
    expect(screen.getByText(/nothing of that kind/i)).toBeInTheDocument();
    // Still there, so All can be chosen again.
    expect(screen.getByRole("button", { name: "All" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "All" }));
    expect(await screen.findByText("Veritasium")).toBeInTheDocument();
  });
});
