import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { VideoCard } from "../components/VideoCard";
import type { Video } from "../api/types";

// baseVideo is a minimal, valid Video row; each test overrides only the
// fields that drive the lifecycle line being asserted.
function baseVideo(overrides: Partial<Video> = {}): Video {
  return {
    id: "v1",
    url: "https://youtu.be/v1",
    title: "A Test Video",
    channel_id: "chan1",
    channel_name: "Test Channel",
    duration_seconds: 754,
    has_thumbnail: false,
    has_media: true,
    availability: "available",
    status: "downloaded",
    watched: false,
    resume_position_seconds: 0,
    favorite: false,
    summary: "",
    chapters: [],
    key_points: [],
    summary_status: "",
    audio_language: "",
    has_subtitles: false,
    ...overrides,
  };
}

const noop = () => {};

describe("VideoCard lifecycle line", () => {
  it('renders "Kept forever" for a favorite video', () => {
    render(
      <VideoCard
        video={baseVideo({ favorite: true, watched: true })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    expect(screen.getByText("Kept forever")).toBeInTheDocument();
  });

  it('renders "Expires in N days" for a watched, non-favorite video', () => {
    const fiveDaysAgo = new Date(Date.now() - 5 * 24 * 60 * 60 * 1000).toISOString();
    render(
      <VideoCard
        video={baseVideo({ watched: true, watched_at: fiveDaysAgo, favorite: false })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    // 14 - 5 = 9 days left.
    expect(screen.getByText(/Expires in 9 days/)).toBeInTheDocument();
  });

  it("renders the progress ring for a downloading video", () => {
    render(
      <VideoCard
        video={baseVideo({ status: "downloading", has_media: false })}
        retentionDays={14}
        progress={{ percent: 42, eta: "1m12s" }}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    const ring = document.querySelector(".ring");
    expect(ring).not.toBeNull();
    expect(ring?.getAttribute("data-p")).toBe("42%");
    expect(screen.getByText("Downloading…")).toBeInTheDocument();
  });

  it('renders "Not watched yet" for a fresh video', () => {
    render(
      <VideoCard
        video={baseVideo()}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    expect(screen.getByText("Not watched yet")).toBeInTheDocument();
  });

  it("calls onOpen with the video id when the thumbnail is clicked", async () => {
    const onOpen = vi.fn();
    render(
      <VideoCard video={baseVideo()} retentionDays={14} onOpen={onOpen} onToggleFavorite={noop} onToggleWatched={noop} />,
    );
    screen.getByRole("button", { name: /Open A Test Video/ }).click();
    expect(onOpen).toHaveBeenCalledWith("v1");
  });

  it('shows "Download failed" + a Re-download button for an errored video and calls onRedownload', () => {
    const onRedownload = vi.fn();
    render(
      <VideoCard
        video={baseVideo({ id: "v1", status: "error" })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
        onRedownload={onRedownload}
      />,
    );
    expect(screen.getByText("Download failed")).toBeInTheDocument();
    const btn = screen.getByRole("button", { name: /re-download/i });
    fireEvent.click(btn);
    expect(onRedownload).toHaveBeenCalledWith("v1");
  });

  it('shows "Removed to save space · summary kept" for a tombstoned video', () => {
    render(
      <VideoCard
        video={baseVideo({ id: "v1", status: "tombstoned" })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
        onRedownload={noop}
      />,
    );
    expect(screen.getByText("Removed to save space · summary kept")).toBeInTheDocument();
  });
});
