import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { Share } from "./Share";
import type { PublicVideo } from "../api/share";

const mockVideo: PublicVideo = {
  title: "How the CIA writes a threat assessment",
  channel_name: "Lex Clips",
  duration_seconds: 2467,
  summary: "A former analyst walks through finished intelligence.",
  summary_status: "done",
  chapters: [],
  key_points: [{ ts: 191, text: "Why confidence is a discipline." }],
  has_thumbnail: false,
  has_subtitles: false,
  audio_language: "en",
  expires_at: "",
};

vi.mock("../api/share", () => ({
  getSharedVideo: vi.fn(),
  shareStreamUrl: (t: string) => `/api/s/${t}/stream`,
  shareThumbnailUrl: (t: string) => `/api/s/${t}/thumbnail`,
  shareSubtitlesUrl: (t: string) => `/api/s/${t}/subtitles`,
}));

import { getSharedVideo } from "../api/share";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("Share (public page)", () => {
  it("renders the video, summary and highlights for a live link", async () => {
    vi.mocked(getSharedVideo).mockResolvedValue(mockVideo);
    render(<Share token="3xK9raPb" />);

    expect(
      await screen.findByRole("heading", {
        name: /how the cia writes a threat assessment/i,
      }),
    ).toBeInTheDocument();
    expect(screen.getByText("Lex Clips")).toBeInTheDocument();
    expect(screen.getByText(/former analyst/i)).toBeInTheDocument();
    expect(
      screen.getByText(/why confidence is a discipline/i),
    ).toBeInTheDocument();
    // The recipient never gets a download control — stream-only.
    expect(screen.queryByText(/download/i)).not.toBeInTheDocument();
  });

  it("shows the expired dead-end when the link is not active (404)", async () => {
    vi.mocked(getSharedVideo).mockRejectedValue(new Error("not found"));
    render(<Share token="dead" />);

    expect(
      await screen.findByRole("heading", { name: /this link has expired/i }),
    ).toBeInTheDocument();
    // Nothing about the video leaks on the dead-end.
    expect(screen.queryByText("Lex Clips")).not.toBeInTheDocument();
  });

  it("shows the expired dead-end when there is no token at all", async () => {
    render(<Share token={null} />);
    expect(
      await screen.findByRole("heading", { name: /this link has expired/i }),
    ).toBeInTheDocument();
    expect(getSharedVideo).not.toHaveBeenCalled();
  });
});
