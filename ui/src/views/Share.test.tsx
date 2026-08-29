import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  render,
  screen,
  fireEvent,
  waitFor,
  act,
} from "@testing-library/react";
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
        { ts: 154, title: "What finished intelligence means", source: "llm" },
      ],
    });
    render(<Share token="3xK9raPb" />);

    expect(await screen.findByText("Chapters")).toBeInTheDocument();
    expect(screen.getByText("2 chapters")).toBeInTheDocument();
    // The yt-dlp/LLM provenance tag is Player-only — internal trivia the
    // recipient has no use for.
    expect(screen.queryByText("yt-dlp")).not.toBeInTheDocument();
    expect(screen.queryByText("LLM")).not.toBeInTheDocument();

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

  it("renders no top bar — the page opens on the video", async () => {
    vi.mocked(getSharedVideo).mockResolvedValue(mockVideo);
    render(<Share token="3xK9raPb" />);

    await screen.findByText("Summary");
    expect(document.querySelector(".sharepage-top")).toBeNull();
    expect(screen.queryByText("Shared with you")).not.toBeInTheDocument();
    // The attribution lives in the footer instead, not nowhere.
    expect(screen.getByText(/shared via/i)).toBeInTheDocument();
  });

  it("puts Chapters and Highlights full-width under the video, leaving the aside to the Summary — the Player's layout", async () => {
    vi.mocked(getSharedVideo).mockResolvedValue({
      ...mockVideo,
      has_subtitles: true,
      chapters: [{ ts: 0, title: "Cold open", source: "yt-dlp" }],
    });
    render(<Share token="3xK9raPb" />);

    await screen.findByText("Chapters");
    // The aside carries the Summary alone, exactly as the Player's sidebar
    // does. Highlights are timestamps into the chapter list, so they belong
    // under it in the primary column rather than in the rail.
    const asideLabels = [
      ...document.querySelectorAll(".sharepage-side .lbl"),
    ].map((el) => el.textContent);
    expect(asideLabels).toEqual(["Summary"]);

    const primaryLabels = [
      ...document.querySelectorAll(".sharepage-primary .lbl"),
    ].map((el) => el.textContent);
    expect(primaryLabels).toEqual(["Chapters", "Highlights", "Transcript"]);
    // Two columns, like the Player's Contents card — not the aside's single one.
    expect(
      document.querySelector(".sharepage-chapters .toc-grid"),
    ).toBeTruthy();
  });

  // Same workaround the Player carries: iPadOS 27 (public beta 1) Safari
  // refuses to load the media at all when a <track> child is present during
  // resource selection, leaving the page on the poster. See
  // tubearchivist/tubearchivist#1196.
  it("mounts the subtitle track only after loadedmetadata", async () => {
    vi.mocked(getSharedVideo).mockResolvedValue({
      ...mockVideo,
      has_subtitles: true,
    });
    render(<Share token="3xK9raPb" />);

    await screen.findByRole("heading", {
      name: /how the cia writes a threat assessment/i,
    });
    expect(document.querySelector("video track")).toBeNull();

    fireEvent.loadedMetadata(
      document.querySelector("video") as HTMLVideoElement,
    );
    await waitFor(() =>
      expect(document.querySelector("video track")).not.toBeNull(),
    );
    expect(document.querySelector("video track")).toHaveAttribute(
      "src",
      "/api/s/3xK9raPb/subtitles",
    );
  });
});

// The shared player skips exactly what the owner's player skips. A recipient has
// no account and no settings, so unless the segments ride along on the public
// payload there is no way to skip an ad on a shared video at all.
describe("Share SponsorBlock", () => {
  function renderWithSegments(
    segments: NonNullable<PublicVideo["sponsorblock_segments"]>,
  ) {
    vi.mocked(getSharedVideo).mockResolvedValue({
      ...mockVideo,
      duration_seconds: 100,
      sponsorblock_segments: segments,
    });
    render(<Share token="3xK9raPb" />);
    return waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });
  }

  it("skips an ad segment and announces the skip", async () => {
    const videoEl = await renderWithSegments([
      { category: "sponsor", start_time: 10, end_time: 25 },
    ]);
    Object.defineProperty(videoEl, "currentTime", {
      value: 12,
      writable: true,
    });
    fireEvent.timeUpdate(videoEl);

    expect(videoEl.currentTime).toBe(25);
    expect(await screen.findByText(/Skipped ad/)).toBeInTheDocument();
  });

  it("plays through a marked segment and skips only the ad", async () => {
    const videoEl = await renderWithSegments([
      { category: "intro", start_time: 0, end_time: 8 },
      { category: "sponsor", start_time: 10, end_time: 25 },
    ]);
    Object.defineProperty(videoEl, "currentTime", { value: 3, writable: true });
    fireEvent.timeUpdate(videoEl);
    // Inside the intro and untouched: cutting it would remove video unasked.
    expect(videoEl.currentTime).toBe(3);

    videoEl.currentTime = 11;
    fireEvent.timeUpdate(videoEl);
    expect(videoEl.currentTime).toBe(25);
  });

  it("draws both band styles on the scrubber", async () => {
    await renderWithSegments([
      { category: "intro", start_time: 0, end_time: 8 },
      { category: "sponsor", start_time: 10, end_time: 25 },
    ]);
    expect(
      document.querySelector('[title="Skipped automatically: ad"]'),
    ).toBeTruthy();
    expect(document.querySelector('[title="Marked: intro"]')).toBeTruthy();
  });

  it("seeks from the scrubber without starting playback", async () => {
    // Clicking a chapter or a transcript line means "take me there and play";
    // moving the bar of a paused video must not start it.
    const videoEl = await renderWithSegments([
      { category: "sponsor", start_time: 10, end_time: 25 },
    ]);
    Object.defineProperty(videoEl, "currentTime", { value: 0, writable: true });
    const playSpy = vi
      .spyOn(window.HTMLMediaElement.prototype, "play")
      .mockResolvedValue(undefined);

    // jsdom lays nothing out, so the bar needs a rect before a click position
    // means anything: 400px wide, clicked at 300 → 75% of a 100s video.
    const bar = screen.getByRole("slider", { name: "Seek" });
    bar.getBoundingClientRect = () => ({ left: 0, width: 400 }) as DOMRect;
    fireEvent.click(bar, { clientX: 300 });

    expect(videoEl.currentTime).toBe(75);
    expect(playSpy).not.toHaveBeenCalled();
  });

  it("prefers the real media duration once metadata loads", async () => {
    // duration_seconds off the DTO is the fallback; the file itself wins as
    // soon as it can be read, so the bar's end matches the media.
    const videoEl = await renderWithSegments([
      { category: "sponsor", start_time: 10, end_time: 25 },
    ]);
    Object.defineProperty(videoEl, "duration", { value: 240, writable: true });
    fireEvent.loadedMetadata(videoEl);

    await waitFor(() => expect(screen.getByText("4:00")).toBeInTheDocument());
  });

  it("clears the skip notice after its timeout", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      const videoEl = await renderWithSegments([
        { category: "sponsor", start_time: 10, end_time: 25 },
      ]);
      Object.defineProperty(videoEl, "currentTime", {
        value: 12,
        writable: true,
      });
      fireEvent.timeUpdate(videoEl);
      expect(await screen.findByText(/Skipped ad/)).toBeInTheDocument();

      await act(async () => {
        vi.advanceTimersByTime(2600);
      });
      expect(screen.queryByText(/Skipped ad/)).not.toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("renders no scrubber at all when the video has no segments", async () => {
    vi.mocked(getSharedVideo).mockResolvedValue(mockVideo);
    render(<Share token="3xK9raPb" />);

    expect(await screen.findByText("Summary")).toBeInTheDocument();
    // The native <video> controls already seek; an empty second bar would be
    // two seek bars stacked for no gain.
    expect(
      screen.queryByRole("slider", { name: "Seek" }),
    ).not.toBeInTheDocument();
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

    expect(await screen.findByText(/not a feeling/i)).toBeInTheDocument();
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
    await screen.findByText(/not a feeling/i);

    fireEvent.change(screen.getByPlaceholderText(/find in transcript/i), {
      target: { value: "discipline" },
    });
    expect(screen.getByText("1 / 2")).toBeInTheDocument();
  });

  it("offers caption downloads named from the title — never the id or the token", async () => {
    renderWithCaptions();
    fireEvent.click(await screen.findByRole("button", { name: /transcript/i }));
    await screen.findByText(/not a feeling/i);

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

  it("copies the transcript text for the recipient too", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    renderWithCaptions();
    fireEvent.click(await screen.findByRole("button", { name: /transcript/i }));
    await screen.findByText(/not a feeling/i);

    fireEvent.click(screen.getByRole("button", { name: /copy text/i }));
    // Same payload as the .txt download, one cue per line.
    expect(writeText).toHaveBeenCalledWith(
      "Confidence is a discipline, not a feeling.\nSourcing is what separates the two.",
    );
    await screen.findByRole("button", { name: /copied/i });
  });

  it("drops a parsed transcript when the token changes", async () => {
    const { rerender } = renderWithCaptions();
    fireEvent.click(await screen.findByRole("button", { name: /transcript/i }));
    await screen.findByText(/not a feeling/i);

    // A different link must never show the previous video's cues, collapsed or
    // not — the panel goes back to its closed, empty state.
    rerender(<Share token="someOtherToken" />);
    await waitFor(() =>
      expect(screen.queryByText(/not a feeling/i)).not.toBeInTheDocument(),
    );
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
