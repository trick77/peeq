import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { StatusPanel } from "./StatusPanel";
import type { Job } from "../api/types";
import type { DownloadsStatus } from "../api/downloads";

function job(overrides: Partial<Job> = {}): Job {
  return {
    job_id: 1,
    video_id: "v1",
    state: "pending",
    priority: 0,
    attempts: 0,
    ...overrides,
  } as Job;
}

// rowFor finds a count row by its label and returns the row element, so an
// assertion can check the label and its number together rather than searching
// the whole panel for a loose "1".
function rowFor(label: string) {
  return screen.getByText(label).closest(".srow");
}

describe("StatusPanel", () => {
  it("names every stage of the pipeline separately", () => {
    render(
      <StatusPanel
        jobs={[
          job({ job_id: 1, state: "running" }),
          job({ job_id: 2, state: "pending" }),
          job({ job_id: 3, state: "pending" }),
        ]}
        pendingCount={7}
      />,
    );
    expect(rowFor("To decide")).toHaveTextContent("7");
    expect(rowFor("Downloading")).toHaveTextContent("1");
    expect(rowFor("Queued")).toHaveTextContent("2");
    expect(screen.getByText("working")).toBeInTheDocument();
  });

  // The rule that makes an idle rail quiet. A row showing "0" is noise that
  // crowds out the one number that does matter, so absence is rendered as
  // absence.
  it("omits a row entirely rather than showing a zero", () => {
    render(<StatusPanel jobs={[job({ state: "running" })]} pendingCount={0} />);
    expect(screen.queryByText("To decide")).not.toBeInTheDocument();
    expect(screen.queryByText("Queued")).not.toBeInTheDocument();
    expect(rowFor("Downloading")).toHaveTextContent("1");
  });

  it("says so plainly when there is nothing to report", () => {
    render(<StatusPanel jobs={[]} pendingCount={0} />);
    expect(screen.getByText("Nothing waiting")).toBeInTheDocument();
    expect(screen.getByText("idle")).toBeInTheDocument();
  });

  // Only the row that wants a person is accented. If this ever loosens, the
  // accent stops meaning "act on me" and the panel loses its glanceability.
  it("accents the decisions row and nothing else", () => {
    render(
      <StatusPanel
        jobs={[job({ state: "running" }), job({ job_id: 2, state: "pending" })]}
        pendingCount={3}
      />,
    );
    expect(rowFor("To decide")).toHaveClass("hot");
    expect(rowFor("Downloading")).not.toHaveClass("hot");
    expect(rowFor("Queued")).not.toHaveClass("hot");
  });

  it("opens the decisions page from the decisions row", () => {
    const onOpenPending = vi.fn();
    render(
      <StatusPanel jobs={[]} pendingCount={2} onOpenPending={onOpenPending} />,
    );
    fireEvent.click(screen.getByText("To decide"));
    expect(onOpenPending).toHaveBeenCalled();
  });

  // Three different stalls, one state word. The panel deliberately does not
  // explain any of them — DownloadStatusBanner does, with the buttons to act.
  // What the panel adds is that the rail never scrolls away, so a stall stays
  // visible after the banner has scrolled off the top of a long page.
  it.each<[string, Partial<DownloadsStatus>]>([
    ["youtube_paused", { youtube_paused: true }],
    ["low_disk", { low_disk: true }],
    ["a cookie pause", { paused: true }],
  ])("reports %s as paused", (_label, flags) => {
    render(
      <StatusPanel
        jobs={[job({ state: "pending" })]}
        pendingCount={0}
        status={{
          paused: false,
          low_disk: false,
          youtube_paused: false,
          youtube_pause_reason: "",
          ...flags,
        }}
      />,
    );
    expect(screen.getByText("paused")).toBeInTheDocument();
    expect(screen.queryByText("working")).not.toBeInTheDocument();
  });

  it("shows the running job's progress under the counts", () => {
    render(
      <StatusPanel
        jobs={[job({ job_id: 9, state: "running", title: "A Long Video" })]}
        progressByJobId={{
          9: { percent: 62, speed: "8.4MiB/s", eta: "00:41" },
        }}
        pendingCount={0}
      />,
    );
    const sub = document.querySelector(".dock-sub");
    expect(sub).toHaveTextContent("A Long Video");
    expect(sub).toHaveTextContent("62%");
    expect(sub).toHaveTextContent("00:41");
    expect(document.querySelector(".dock-bar > i")).toHaveStyle({
      width: "62%",
    });
  });

  it("shows no progress bar when nothing is running", () => {
    render(<StatusPanel jobs={[job({ state: "pending" })]} pendingCount={0} />);
    expect(document.querySelector(".dock-bar")).toBeNull();
  });
});
