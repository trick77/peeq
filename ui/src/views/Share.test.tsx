import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { Share } from "./Share";
import type { PublicVideo } from "../api/share";

// A UTC expiry `days` from now, in the backend's 'YYYY-MM-DD HH:MM:SS' shape.
function expiryIn(days: number): string {
  return new Date(Date.now() + days * 86_400_000)
    .toISOString()
    .slice(0, 19)
    .replace("T", " ");
}

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
    // The recipient can save the captions (see the transcript tests below), but
    // never the video itself: no ?download=1 link, nothing that turns the media
    // file into a file. That half of "stream-only" still holds.
    for (const a of document.querySelectorAll("a[href]")) {
      expect(a.getAttribute("href")).not.toMatch(/download=1/);
    }
    // With no captions on this fixture there is no download control at all.
    expect(screen.queryByText(".vtt")).not.toBeInTheDocument();
    expect(screen.queryByText(".txt")).not.toBeInTheDocument();
  });

  it("seeks the player when a highlight is clicked, and shows the expiry footer", async () => {
    vi.mocked(getSharedVideo).mockResolvedValue({
      ...mockVideo,
      has_thumbnail: true,
      has_subtitles: true,
      expires_at: expiryIn(6),
    });
    render(<Share token="3xK9raPb" />);

    // Footer reflects the future expiry.
    expect(
      await screen.findByText(/this link expires in 6 days/i),
    ).toBeInTheDocument();

    // Clicking a highlight seeks the <video> element to its timestamp.
    const seekSpy = vi
      .spyOn(window.HTMLMediaElement.prototype, "play")
      .mockResolvedValue(undefined);
    const highlight = screen.getByRole("button", {
      name: /why confidence is a discipline/i,
    });
    fireEvent.click(highlight);
    expect(seekSpy).toHaveBeenCalled();
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

  it("lists chapters and seeks to one when clicked", async () => {
    vi.mocked(getSharedVideo).mockResolvedValue({
      ...mockVideo,
      chapters: [
        { ts: 0, title: "Cold open", source: "yt-dlp" },
        { ts: 154, title: "What finished intelligence means", source: "mimo" },
      ],
    });
    render(<Share token="3xK9raPb" />);

    expect(await screen.findByText("Chapters")).toBeInTheDocument();
    expect(screen.getByText("2 chapters")).toBeInTheDocument();
    // The yt-dlp/MiMo provenance tag is Player-only — internal trivia the
    // recipient has no use for.
    expect(screen.queryByText("yt-dlp")).not.toBeInTheDocument();
    expect(screen.queryByText("MiMo")).not.toBeInTheDocument();

    const playSpy = vi
      .spyOn(window.HTMLMediaElement.prototype, "play")
      .mockResolvedValue(undefined);
    fireEvent.click(
      screen.getByRole("button", { name: /what finished intelligence means/i }),
    );
    expect(playSpy).toHaveBeenCalled();
  });

  it("omits the chapters card entirely when there are none", async () => {
    vi.mocked(getSharedVideo).mockResolvedValue(mockVideo);
    render(<Share token="3xK9raPb" />);

    expect(await screen.findByText("Summary")).toBeInTheDocument();
    // No empty-state placeholder on a public page — the card simply isn't there.
    expect(screen.queryByText("Chapters")).not.toBeInTheDocument();
    expect(screen.queryByText(/no chapters/i)).not.toBeInTheDocument();
  });
});

describe("Share transcript panel", () => {
  const VTT = [
    "WEBVTT",
    "",
    "00:00:12.000 --> 00:00:15.000",
    "Confidence is a discipline, not a feeling.",
    "",
    "00:01:04.000 --> 00:01:07.000",
    "Sourcing is what separates the two.",
    "",
  ].join("\n");

  function renderWithCaptions() {
    vi.mocked(getSharedVideo).mockResolvedValue({
      ...mockVideo,
      has_subtitles: true,
    });
    return render(<Share token="3xK9raPb" />);
  }

  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, text: async () => VTT }),
    );
  });

  it("fetches the token-gated VTT on first expand and renders click-to-seek cues", async () => {
    renderWithCaptions();

    // Collapsed: nothing fetched yet.
    fireEvent.click(await screen.findByRole("button", { name: /transcript/i }));

    expect(
      await screen.findByText(/confidence is a discipline/i),
    ).toBeInTheDocument();
    // The share route, never the session-gated /api/videos/<id>/subtitles.
    expect(fetch).toHaveBeenCalledWith("/api/s/3xK9raPb/subtitles");

    const playSpy = vi
      .spyOn(window.HTMLMediaElement.prototype, "play")
      .mockResolvedValue(undefined);
    fireEvent.click(
      screen.getByRole("button", { name: /sourcing is what separates/i }),
    );
    expect(playSpy).toHaveBeenCalled();
  });

  it("counts matches as you search the transcript", async () => {
    renderWithCaptions();
    fireEvent.click(await screen.findByRole("button", { name: /transcript/i }));
    await screen.findByText(/confidence is a discipline/i);

    fireEvent.change(screen.getByPlaceholderText(/find in transcript/i), {
      target: { value: "discipline" },
    });
    expect(screen.getByText("1 / 2")).toBeInTheDocument();
  });

  it("offers caption downloads named from the title — never the id or the token", async () => {
    renderWithCaptions();
    fireEvent.click(await screen.findByRole("button", { name: /transcript/i }));
    await screen.findByText(/confidence is a discipline/i);

    const vtt = screen.getByText(".vtt").closest("a");
    expect(vtt).toHaveAttribute("href", "/api/s/3xK9raPb/subtitles");
    const name = vtt?.getAttribute("download") ?? "";
    expect(name).toBe("How_the_CIA_writes_a_threat_assessment.vtt");
    expect(name).not.toContain("3xK9raPb");

    // .txt is built client-side from the cues already parsed for the panel;
    // clicking it makes a Blob and triggers the save (jsdom lacks
    // createObjectURL, so stub it).
    const createURL = vi.fn(() => "blob:mock");
    const revokeURL = vi.fn();
    URL.createObjectURL = createURL;
    URL.revokeObjectURL = revokeURL;
    fireEvent.click(screen.getByRole("button", { name: /\.txt/i }));
    expect(createURL).toHaveBeenCalledTimes(1);
    expect(revokeURL).toHaveBeenCalledTimes(1);

    // Still no way to save the video file itself.
    for (const a of document.querySelectorAll("a[href]")) {
      expect(a.getAttribute("href")).not.toMatch(/download=1/);
    }
  });

  it("shows an error line when the captions fail to load", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false }));
    renderWithCaptions();
    fireEvent.click(await screen.findByRole("button", { name: /transcript/i }));

    expect(
      await screen.findByText(/failed to load transcript/i),
    ).toBeInTheDocument();
  });

  it("does not render the transcript card when the video has no captions", async () => {
    vi.mocked(getSharedVideo).mockResolvedValue(mockVideo);
    render(<Share token="3xK9raPb" />);

    expect(await screen.findByText("Summary")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /transcript/i }),
    ).not.toBeInTheDocument();
    expect(fetch).not.toHaveBeenCalled();
  });
});
