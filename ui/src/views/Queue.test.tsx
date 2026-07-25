import { describe, it, expect, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Queue } from "./Queue";
import type { Job, SummaryJob } from "../api/types";

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

const noop = () => {};

describe("Queue", () => {
  it("says the queue is empty when both lanes are empty", () => {
    render(<Queue jobs={[]} summaries={[]} onCancel={noop} />);
    expect(screen.getByText(/nothing in the queue/i)).toBeInTheDocument();
    // A lane heading must not show for an empty lane.
    expect(screen.queryByText("Downloading")).not.toBeInTheDocument();
    expect(screen.queryByText("Being summarized")).not.toBeInTheDocument();
  });

  it("shows a downloading row with live percent and eta", () => {
    render(
      <Queue
        jobs={[job({ job_id: 9, title: "A Long Video", channel_name: "Chan" })]}
        progressByJobId={{ 9: { percent: 62, speed: "8MiB/s", eta: "00:41" } }}
        summaries={[]}
        onCancel={noop}
      />,
    );
    expect(screen.getByText("Downloading")).toBeInTheDocument();
    const row = screen
      .getByText("A Long Video")
      .closest(".qrow") as HTMLElement;
    expect(within(row).getByText("Chan")).toBeInTheDocument();
    expect(within(row).getByText(/62%/)).toHaveTextContent("00:41");
  });

  // yt-dlp only reports a speed and an eta once a download is properly under
  // way, so the first ticks arrive with the percent alone. Neither separator
  // may show up on its own then — a trailing "3% ·" reads like a truncation.
  it("shows only the percent while speed and eta are still unknown", () => {
    render(
      <Queue
        jobs={[job({ job_id: 4, title: "Just started" })]}
        progressByJobId={{ 4: { percent: 3, speed: "", eta: "" } }}
        summaries={[]}
        onCancel={noop}
      />,
    );
    const row = screen
      .getByText("Just started")
      .closest(".qrow") as HTMLElement;
    expect(row.querySelector(".qstate")?.textContent).toBe("3%");
  });

  it("labels a queued (not running) download as Queued, no bar", () => {
    render(
      <Queue
        jobs={[job({ job_id: 2, state: "pending", title: "Waiting one" })]}
        summaries={[]}
        onCancel={noop}
      />,
    );
    const row = screen.getByText("Waiting one").closest(".qrow") as HTMLElement;
    expect(within(row).getByText("Queued")).toBeInTheDocument();
    expect(row.querySelector(".qbar")).toBeNull();
  });

  it("cancels a download by its job id", async () => {
    const user = userEvent.setup();
    const onCancel = vi.fn();
    render(
      <Queue
        jobs={[job({ job_id: 7, title: "Cancelme" })]}
        summaries={[]}
        onCancel={onCancel}
      />,
    );
    await user.click(screen.getByRole("button", { name: /cancel/i }));
    expect(onCancel).toHaveBeenCalledWith(7);
  });

  it("shows summaries with their live phase and no actions", () => {
    render(
      <Queue
        jobs={[]}
        summaries={[summary({ id: 3, video_id: "s3", title: "Being done" })]}
        summaryPhaseByVideoId={{ s3: "embedding" }}
        onCancel={noop}
      />,
    );
    expect(screen.getByText("Being summarized")).toBeInTheDocument();
    const row = screen.getByText("Being done").closest(".qrow") as HTMLElement;
    expect(within(row).getByText("Embedding")).toBeInTheDocument();
    // The summarize lane offers no cancel — summaries run unattended.
    expect(within(row).queryByRole("button")).toBeNull();
  });

  it("falls back to a state-derived phase word without an SSE phase yet", () => {
    render(
      <Queue
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
      .closest(".qrow") as HTMLElement;
    expect(within(running).getByText("Summarizing")).toBeInTheDocument();
    const waiting = screen
      .getByText("Still waiting")
      .closest(".qrow") as HTMLElement;
    expect(within(waiting).getByText("Waiting")).toBeInTheDocument();
  });
});
