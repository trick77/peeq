import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UpNext } from "./UpNext";
import type { Job, SummaryJob } from "../api/types";

vi.mock("../api", () => ({
  listUpcoming: vi.fn(),
  listFailedSummaries: vi.fn(),
  retryFailedSummaries: vi.fn(),
  skipScheduledScan: vi.fn(),
  skipScheduledMeta: vi.fn(),
}));

import {
  listFailedSummaries,
  listUpcoming,
  retryFailedSummaries,
  skipScheduledMeta,
  skipScheduledScan,
} from "../api";

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
    vi.mocked(listFailedSummaries).mockReset();
    vi.mocked(listFailedSummaries).mockResolvedValue([]);
    vi.mocked(retryFailedSummaries).mockReset();
    vi.mocked(retryFailedSummaries).mockResolvedValue({ requeued: 0 });
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
  it("says Peeq is paused, and points at the Resume button that exists", async () => {
    render(
      <UpNext jobs={[]} summaries={[]} onCancel={noop} stalled="youtube" />,
    );
    expect(await screen.findByText(/Peeq is paused/i)).toBeInTheDocument();
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

  // A video added by URL is queued before anything is known about it: the
  // worker reads its title and channel only when it claims the job. The row
  // used to print the raw YouTube id in the title slot, which asks the reader
  // to recognise a video by its id.
  describe("a video whose details haven't arrived", () => {
    it("says what peeq is doing instead of showing the id as a title", () => {
      render(
        <UpNext
          jobs={[job({ job_id: 30, state: "pending", video_id: "abc123" })]}
          summaries={[]}
          onCancel={noop}
        />,
      );
      const row = screen
        .getByText("Reading details from YouTube")
        .closest(".ag-row") as HTMLElement;
      expect(row.querySelector(".ag-subject")).toHaveClass("pending");
      // Not a link into the player: there is nothing there to open yet.
      expect(
        within(row).queryByRole("button", {
          name: "Reading details from YouTube",
        }),
      ).not.toBeInTheDocument();
      // The id keeps its place one line down, as the receipt for what was
      // pasted, pointing at the video on YouTube.
      expect(
        within(row).getByRole("link", { name: "youtu.be/abc123" }),
      ).toHaveAttribute("href", "https://www.youtube.com/watch?v=abc123");
    });

    // The download worker is single-threaded and stops entirely on a missing
    // cookie, low disk, or the kill-switch. "Reading details" would then be a
    // claim about work nobody is doing — and a placeholder that pulses forever
    // is worse than the id it replaced.
    it("drops the fetching claim while YouTube work is stopped", () => {
      render(
        <UpNext
          jobs={[job({ job_id: 31, state: "pending", video_id: "abc123" })]}
          summaries={[]}
          onCancel={noop}
          stalled="cookie"
        />,
      );
      const row = screen
        .getByText("Waiting to read details")
        .closest(".ag-row") as HTMLElement;
      expect(row.querySelector(".ag-subject")).not.toHaveClass("pending");
      expect(
        within(row).getByRole("link", { name: "youtu.be/abc123" }),
      ).toBeInTheDocument();
    });

    // The stall flag is global, but a job that is already running keeps running
    // when the cookie expires or the disk fills under it. Such a row IS reading
    // details, and saying otherwise contradicts its own progress line.
    it("keeps the fetching claim on a row that is already running", () => {
      render(
        <UpNext
          jobs={[job({ job_id: 34, state: "running", video_id: "abc123" })]}
          summaries={[]}
          onCancel={noop}
          stalled="disk"
        />,
      );
      expect(
        screen.getByText("Reading details from YouTube"),
      ).toBeInTheDocument();
      expect(screen.queryByText("Waiting to read details")).toBeNull();
    });

    // The id is on screen, so the search box must still reach it.
    it("is still findable by its id", () => {
      render(
        <UpNext
          jobs={[
            job({ job_id: 32, state: "pending", video_id: "abc123" }),
            job({ job_id: 33, state: "pending", title: "Something else" }),
          ]}
          summaries={[]}
          onCancel={noop}
          search="abc123"
        />,
      );
      expect(
        screen.getByText("Reading details from YouTube"),
      ).toBeInTheDocument();
      expect(screen.queryByText("Something else")).not.toBeInTheDocument();
    });
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
    // named in words rather than as a bare "4/4". Embedding is the LAST stage:
    // it moved after key points so chapter chunks can be built from the
    // chapters that step writes.
    expect(row.querySelectorAll(".un-step")).toHaveLength(4);
    expect(row.querySelector(".ag-when")?.textContent).toBe("step 4 of 4");
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

    // The scheduled row carries the work in words again, so the box has to
    // reach them: a field the row displays and the search refuses to find is
    // the same broken promise as matching text the row cannot show.
    it("matches a scheduled row's summary, not only its subject", async () => {
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
      render(
        <UpNext
          jobs={[]}
          summaries={[]}
          onCancel={noop}
          search="channel scan"
        />,
      );
      expect(await screen.findByText("Veritasium")).toBeInTheDocument();
      expect(
        screen.queryByText(/Nothing queued or scheduled matches/),
      ).toBeNull();
    });

    // The fallback line is displayed text like any other, so the same promise
    // covers it: with no summary the row reads "Metadata", and a search for
    // that word has to keep the row rather than hide the very thing it names.
    it("matches a scheduled row's fallback kind label", async () => {
      vi.mocked(listUpcoming).mockResolvedValue({
        items: [
          {
            kind: "channel_meta",
            approx: false,
            at: soon(20),
            subject: "Veritasium",
          },
        ],
        truncated: 0,
      });
      render(
        <UpNext jobs={[]} summaries={[]} onCancel={noop} search="metadata" />,
      );
      expect(await screen.findByText("Veritasium")).toBeInTheDocument();
      expect(
        screen.queryByText(/Nothing queued or scheduled matches/),
      ).toBeNull();
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
    vi.mocked(listFailedSummaries).mockReset();
    vi.mocked(listFailedSummaries).mockResolvedValue([]);
    vi.mocked(retryFailedSummaries).mockReset();
    vi.mocked(retryFailedSummaries).mockResolvedValue({ requeued: 0 });
  });

  // The row says what is about to happen, in the worker's own words, on a
  // second line — History's shape. A glyph alone names a kind, not a deed: a
  // magnifying glass beside a channel name never said "about to look for new
  // videos". The glyph stays as the a11y name for the same fact.
  it("names a scheduled row's work in words, under the subject", async () => {
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
    // Capitalised by leadCap, and in the same .ag-kind slot History leads its
    // detail line with — never a second vocabulary invented in the view.
    expect(within(row).getByText("Channel scan")).toHaveClass("ag-kind");
    expect(within(row).getByLabelText("Scan")).toBeInTheDocument();
  });

  // The wording is the worker's, so the row has to cope with the worker not
  // supplying it — an older backend, or a kind added there before it is given a
  // phrase here. The kind's own label stands in, which is what the glyph
  // already means, rather than leaving the line blank and the row silent about
  // what it is for.
  it("falls back to the kind label when the item carries no summary", async () => {
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [
        {
          kind: "channel_meta",
          approx: false,
          at: soon(30),
          subject: "Kurzgesagt",
        },
      ],
      truncated: 0,
    });
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
    await screen.findByText("Kurzgesagt");
    const row = screen
      .getByText("Kurzgesagt")
      .closest(".ag-row") as HTMLElement;
    expect(within(row).getByText("Metadata")).toHaveClass("ag-kind");
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
    expect(screen.getByRole("button", { name: /^All\b/ })).toHaveClass("on");
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
    await user.click(screen.getByRole("button", { name: /^Downloads\b/ }));
    expect(screen.getByText("A download")).toBeInTheDocument();
    expect(screen.queryByText("A summary")).not.toBeInTheDocument();
    expect(screen.queryByText("Veritasium")).not.toBeInTheDocument();
  });

  it("filters the schedule by its own kind", async () => {
    const user = userEvent.setup();
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
    await screen.findByText("Veritasium");
    await user.click(screen.getByRole("button", { name: /^Metadata\b/ }));
    expect(screen.queryByText("Veritasium")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^Scans\b/ }));
    expect(await screen.findByText("Veritasium")).toBeInTheDocument();
  });

  // An early return without the chips would strand anyone who filtered to a
  // kind with nothing in it — the page would say "nothing" with no way back.
  it("keeps the chips when the filter matches nothing", async () => {
    const user = userEvent.setup();
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
    await screen.findByText("Veritasium");
    await user.click(screen.getByRole("button", { name: /^Summaries\b/ }));
    expect(screen.getByText(/nothing of that kind/i)).toBeInTheDocument();
    // Still there, so All can be chosen again.
    expect(screen.getByRole("button", { name: /^All\b/ })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^All\b/ }));
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
    await user.click(screen.getByRole("button", { name: /^Downloads\b/ }));
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
    await user.click(screen.getByRole("button", { name: /^Summaries\b/ }));
    expect(
      screen.queryByText(/Couldn’t load the schedule/i),
    ).not.toBeInTheDocument();
    expect(screen.getByText(/nothing of that kind/i)).toBeInTheDocument();
  });
  // Each chip carries what it would show if clicked, counted off the same arrays
  // and the same predicates the sections render from — so a number can never
  // disagree with the rows beneath it, search included.
  it("counts each chip, and narrows the counts to the search box", async () => {
    const jobsProp = [
      job({ job_id: 1, title: "A download" }),
      job({ job_id: 2, title: "Another download", state: "pending" }),
    ];
    const summariesProp = [
      summary({ id: 3, video_id: "s3", title: "A summary" }),
    ];
    const { rerender } = render(
      <UpNext jobs={jobsProp} summaries={summariesProp} onCancel={noop} />,
    );
    await screen.findByText("A download");

    const countFor = (label: string) =>
      Array.from(document.querySelectorAll(".chips .chip"))
        .find((c) => c.textContent?.startsWith(label))
        ?.querySelector(".n")?.textContent;

    await waitFor(() => expect(countFor("Downloads")).toBe("2"));
    expect(countFor("Summaries")).toBe("1");
    // All is the sum of the other four — the schedule contributes to it too, so
    // this is asserted as the sum rather than as the lanes' total.
    const sum = ["Downloads", "Summaries", "Scans", "Metadata"].reduce(
      (n, label) => n + Number(countFor(label)),
      0,
    );
    expect(countFor("All")).toBe(String(sum));

    // The box narrows the numbers along with the rows.
    rerender(
      <UpNext
        jobs={jobsProp}
        summaries={summariesProp}
        onCancel={noop}
        search="another"
      />,
    );
    await waitFor(() => expect(countFor("Downloads")).toBe("1"));
    expect(countFor("Summaries")).toBe("0");
    expect(countFor("All")).toBe("1");
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
    await user.click(screen.getByRole("button", { name: /^Downloads\b/ }));
    expect(screen.queryByText("A summary")).not.toBeInTheDocument();
    expect(
      screen.getByText(/nothing of that kind matches/i),
    ).toBeInTheDocument();

    // The chips survive it, so there is a way back.
    await user.click(screen.getByRole("button", { name: /^All\b/ }));
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

// --- Skipping a scheduled item (issue #156) ----------------------------------

describe("UpNext skip", () => {
  beforeEach(() => {
    vi.mocked(listUpcoming).mockReset();
    vi.mocked(listUpcoming).mockResolvedValue({ items: [], truncated: 0 });
    vi.mocked(listFailedSummaries).mockReset();
    vi.mocked(listFailedSummaries).mockResolvedValue([]);
    vi.mocked(retryFailedSummaries).mockReset();
    vi.mocked(retryFailedSummaries).mockResolvedValue({ requeued: 0 });
    vi.mocked(skipScheduledScan).mockReset();
    vi.mocked(skipScheduledMeta).mockReset();
  });

  // A scan row for a channel, which is what most skip tests below act on.
  function scanRow(at = soon(20)) {
    return {
      kind: "scan",
      approx: false,
      at,
      subject_id: "UCx",
      subject: "Veritasium",
      summary: "channel scan",
    };
  }

  it("skips a scheduled scan and refetches the schedule", async () => {
    const user = userEvent.setup();
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [scanRow()],
      truncated: 0,
    });
    vi.mocked(skipScheduledScan).mockResolvedValue({
      status: "skipped",
      at: soon(1500),
      previous_at: "2026-07-26 09:00:00",
    });
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);

    await user.click(await screen.findByRole("button", { name: /^Skip/ }));

    expect(skipScheduledScan).toHaveBeenCalledWith("UCx");
    // The refetch is the part most likely to be silently broken: nothing in the
    // lanes moved, so only the page's own nonce can have triggered it.
    await waitFor(() => expect(listUpcoming).toHaveBeenCalledTimes(2));
  });

  // A skip with no way back is a trap on a row clicked by accident, so the row
  // stays put holding an Undo rather than vanishing.
  it("leaves an undo on the skipped row and restores the exact instant", async () => {
    const user = userEvent.setup();
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [scanRow()],
      truncated: 0,
    });
    vi.mocked(skipScheduledScan).mockResolvedValue({
      status: "skipped",
      at: soon(1500),
      previous_at: "2026-07-26 09:00:00",
    });
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);

    await user.click(await screen.findByRole("button", { name: /^Skip/ }));

    const undo = await screen.findByRole("button", { name: "Undo" });
    // The relative label's cell says so — it is the same slot the button shares.
    expect(screen.getByText("Skipped")).toBeInTheDocument();
    // The row itself is still there — an undo on a row that vanished would be
    // an undo nobody can reach.
    expect(screen.getByText("Veritasium")).toBeInTheDocument();

    await user.click(undo);
    // Undo must hand back the instant the skip reported, not an approximation:
    // each channel sits on its own slot in the cycle, and a re-derived time
    // would drift it out of that rotation.
    expect(skipScheduledScan).toHaveBeenLastCalledWith(
      "UCx",
      "2026-07-26 09:00:00",
    );
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Undo" })).toBeNull(),
    );
  });

  // The button is hidden by opacity, never unmounted. Conditional rendering
  // would be the easy way to write "appears on hover" and would put the action
  // out of reach of anyone without a pointer — the row's :focus-within is what
  // reveals it instead, and that only works on an element already in the tree.
  //
  // jsdom applies no stylesheet, so this asserts the part that survives it: the
  // button is mounted with nothing hovering the row, and it takes focus.
  it("keeps Skip mounted and focusable without a pointer", async () => {
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [scanRow()],
      truncated: 0,
    });
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);

    const skip = await screen.findByRole("button", { name: /^Skip/ });
    expect(skip).toBeEnabled();
    skip.focus();
    expect(skip).toHaveFocus();
  });

  it("skips a metadata refresh through its own endpoint", async () => {
    const user = userEvent.setup();
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [
        {
          kind: "channel_meta",
          approx: false,
          at: soon(30),
          subject_id: "UCx",
          subject: "Veritasium",
          summary: "metadata refresh",
        },
      ],
      truncated: 0,
    });
    vi.mocked(skipScheduledMeta).mockResolvedValue({
      status: "skipped",
      at: soon(9000),
      previous_at: "2026-08-01 12:00:00",
    });
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);

    await user.click(await screen.findByRole("button", { name: /^Skip/ }));

    expect(skipScheduledMeta).toHaveBeenCalledWith("UCx");
    expect(skipScheduledScan).not.toHaveBeenCalled();
  });

  // A failed skip must not leave an undo behind: nothing moved, so there is
  // nothing to restore, and offering one would misreport what happened.
  it("reports a failed skip and keeps the row actionable", async () => {
    const user = userEvent.setup();
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [scanRow()],
      truncated: 0,
    });
    vi.mocked(skipScheduledScan).mockRejectedValue(new Error("nope"));
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);

    await user.click(await screen.findByRole("button", { name: /^Skip/ }));

    expect(await screen.findByText(/Nothing was changed/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Undo" })).toBeNull();
    expect(
      await screen.findByRole("button", { name: /^Skip/ }),
    ).toBeInTheDocument();
  });

  // The retention sweep and the yt-dlp check are in-memory tickers with no
  // persisted schedule, so there is nothing a skip could write.
  it("offers no skip on a row with no schedule to move", async () => {
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [
        {
          kind: "retention",
          approx: false,
          at: soon(40),
          summary: "retention sweep",
        },
      ],
      truncated: 0,
    });
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);

    // The row is identified by its kind label, not by the summary: a scheduled
    // entry is one line, so there is no detail line to carry "retention sweep".
    // Asserting the row is present at all still matters — a missing row would
    // make the no-Skip assertion below pass for the wrong reason.
    expect(await screen.findByText("Cleanup")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Skip/ })).toBeNull();
  });

  // History renders its own copy of this row and must not grow a control from
  // a rule written for Up next. The modifier class is what keeps them apart.
  it("hangs the skip styling off a class History's rows do not carry", async () => {
    vi.mocked(listUpcoming).mockResolvedValue({
      items: [scanRow()],
      truncated: 0,
    });
    render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);

    await screen.findByText("Veritasium");
    const row = screen.getByText("Veritasium").closest(".ag-row");
    expect(row).toHaveClass("planned");
  });

  // The gave-up section is the only surface these jobs have anywhere: they are
  // gone from the lanes, the boot sweep skips them on purpose, and one that
  // failed after the summary step left its video reading "done" everywhere else.
  describe("summaries that gave up", () => {
    const failed = () =>
      summary({ id: 9, video_id: "gone", state: "failed" }) as SummaryJob;

    it("lists them under their own heading, apart from the queue", async () => {
      vi.mocked(listFailedSummaries).mockResolvedValue([failed()]);
      render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);

      expect(await screen.findByText("Gave up")).toBeInTheDocument();
    });

    // A failed job carries no phase, so it used to take the queued branch and
    // read "waiting" — under a heading saying it gave up, on a row nothing will
    // move again.
    it("says the row gave up rather than that it is waiting", async () => {
      vi.mocked(listFailedSummaries).mockResolvedValue([failed()]);
      render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
      await screen.findByText("Gave up");

      expect(screen.getByText("gave up")).toBeInTheDocument();
      expect(screen.queryByText("waiting")).toBeNull();
    });

    // The bound that failed is kept on the job row and nowhere else, and the
    // three bounds have three different answers — so a row that omits it can
    // say a video failed but never which thing did.
    it("shows the bound that failed", async () => {
      vi.mocked(listFailedSummaries).mockResolvedValue([
        { ...failed(), last_error: "stream idle for 1m30s" },
      ]);
      render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
      await screen.findByText("Gave up");

      expect(screen.getByText("stream idle for 1m30s")).toBeInTheDocument();
    });

    // Tinted through the row modifier History already uses, not the form-error
    // box: these rows are a quiet list, and .errline carries padding, a border
    // and a bottom margin sized to stack above a sign-in form.
    it("marks the row failed rather than boxing the error", async () => {
      vi.mocked(listFailedSummaries).mockResolvedValue([
        { ...failed(), last_error: "stream idle for 1m30s" },
      ]);
      render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
      await screen.findByText("Gave up");

      const line = screen.getByText("stream idle for 1m30s");
      expect(line).not.toHaveClass("errline");
      expect(line.closest(".ag-row")).toHaveClass("fail");
    });

    // A queued row must not grow an error line from a rule written for the
    // gave-up section: its last_error is from an attempt still being retried,
    // so showing it would report a settled failure that has not happened.
    it("keeps the error line off a job that is still being retried", async () => {
      render(
        <UpNext
          jobs={[]}
          summaries={[
            summary({ state: "pending", last_error: "stream idle for 1m30s" }),
          ]}
          onCancel={noop}
        />,
      );

      await screen.findByText("waiting");
      expect(screen.queryByText("stream idle for 1m30s")).toBeNull();
    });

    it("stays off the page when nothing has failed", async () => {
      render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);

      await screen.findByText(/subscribe to a channel/i);
      expect(screen.queryByText("Gave up")).toBeNull();
    });

    // A summary row is a summary row: filtering to Downloads must take these
    // with it, or the chip would claim to have narrowed the page and not have.
    it("hides under the Downloads filter", async () => {
      vi.mocked(listFailedSummaries).mockResolvedValue([failed()]);
      render(
        <UpNext
          jobs={[job()]}
          summaries={[]}
          onCancel={noop}
          onSearchChange={noop}
        />,
      );
      await screen.findByText("Gave up");

      await userEvent.click(screen.getByRole("button", { name: /Downloads/ }));

      expect(screen.queryByText("Gave up")).toBeNull();
    });

    it("retries every one of them, then re-reads what is left", async () => {
      vi.mocked(listFailedSummaries).mockResolvedValue([failed()]);
      render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);
      await screen.findByText("Gave up");
      // The retry empties the section: the rows moved back to the queue.
      vi.mocked(listFailedSummaries).mockResolvedValue([]);

      await userEvent.click(screen.getByRole("button", { name: "Retry all" }));

      await waitFor(() => expect(retryFailedSummaries).toHaveBeenCalled());
      await waitFor(() => expect(screen.queryByText("Gave up")).toBeNull());
    });

    // A supplementary list that cannot load must not put an error where the
    // page's real news goes.
    it("says nothing when the list itself fails to load", async () => {
      vi.mocked(listFailedSummaries).mockRejectedValue(new Error("boom"));
      render(<UpNext jobs={[]} summaries={[]} onCancel={noop} />);

      await screen.findByText(/subscribe to a channel/i);
      expect(screen.queryByText("Gave up")).toBeNull();
    });
  });
});
