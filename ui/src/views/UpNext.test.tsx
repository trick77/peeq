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
    // A group heading must not show for an empty group.
    expect(screen.queryByText("Now")).not.toBeInTheDocument();
    expect(screen.queryByText("Queued")).not.toBeInTheDocument();
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

  it("puts a running download under Now, with its bar and its eta", async () => {
    render(
      <UpNext
        jobs={[job({ job_id: 9, title: "A Long Video", channel_name: "Chan" })]}
        progressByJobId={{ 9: { percent: 62, speed: "8MiB/s", eta: "00:41" } }}
        summaries={[]}
        onCancel={noop}
      />,
    );
    expect(screen.getByText("Now")).toBeInTheDocument();
    const row = screen
      .getByText("A Long Video")
      .closest(".ag-row") as HTMLElement;
    // `live` is the running ring, History's ok/warn/fail in the future tense.
    expect(row).toHaveClass("live");
    expect(within(row).getByText("Downloading")).toBeInTheDocument();
    // The channel sits beside the kind word on the detail line — plain text
    // here, a link once onOpenChannel is wired.
    expect(row.querySelector(".ag-detail")?.textContent).toContain("Chan");
    // The eta sits in the gutter History puts a wall clock in; the percent and
    // rate sit in the second detail line, under the bar.
    expect(row.querySelector(".ag-clock")?.textContent).toBe("00:41");
    expect(row.textContent).toContain("62%");
    expect(row.textContent).toContain("8MiB/s");
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
      .closest(".ag-row") as HTMLElement;
    const details = row.querySelectorAll(".ag-detail");
    expect(details[details.length - 1].textContent).toBe("3%");
    // The gutter answers WHEN on every row. With bytes moving but no ETA yet
    // the honest answer is the bare tense, not a blank.
    expect(row.querySelector(".ag-clock")?.textContent).toBe("now");
  });

  // Before yt-dlp has said anything at all, the row says what it is waiting on
  // rather than putting a guess in the gutter.
  it("says what a not-yet-started download is waiting on", () => {
    render(
      <UpNext
        jobs={[job({ job_id: 6, title: "Not begun" })]}
        summaries={[]}
        onCancel={noop}
      />,
    );
    const row = screen.getByText("Not begun").closest(".ag-row") as HTMLElement;
    expect(within(row).getByText("Contacting YouTube")).toBeInTheDocument();
    expect(row.querySelector(".ag-clock")?.textContent).toBe("now");
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
      .closest(".ag-row") as HTMLElement;
    expect(row.querySelector(".un-bar")).toHaveClass("stub");
    expect(row.querySelector(".un-bar > i")).toHaveStyle({ width: "0%" });
  });

  // Ranks across two independent lanes would imply a comparison that doesn't
  // exist — a waiting summary does not hold up a download. The group heading
  // carries position instead, and a queued row shows no progress of its own.
  it("files a waiting download under Queued, with no bar and no rank", () => {
    render(
      <UpNext
        jobs={[job({ job_id: 2, state: "pending", title: "Waiting one" })]}
        summaries={[]}
        onCancel={noop}
      />,
    );
    expect(screen.getByText("Queued")).toBeInTheDocument();
    expect(screen.queryByText("Now")).not.toBeInTheDocument();
    const row = screen
      .getByText("Waiting one")
      .closest(".ag-row") as HTMLElement;
    expect(row).not.toHaveClass("live");
    expect(row.querySelector(".un-bar")).toBeNull();
    expect(row.querySelector(".ag-clock")?.textContent).toBe("then");
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
    const row = screen
      .getByText("Being done")
      .closest(".ag-row") as HTMLElement;
    expect(within(row).getByText("Summarising")).toBeInTheDocument();
    expect(within(row).getByText("Embedding")).toBeInTheDocument();
    // Four segments in the same slot the download bar uses, and the step is
    // named in words rather than as a bare "3/4".
    expect(row.querySelectorAll(".un-step")).toHaveLength(4);
    expect(row.querySelector(".ag-when")?.textContent).toBe("step 3 of 4");
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
      .closest(".ag-row") as HTMLElement;
    const pending = screen
      .getByText("Still waiting")
      .closest(".ag-row") as HTMLElement;
    expect(running.querySelector(".ag-when")?.textContent).toBe("step 1 of 4");
    expect(pending.querySelector(".ag-when")?.textContent).toBe("waiting");
    expect(pending.querySelector(".un-steps")).toBeNull();
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
  it("opens a queued video in the player from its title", async () => {
    const onOpenVideo = vi.fn();
    const user = userEvent.setup();
    render(
      <UpNext
        jobs={[job({ job_id: 11, video_id: "vid11", title: "Clickable" })]}
        summaries={[]}
        onCancel={noop}
        onOpenVideo={onOpenVideo}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Clickable" }));
    expect(onOpenVideo).toHaveBeenCalledWith("vid11");
  });

  // Everything this page can show is already in memory, so the box filters
  // client-side — across both lanes and the schedule at once.
  describe("search", () => {
    const items = [
      {
        kind: "scan",
        approx: false,
        at: soon(20),
        subject: "Veritasium",
        summary: "channel scan",
      },
    ];

    it("narrows the lanes and the schedule together", async () => {
      vi.mocked(listUpcoming).mockResolvedValue({ items, truncated: 0 });
      render(
        <UpNext
          jobs={[
            job({ job_id: 20, title: "Keep me" }),
            job({ job_id: 21, state: "pending", title: "Drop me" }),
          ]}
          summaries={[]}
          onCancel={noop}
          search="keep"
        />,
      );
      await waitFor(() =>
        expect(screen.getByText("Keep me")).toBeInTheDocument(),
      );
      expect(screen.queryByText("Drop me")).not.toBeInTheDocument();
      // The scheduled scan doesn't match either, so its bucket goes with it.
      expect(screen.queryByText("Veritasium")).not.toBeInTheDocument();
      expect(screen.queryByText("Within the hour")).not.toBeInTheDocument();
    });

    it("matches a channel name, not only a title", () => {
      render(
        <UpNext
          jobs={[
            job({ job_id: 22, title: "Some video", channel_name: "Kurz" }),
          ]}
          summaries={[]}
          onCancel={noop}
          search="kurz"
        />,
      );
      expect(screen.getByText("Some video")).toBeInTheDocument();
    });

    // A query that matches nothing must say so — an empty timeline would read
    // as "peeq has nothing to do", which is a different and alarming claim.
    it("says nothing matches rather than looking idle", async () => {
      vi.mocked(listUpcoming).mockResolvedValue({ items, truncated: 0 });
      render(
        <UpNext
          jobs={[job({ job_id: 23, title: "Some video" })]}
          summaries={[]}
          onCancel={noop}
          search="zzz"
        />,
      );
      expect(
        await screen.findByText(/Nothing queued or scheduled matches/),
      ).toBeInTheDocument();
      expect(screen.queryByText(/subscribe to a channel/i)).toBeNull();
    });
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
      .closest(".ag-row") as HTMLElement;
    // The glyph column is History's node now, not a one-off .un-kind span.
    expect(row.querySelector(".ag-node")).toBeTruthy();
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

  // The server's count is a cross-kind drop count, so it can only be told
  // truthfully under All — under Downloads it would sit alone beneath a view
  // with no schedule in it at all.
  it("drops the truncation hint once the filter narrows", async () => {
    const user = userEvent.setup();
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
    render(
      <UpNext
        jobs={[job({ job_id: 1, title: "A download" })]}
        summaries={[]}
        onCancel={noop}
      />,
    );
    expect(await screen.findByText("+4 more scheduled")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Downloads" }));
    expect(screen.queryByText("+4 more scheduled")).not.toBeInTheDocument();
  });

  // A failed schedule fetch is not news under a filter the schedule is not in:
  // the schedule holds no downloads whether it loaded or not.
  it("does not blame the schedule under a filter it has no part in", async () => {
    const user = userEvent.setup();
    vi.mocked(listUpcoming).mockRejectedValue(new Error("nope"));
    render(
      <UpNext
        jobs={[job({ job_id: 1, title: "A download" })]}
        summaries={[]}
        onCancel={noop}
      />,
    );
    await screen.findByText(/Couldn’t load the schedule|A download/i);
    await user.click(screen.getByRole("button", { name: "Summaries" }));
    expect(
      screen.queryByText(/Couldn’t load the schedule/i),
    ).not.toBeInTheDocument();
    expect(screen.getByText(/nothing of that kind/i)).toBeInTheDocument();
  });
  // Two narrowing controls now sit on this page, and they have to compose: the
  // chips decide which lanes are on it at all, the box which of their rows
  // survive. A search evaluated around the chips would report on work the
  // chips are hiding.
  it("composes with the kind chips, and names both when both are on", async () => {
    const user = userEvent.setup();
    render(
      <UpNext
        jobs={[job({ job_id: 1, title: "A download" })]}
        summaries={[summary({ id: 2, video_id: "s2", title: "A summary" })]}
        onCancel={noop}
        search="summary"
      />,
    );
    // Under All the query finds the summary.
    expect(await screen.findByText("A summary")).toBeInTheDocument();
    expect(screen.queryByText("A download")).not.toBeInTheDocument();

    // Under Downloads the summary lane is off the page entirely, so the same
    // query now matches nothing — and the message must not blame the box alone.
    await user.click(screen.getByRole("button", { name: "Downloads" }));
    expect(screen.queryByText("A summary")).not.toBeInTheDocument();
    expect(
      screen.getByText(/nothing of that kind matches/i),
    ).toBeInTheDocument();

    // The chips survive it, so there is a way back.
    await user.click(screen.getByRole("button", { name: "All" }));
    expect(await screen.findByText("A summary")).toBeInTheDocument();
  });

  // A search that matches nothing is not the same claim as "peeq has nothing to
  // do" — the work is there, you filtered past it.
  it("does not offer to subscribe when the box is what emptied the page", async () => {
    render(
      <UpNext
        jobs={[job({ job_id: 1, title: "A download" })]}
        summaries={[]}
        onCancel={noop}
        search="zzz"
      />,
    );
    expect(
      await screen.findByText(/Nothing queued or scheduled matches/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/subscribe to a channel/i)).toBeNull();
  });
});
