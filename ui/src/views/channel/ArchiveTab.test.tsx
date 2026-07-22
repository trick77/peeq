import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ArchiveTab } from "./ArchiveTab";
import { listVideos, setWatched } from "../../api/videos";
import { getSettings } from "../../api";
import type { Video } from "../../api/types";

vi.mock("../../api/videos", () => ({
  listVideos: vi.fn(),
  setFavorite: vi.fn().mockResolvedValue(true),
  setWatched: vi.fn().mockResolvedValue(true),
  thumbnailUrl: (id: string) => `/api/videos/${id}/thumbnail`,
}));

vi.mock("../../api", () => ({
  getSettings: vi.fn(),
}));

function archiveVideo(overrides: Partial<Video> = {}): Video {
  return {
    id: "v1",
    url: "https://youtu.be/v1",
    title: "A Channel Video",
    channel_id: "chan1",
    channel_name: "Test Channel",
    duration_seconds: 100,
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
    category: "uncategorized",
    ...overrides,
  } as Video;
}

describe("ArchiveTab watched toggle", () => {
  beforeEach(() => {
    vi.mocked(getSettings).mockResolvedValue({ retention_days: 14 } as never);
    vi.mocked(setWatched).mockClear();
  });

  it("clears the resume position so no stale progress bar appears", async () => {
    // The server zeroes resume_position_seconds on either direction of the
    // toggle but answers with the watched flag alone, so the optimistic update
    // has to mirror the reset. Without it, un-watching this card would draw a
    // progress bar (VideoCard shows it only when !watched) at a position the
    // server has already cleared.
    vi.mocked(listVideos).mockResolvedValue([
      archiveVideo({ watched: true, resume_position_seconds: 40 }),
    ]);
    render(<ArchiveTab channelId="chan1" onOpenVideo={() => {}} />);
    await screen.findByText("A Channel Video");

    fireEvent.click(screen.getByRole("button", { name: "Mark unwatched" }));

    await waitFor(() => expect(setWatched).toHaveBeenCalledWith("v1", false));
    expect(document.querySelector(".resume")).toBeNull();
  });

  it("restores the position when the toggle fails", async () => {
    vi.mocked(listVideos).mockResolvedValue([
      archiveVideo({ watched: false, resume_position_seconds: 40 }),
    ]);
    vi.mocked(setWatched).mockRejectedValueOnce(new Error("network down"));
    render(<ArchiveTab channelId="chan1" onOpenVideo={() => {}} />);
    await screen.findByText("A Channel Video");
    expect(document.querySelector(".resume")).not.toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Mark watched" }));

    // Rolled back to unwatched *and* to the position it had, so the bar the
    // card was showing before the click comes back with it.
    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Mark watched" }),
      ).toBeInTheDocument();
    });
    expect(document.querySelector(".resume")).not.toBeNull();
  });
});
