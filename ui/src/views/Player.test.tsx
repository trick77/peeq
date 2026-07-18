import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { Player } from "./Player";
import type { Video } from "../api/types";

const mockVideo: Video = {
  id: "v1",
  url: "https://youtu.be/v1",
  title: "The Trillion Dollar Equation",
  channel_id: "chan1",
  channel_name: "Veritasium",
  duration_seconds: 1684,
  has_thumbnail: false,
  has_media: true,
  availability: "available",
  status: "downloaded",
  watched: false,
  resume_position_seconds: 42,
  favorite: false,
};

vi.mock("../api/videos", () => ({
  getVideo: vi.fn(),
  setFavorite: vi.fn().mockResolvedValue(true),
  setWatched: vi.fn().mockResolvedValue(true),
  setResume: vi.fn().mockResolvedValue(42),
  deleteVideo: vi.fn().mockResolvedValue(undefined),
  streamUrl: (id: string) => `/api/videos/${id}/stream`,
}));

import { getVideo, setResume } from "../api/videos";

describe("Player", () => {
  beforeEach(() => {
    vi.mocked(getVideo).mockReset();
    vi.mocked(setResume).mockClear();
    vi.mocked(getVideo).mockResolvedValue(mockVideo);
  });

  it("sets video.currentTime from resume_position_seconds once metadata loads", async () => {
    render(<Player videoId="v1" onDeleted={() => {}} />);

    const videoEl = await waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });

    fireEvent.loadedMetadata(videoEl);

    expect(videoEl.currentTime).toBeCloseTo(42, 0);
  });

  it("posts the current position to setResume on timeupdate", async () => {
    render(<Player videoId="v1" onDeleted={() => {}} />);

    const videoEl = await waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });
    Object.defineProperty(videoEl, "currentTime", { value: 100, writable: true });

    fireEvent.timeUpdate(videoEl);

    await waitFor(() => {
      expect(setResume).toHaveBeenCalledWith("v1", 100);
    });
  });

  it('renders a "Watch on YouTube" link to the video url', async () => {
    render(<Player videoId="v1" onDeleted={() => {}} />);
    const link = await screen.findByRole("link", { name: /Watch on YouTube/i });
    expect(link).toHaveAttribute("href", "https://youtu.be/v1");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noreferrer");
  });

  it("shows a placeholder message with nothing selected", () => {
    render(<Player videoId={null} onDeleted={() => {}} />);
    expect(screen.getByText(/Pick a video from the Library/i)).toBeInTheDocument();
  });
});
