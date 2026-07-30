import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { UnfetchedVideo } from "./UnfetchedVideo";
import type { Video } from "../../api/types";

vi.mock("../../api/pending", () => ({
  downloadPending: vi.fn(),
  ignorePending: vi.fn(),
}));

import { downloadPending, ignorePending } from "../../api/pending";

function video(overrides: Partial<Video> = {}): Video {
  return {
    id: "v1",
    title: "Why this dam was built to fail on purpose",
    channel_id: "c1",
    channel_name: "Practical Engineering",
    duration_seconds: 1122,
    published_at: "2026-07-28",
    status: "new",
    summary: "First paragraph about fuse plugs.\n\nSecond paragraph.",
    summary_status: "done",
    ...overrides,
  } as Video;
}

describe("UnfetchedVideo", () => {
  beforeEach(() => {
    vi.mocked(downloadPending).mockReset();
    vi.mocked(ignorePending).mockReset();
    vi.mocked(downloadPending).mockResolvedValue(undefined);
    vi.mocked(ignorePending).mockResolvedValue(undefined);
  });

  it("renders the summary as paragraphs and offers both decisions", async () => {
    render(<UnfetchedVideo video={video()} />);

    expect(screen.getByText("First paragraph about fuse plugs.")).toBeTruthy();
    expect(screen.getByText("Second paragraph.")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Download/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Ignore/ })).toBeTruthy();
  });

  // There is no media, so nothing on this page may imply there is. A <video>
  // element here would try to load a stream that cannot exist.
  it("renders no media element", () => {
    const { container } = render(<UnfetchedVideo video={video()} />);
    expect(container.querySelector("video")).toBeNull();
  });

  it("queues a download and says so without navigating away", async () => {
    const onQueued = vi.fn();
    render(<UnfetchedVideo video={video()} onQueued={onQueued} />);

    await userEvent.click(screen.getByRole("button", { name: /Download/ }));

    expect(downloadPending).toHaveBeenCalledWith("v1");
    expect(onQueued).toHaveBeenCalled();
    expect(await screen.findByText("Queued for download.")).toBeTruthy();
  });

  // Ignoring deletes the row and the summary server-side, so the page's subject
  // is gone: the caller has to be told to leave.
  it("tells the caller to leave after an ignore", async () => {
    const onDismissed = vi.fn();
    render(<UnfetchedVideo video={video()} onDismissed={onDismissed} />);

    await userEvent.click(screen.getByRole("button", { name: /Ignore/ }));

    expect(ignorePending).toHaveBeenCalledWith("v1");
    expect(onDismissed).toHaveBeenCalled();
  });

  // The wording covers both "YouTube has no captions" and "they turned out to
  // be music", because one status carries both — the same reason the Player's
  // copy says speech rather than captions.
  it("explains a video with no speech instead of showing a spinner", () => {
    render(
      <UnfetchedVideo
        video={video({ summary: "", summary_status: "no_transcript" })}
      />,
    );
    expect(screen.getByText(/No speech in this video/)).toBeTruthy();
  });

  it("says it is still reading a video whose captions have not landed", () => {
    render(
      <UnfetchedVideo
        video={video({ summary: "", summary_status: "pending" })}
      />,
    );
    expect(screen.getByText(/Reading this video/)).toBeTruthy();
  });
});
