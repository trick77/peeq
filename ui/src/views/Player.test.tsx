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
  summary: "",
  chapters: [],
  key_points: [],
  summary_status: "",
  audio_language: "",
  has_subtitles: false,
};

vi.mock("../api/videos", () => ({
  getVideo: vi.fn(),
  setFavorite: vi.fn().mockResolvedValue(true),
  setWatched: vi.fn().mockResolvedValue(true),
  setResume: vi.fn().mockResolvedValue(42),
  deleteVideo: vi.fn().mockResolvedValue(undefined),
  streamUrl: (id: string) => `/api/videos/${id}/stream`,
}));

vi.mock("../api/search", () => ({
  subtitlesUrl: (id: string) => `/api/videos/${id}/subtitles`,
  resummarize: vi.fn().mockResolvedValue(undefined),
}));

import { getVideo, setResume } from "../api/videos";
import { resummarize } from "../api/search";

function makeVideo(overrides: Partial<Video> = {}): Video {
  return { ...mockVideo, ...overrides };
}

describe("Player", () => {
  beforeEach(() => {
    vi.mocked(getVideo).mockReset();
    vi.mocked(setResume).mockClear();
    vi.mocked(resummarize).mockClear();
    vi.mocked(getVideo).mockResolvedValue(mockVideo);
    vi.unstubAllGlobals();
  });

  it("flushes the latest position to setResume on unmount", async () => {
    const { unmount } = render(<Player videoId="v1" onDeleted={() => {}} />);

    const videoEl = await waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });

    // First timeupdate: lastSentRef starts at 0, so this one posts
    // immediately (a real send, not the one under test).
    Object.defineProperty(videoEl, "currentTime", { value: 50, writable: true });
    fireEvent.timeUpdate(videoEl);
    await waitFor(() => expect(setResume).toHaveBeenCalledWith("v1", 50));
    vi.mocked(setResume).mockClear();

    // Second timeupdate lands inside the RESUME_THROTTLE_MS window, so on
    // its own it would NOT post — this is the progress that would
    // otherwise be silently discarded by unmounting (e.g. clicking back to
    // Library, the common in-SPA exit).
    Object.defineProperty(videoEl, "currentTime", { value: 77, writable: true });
    fireEvent.timeUpdate(videoEl);
    expect(setResume).not.toHaveBeenCalled();

    unmount();

    await waitFor(() => {
      expect(setResume).toHaveBeenCalledWith("v1", 77);
    });
  });

  it("does not clobber the stored resume with 0 when unmounted before any position is observed", async () => {
    const { unmount } = render(<Player videoId="v1" onDeleted={() => {}} />);

    // Wait for the video element to mount, but never fire loadedMetadata
    // or timeupdate — this is the quick-exit window where the playhead's
    // real position (mockVideo.resume_position_seconds = 42, already
    // stored server-side) is not yet known client-side.
    await waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });

    unmount();

    // Give any queued microtasks a chance to run, then confirm the
    // unmount flush stayed silent instead of posting a spurious 0.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(setResume).not.toHaveBeenCalled();
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

  it("seeks to seekTo instead of resume_position_seconds when set (Task 18 jump-to-moment)", async () => {
    render(<Player videoId="v1" seekTo={560} onDeleted={() => {}} />);

    const videoEl = await waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });

    fireEvent.loadedMetadata(videoEl);

    expect(videoEl.currentTime).toBeCloseTo(560, 0);
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

  it("renders the summary paragraphs when summary_status is done", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({ summary_status: "done", summary: "Prose one.\n\nProse two." }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);
    expect(await screen.findByText("Prose one.")).toBeInTheDocument();
    expect(screen.getByText("Prose two.")).toBeInTheDocument();
  });

  it("shows No transcript available for a no_transcript summary status", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ summary_status: "no_transcript" }));
    render(<Player videoId="v1" onDeleted={() => {}} />);
    expect(await screen.findByText(/No transcript available/i)).toBeInTheDocument();
  });

  it("shows a Re-summarize button on error status and calls resummarize", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ summary_status: "error" }));
    render(<Player videoId="v1" onDeleted={() => {}} />);
    const btn = await screen.findByRole("button", { name: /Re-summarize/i });
    fireEvent.click(btn);
    await waitFor(() => expect(resummarize).toHaveBeenCalledWith("v1"));
  });

  it("clicking a chapter seeks the video to its timestamp", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({ chapters: [{ ts: 108, title: "Frame", source: "yt-dlp" }] }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);
    const chapterBtn = await screen.findByRole("button", { name: /Frame/ });
    const vid = document.querySelector("video") as HTMLVideoElement;
    const seekSpy = vi.spyOn(vid, "currentTime", "set");
    fireEvent.click(chapterBtn);
    expect(seekSpy).toHaveBeenCalledWith(108);
  });

  it("clicking a highlight seeks the video to its timestamp", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({ key_points: [{ ts: 12, text: "wow moment" }] }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);
    const hlBtn = await screen.findByRole("button", { name: /wow moment/ });
    const vid = document.querySelector("video") as HTMLVideoElement;
    const seekSpy = vi.spyOn(vid, "currentTime", "set");
    fireEvent.click(hlBtn);
    expect(seekSpy).toHaveBeenCalledWith(12);
  });

  it("toggles the CC track mode between hidden and showing", async () => {
    // jsdom never populates HTMLMediaElement.textTracks from a <track>
    // child, so the mode flip is exercised against a stubbed TextTrackList
    // (mirroring how the existing tests stub `currentTime`).
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_subtitles: true }));
    render(<Player videoId="v1" onDeleted={() => {}} />);
    const ccBtn = await screen.findByRole("button", { name: /^CC$/ });
    const vid = document.querySelector("video") as HTMLVideoElement;
    const fakeTrack = { mode: "hidden" } as unknown as TextTrack;
    Object.defineProperty(vid, "textTracks", { value: [fakeTrack], configurable: true });

    fireEvent.click(ccBtn);
    expect(fakeTrack.mode).toBe("showing");
    fireEvent.click(ccBtn);
    expect(fakeTrack.mode).toBe("hidden");
  });

  it("fetches and shows the transcript, seeking on cue click, once expanded", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_subtitles: true }));
    const vtt = "WEBVTT\n\n00:00:05.000 --> 00:00:08.000\nHello there\n\n00:00:10.000 --> 00:00:12.000\nBattery life is great\n";
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, text: () => Promise.resolve(vtt) }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);
    const toggle = await screen.findByRole("button", { name: /Transcript/i });
    fireEvent.click(toggle);
    expect(await screen.findByText(/Battery life is great/i)).toBeInTheDocument();

    const cueBtn = screen.getByRole("button", { name: /Battery life is great/i });
    const vid = document.querySelector("video") as HTMLVideoElement;
    const seekSpy = vi.spyOn(vid, "currentTime", "set");
    fireEvent.click(cueBtn);
    expect(seekSpy).toHaveBeenCalledWith(10);
  });

  it("highlights matching transcript rows via the find box", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_subtitles: true }));
    const vtt = "WEBVTT\n\n00:00:05.000 --> 00:00:08.000\nHello there\n\n00:00:10.000 --> 00:00:12.000\nBattery life is great\n";
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, text: () => Promise.resolve(vtt) }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);
    const toggle = await screen.findByRole("button", { name: /Transcript/i });
    fireEvent.click(toggle);
    await screen.findByText(/Battery life is great/i);

    const findBox = screen.getByPlaceholderText(/Find in transcript/i);
    fireEvent.change(findBox, { target: { value: "battery" } });

    const markEl = await screen.findByText(/battery/i, { selector: "mark" });
    expect(markEl).toBeInTheDocument();
    const cueBtn = markEl.closest("button");
    expect(cueBtn).toHaveClass("hit");
    const helloRow = screen.getByText("Hello there").closest("button");
    expect(helloRow).not.toHaveClass("hit");
  });
});
