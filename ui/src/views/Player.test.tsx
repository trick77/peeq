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
  category: "uncategorized",
};

vi.mock("../api/videos", () => ({
  getVideo: vi.fn(),
  setFavorite: vi.fn().mockResolvedValue(true),
  setWatched: vi.fn().mockResolvedValue(true),
  setResume: vi.fn().mockResolvedValue(42),
  deleteVideo: vi.fn().mockResolvedValue(undefined),
  redownload: vi.fn().mockResolvedValue(undefined),
  streamUrl: (id: string) => `/api/videos/${id}/stream`,
  thumbnailUrl: (id: string) => `/api/videos/${id}/thumbnail`,
}));

vi.mock("../api/search", () => ({
  subtitlesUrl: (id: string) => `/api/videos/${id}/subtitles`,
  resummarize: vi.fn().mockResolvedValue(undefined),
}));

// streamDownloads defaults to a never-resolving promise so it doesn't
// interfere with (or hang) any of the other Player tests, which don't care
// about the SSE feed at all — individual tests below override this mock to
// capture the onEvent callback and drive it directly.
vi.mock("../api/downloads", () => ({
  streamDownloads: vi.fn().mockImplementation(() => new Promise(() => {})),
}));

// Player reads the global subtitles preference on mount, so this mock is
// needed by EVERY test in this file, not just the subtitles ones — an
// unmocked getSettings would reject on mount everywhere.
vi.mock("../api/settings", () => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
}));

import { getVideo, setResume, redownload, deleteVideo } from "../api/videos";
import { resummarize } from "../api/search";
import { getSettings, updateSettings } from "../api/settings";
import type { Settings } from "../api/types";
import { gradientClassFor } from "../format";
import { streamDownloads } from "../api/downloads";

function makeVideo(overrides: Partial<Video> = {}): Video {
  return { ...mockVideo, ...overrides };
}

// Player only ever reads subtitles_default off Settings, but the mock
// returns a whole (cast) object so the shape stays honest.
function makeSettings(subtitlesDefault: boolean): Settings {
  return { subtitles_default: subtitlesDefault } as Settings;
}

describe("Player", () => {
  beforeEach(() => {
    vi.mocked(getVideo).mockReset();
    vi.mocked(setResume).mockClear();
    vi.mocked(resummarize).mockClear();
    vi.mocked(redownload).mockClear();
    vi.mocked(deleteVideo).mockClear();
    vi.mocked(getVideo).mockResolvedValue(mockVideo);
    vi.mocked(streamDownloads).mockReset();
    vi.mocked(streamDownloads).mockImplementation(() => new Promise(() => {}));
    vi.mocked(getSettings).mockReset();
    vi.mocked(getSettings).mockResolvedValue(makeSettings(false));
    vi.mocked(updateSettings).mockReset();
    vi.mocked(updateSettings).mockResolvedValue(makeSettings(false));
    vi.unstubAllGlobals();
    sessionStorage.clear();
  });

  describe("stage poster", () => {
    it("posters the video with its thumbnail when one was downloaded", async () => {
      vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_thumbnail: true }));
      render(<Player videoId="v1" onDeleted={() => {}} />);

      const el = await waitFor(() => {
        const v = document.querySelector("video");
        if (!v) throw new Error("video element not mounted yet");
        return v;
      });

      expect(el).toHaveAttribute("poster", "/api/videos/v1/thumbnail");
      // A real thumbnail means no gradient fallback — the poster covers it.
      expect(el.className).toBe("");
    });

    it("falls back to the per-id gradient when there is no thumbnail", async () => {
      vi.mocked(getVideo).mockResolvedValue(
        makeVideo({ has_thumbnail: false }),
      );
      render(<Player videoId="v1" onDeleted={() => {}} />);

      const el = await waitFor(() => {
        const v = document.querySelector("video");
        if (!v) throw new Error("video element not mounted yet");
        return v;
      });

      expect(el).not.toHaveAttribute("poster");
      expect(el.className).toBe(gradientClassFor("v1"));
    });
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
    Object.defineProperty(videoEl, "currentTime", {
      value: 50,
      writable: true,
    });
    fireEvent.timeUpdate(videoEl);
    await waitFor(() => expect(setResume).toHaveBeenCalledWith("v1", 50));
    vi.mocked(setResume).mockClear();

    // Second timeupdate lands inside the RESUME_THROTTLE_MS window, so on
    // its own it would NOT post — this is the progress that would
    // otherwise be silently discarded by unmounting (e.g. clicking back to
    // Library, the common in-SPA exit).
    Object.defineProperty(videoEl, "currentTime", {
      value: 77,
      writable: true,
    });
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

  it("calls onSeekConsumed exactly once right after applying seekTo", async () => {
    const onSeekConsumed = vi.fn();
    render(
      <Player
        videoId="v1"
        seekTo={560}
        onSeekConsumed={onSeekConsumed}
        onDeleted={() => {}}
      />,
    );

    const videoEl = await waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });

    fireEvent.loadedMetadata(videoEl);

    expect(videoEl.currentTime).toBeCloseTo(560, 0);
    expect(onSeekConsumed).toHaveBeenCalledTimes(1);

    // A second loadedmetadata on the same mount must not re-apply or
    // re-consume — handleLoadedMetadata's resumeAppliedRef guard already
    // makes this a no-op regardless of seekTo.
    fireEvent.loadedMetadata(videoEl);
    expect(onSeekConsumed).toHaveBeenCalledTimes(1);
  });

  it("regression: a remount without seekTo (the stale-pendingSeek scenario) uses resume, not the earlier seek", async () => {
    // Simulates the real bug: a search jump seeks to 560s and the parent
    // clears its pendingSeek via onSeekConsumed. If the Player is later
    // remounted (e.g. via the rail's "Now playing" link) with no seekTo
    // prop — because the parent's pendingSeek was actually cleared — it
    // must resume at resume_position_seconds (42), never replay the old
    // 560s jump.
    const onSeekConsumed = vi.fn();
    const { unmount } = render(
      <Player
        videoId="v1"
        seekTo={560}
        onSeekConsumed={onSeekConsumed}
        onDeleted={() => {}}
      />,
    );

    const firstEl = await waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });
    fireEvent.loadedMetadata(firstEl);
    expect(firstEl.currentTime).toBeCloseTo(560, 0);
    expect(onSeekConsumed).toHaveBeenCalledTimes(1);

    unmount();

    // Remount with no seekTo — the one-shot consumption means a parent
    // wired to onSeekConsumed would have cleared pendingSeek by now, so
    // this is exactly what a rail "Now playing" remount looks like.
    render(<Player videoId="v1" onDeleted={() => {}} />);
    const secondEl = await waitFor(() => {
      const els = document.querySelectorAll("video");
      if (els.length === 0) throw new Error("video element not mounted yet");
      return els[els.length - 1];
    });
    fireEvent.loadedMetadata(secondEl);

    expect(secondEl.currentTime).toBeCloseTo(42, 0);
  });

  it("posts the current position to setResume on timeupdate", async () => {
    render(<Player videoId="v1" onDeleted={() => {}} />);

    const videoEl = await waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });
    Object.defineProperty(videoEl, "currentTime", {
      value: 100,
      writable: true,
    });

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

  it("renders a download link carrying the download=1 filename flag", async () => {
    render(<Player videoId="v1" onDeleted={() => {}} />);
    const link = await screen.findByRole("link", { name: /download video/i });
    // download=1 is what makes the server attach a Content-Disposition with
    // the real filename — a plain `download` attribute cannot, since the UI
    // never learns the file's extension.
    expect(link).toHaveAttribute("href", "/api/videos/v1/stream?download=1");
  });

  it("hides the download link for a video with no media file", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_media: false }));
    render(<Player videoId="v1" onDeleted={() => {}} />);
    await screen.findByRole("link", { name: /watch on youtube/i });
    expect(screen.queryByRole("link", { name: /download video/i })).toBeNull();
  });

  describe("delete confirmation", () => {
    it("does not delete on the first click — it only arms the confirm", async () => {
      render(<Player videoId="v1" onDeleted={() => {}} />);
      fireEvent.click(
        await screen.findByRole("button", { name: /delete video/i }),
      );
      expect(deleteVideo).not.toHaveBeenCalled();
      expect(screen.getByRole("button", { name: /delete\?/i })).toBeTruthy();
    });

    it("deletes on the second click", async () => {
      const onDeleted = vi.fn();
      render(<Player videoId="v1" onDeleted={onDeleted} />);
      fireEvent.click(
        await screen.findByRole("button", { name: /delete video/i }),
      );
      fireEvent.click(screen.getByRole("button", { name: /delete\?/i }));
      await waitFor(() => expect(deleteVideo).toHaveBeenCalledWith("v1"));
      await waitFor(() => expect(onDeleted).toHaveBeenCalled());
    });

    it("disarms on Escape without deleting", async () => {
      render(<Player videoId="v1" onDeleted={() => {}} />);
      fireEvent.click(
        await screen.findByRole("button", { name: /delete video/i }),
      );
      fireEvent.keyDown(screen.getByRole("button", { name: /delete\?/i }), {
        key: "Escape",
      });
      await screen.findByRole("button", { name: /delete video/i });
      expect(deleteVideo).not.toHaveBeenCalled();
    });

    it("disarms itself after the timeout without deleting", async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      try {
        render(<Player videoId="v1" onDeleted={() => {}} />);
        fireEvent.click(
          await screen.findByRole("button", { name: /delete video/i }),
        );
        expect(screen.getByRole("button", { name: /delete\?/i })).toBeTruthy();
        await vi.advanceTimersByTimeAsync(4100);
        await screen.findByRole("button", { name: /delete video/i });
        expect(deleteVideo).not.toHaveBeenCalled();
      } finally {
        vi.useRealTimers();
      }
    });
  });

  it("shows a placeholder message with nothing selected", () => {
    render(<Player videoId={null} onDeleted={() => {}} />);
    expect(
      screen.getByText(/Pick a video from the Library/i),
    ).toBeInTheDocument();
  });

  it("renders the summary paragraphs when summary_status is done", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({
        summary_status: "done",
        summary: "Prose one.\n\nProse two.",
      }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);
    expect(await screen.findByText("Prose one.")).toBeInTheDocument();
    expect(screen.getByText("Prose two.")).toBeInTheDocument();
  });

  it("shows No transcript available for a no_transcript summary status", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({ summary_status: "no_transcript" }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);
    expect(
      await screen.findByText(/No transcript available/i),
    ).toBeInTheDocument();
  });

  it("shows a Re-summarize button on error status and calls resummarize", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({ summary_status: "error" }),
    );
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
    // jsdom never populates HTMLMediaElement.textTracks from a <track> child,
    // so the mode flip is exercised against a stubbed TextTrackList.
    //
    // The stub is installed on the prototype BEFORE render (not on the instance
    // after it), and we wait for the "captions off on load" effect (Player.tsx
    // ~L268) to have run before clicking. Otherwise that passive effect can
    // still be pending when the click fires — React 19 defers passive effects
    // to a macrotask, and findByRole resolving does not guarantee they flushed.
    // The click's act() would then drain it *after* the handler set "showing",
    // reverting the track to "hidden" and failing intermittently under CI's
    // slower workers (issue #53). The "disabled" sentinel starts as a value
    // only that effect clears, so the wait proves the effect actually ran.
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_subtitles: true }));
    const fakeTrack = { mode: "disabled" } as unknown as TextTrack;
    const ttSpy = vi
      .spyOn(HTMLMediaElement.prototype, "textTracks", "get")
      .mockReturnValue([fakeTrack] as unknown as TextTrackList);
    try {
      render(<Player videoId="v1" onDeleted={() => {}} />);
      const ccBtn = await screen.findByRole("button", {
        name: /^Subtitles (on|off)$/,
      });
      await waitFor(() => expect(fakeTrack.mode).toBe("hidden"));

      fireEvent.click(ccBtn);
      expect(fakeTrack.mode).toBe("showing");
      fireEvent.click(ccBtn);
      expect(fakeTrack.mode).toBe("hidden");
    } finally {
      ttSpy.mockRestore();
    }
  });

  it("starts subtitles showing when the global default is on", async () => {
    // Same prototype-spy + sentinel technique as the toggle test above: the
    // "disabled" start value is one only the apply-the-default effect can
    // clear, so waiting for it proves the effect really ran.
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_subtitles: true }));
    vi.mocked(getSettings).mockResolvedValue(makeSettings(true));
    const fakeTrack = { mode: "disabled" } as unknown as TextTrack;
    const ttSpy = vi
      .spyOn(HTMLMediaElement.prototype, "textTracks", "get")
      .mockReturnValue([fakeTrack] as unknown as TextTrackList);
    try {
      render(<Player videoId="v1" onDeleted={() => {}} />);
      await waitFor(() => expect(fakeTrack.mode).toBe("showing"));
      expect(
        await screen.findByRole("button", { name: "Subtitles on" }),
      ).toHaveAttribute("aria-pressed", "true");
    } finally {
      ttSpy.mockRestore();
    }
  });

  it("writes the flipped value back as the global default when toggled", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_subtitles: true }));
    const fakeTrack = { mode: "disabled" } as unknown as TextTrack;
    const ttSpy = vi
      .spyOn(HTMLMediaElement.prototype, "textTracks", "get")
      .mockReturnValue([fakeTrack] as unknown as TextTrackList);
    try {
      render(<Player videoId="v1" onDeleted={() => {}} />);
      const ccBtn = await screen.findByRole("button", {
        name: /^Subtitles (on|off)$/,
      });
      await waitFor(() => expect(fakeTrack.mode).toBe("hidden"));

      fireEvent.click(ccBtn);
      expect(updateSettings).toHaveBeenCalledWith({ subtitles_default: true });

      // The toggle updates the preference the apply-effect depends on, so
      // this is also the regression guard for that effect snapping the
      // track back to the default mid-video.
      await waitFor(() => expect(fakeTrack.mode).toBe("showing"));

      fireEvent.click(ccBtn);
      expect(updateSettings).toHaveBeenCalledWith({ subtitles_default: false });
    } finally {
      ttSpy.mockRestore();
    }
  });

  it("keeps playing when the settings read fails", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_subtitles: true }));
    vi.mocked(getSettings).mockRejectedValue(new Error("settings are down"));
    const fakeTrack = { mode: "disabled" } as unknown as TextTrack;
    const ttSpy = vi
      .spyOn(HTMLMediaElement.prototype, "textTracks", "get")
      .mockReturnValue([fakeTrack] as unknown as TextTrackList);
    try {
      render(<Player videoId="v1" onDeleted={() => {}} />);
      // Falls back to subtitles-off, and the video still renders.
      await waitFor(() => expect(fakeTrack.mode).toBe("hidden"));
      expect(document.querySelector("video")).toBeInTheDocument();
    } finally {
      ttSpy.mockRestore();
    }
  });

  it("fetches and shows the transcript, seeking on cue click, once expanded", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_subtitles: true }));
    const vtt =
      "WEBVTT\n\n00:00:05.000 --> 00:00:08.000\nHello there\n\n00:00:10.000 --> 00:00:12.000\nBattery life is great\n";
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, text: () => Promise.resolve(vtt) }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);
    const toggle = await screen.findByRole("button", { name: /Transcript/i });
    fireEvent.click(toggle);
    expect(
      await screen.findByText(/Battery life is great/i),
    ).toBeInTheDocument();

    const cueBtn = screen.getByRole("button", {
      name: /Battery life is great/i,
    });
    const vid = document.querySelector("video") as HTMLVideoElement;
    const seekSpy = vi.spyOn(vid, "currentTime", "set");
    fireEvent.click(cueBtn);
    expect(seekSpy).toHaveBeenCalledWith(10);
  });

  it("highlights matching transcript rows via the find box", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_subtitles: true }));
    const vtt =
      "WEBVTT\n\n00:00:05.000 --> 00:00:08.000\nHello there\n\n00:00:10.000 --> 00:00:12.000\nBattery life is great\n";
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

  it("updates live when a summary SSE event arrives for the open video", async () => {
    vi.mocked(getVideo)
      .mockResolvedValueOnce(makeVideo({ id: "v1", summary_status: "running" }))
      .mockResolvedValueOnce(
        makeVideo({
          id: "v1",
          summary_status: "done",
          summary: "Fresh summary.",
        }),
      );
    // streamSSE (the real ../api/downloads dependency) already parses each
    // frame's JSON body before invoking onEvent — App.tsx's own "progress"
    // handler consumes evt.data the same way, uncast-and-parsed — so the
    // mock here hands the callback an already-parsed object, not a string.
    let emit: (e: { event: string; data: unknown }) => void = () => {};
    vi.mocked(streamDownloads).mockImplementation((onEvent) => {
      emit = onEvent as (e: { event: string; data: unknown }) => void;
      return new Promise(() => {}); // never resolves
    });
    render(<Player videoId="v1" onDeleted={() => {}} />);
    expect(await screen.findByText(/summarizing/i)).toBeInTheDocument();

    emit({
      event: "summary",
      data: { video_id: "v1", status: "done", phase: "" },
    });

    expect(await screen.findByText(/fresh summary/i)).toBeInTheDocument();
  });

  it("renders a MiMo tag on a summarizer-generated chapter", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({ chapters: [{ ts: 108, title: "Frame", source: "mimo" }] }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);
    expect(await screen.findByText(/mimo/i)).toBeInTheDocument();
  });

  it("shows Re-download for an errored video and queues it", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({ id: "v1", status: "error" }),
    );
    vi.mocked(redownload).mockResolvedValue(undefined);
    render(<Player videoId="v1" onDeleted={() => {}} />);
    const btn = await screen.findByRole("button", { name: /re-download/i });
    fireEvent.click(btn);
    await waitFor(() => expect(redownload).toHaveBeenCalledWith("v1"));
  });

  it("hides Re-download for a downloaded video", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({ id: "v1", status: "downloaded" }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);
    await screen.findByRole("link", { name: /watch on youtube/i }); // wait for load
    expect(screen.queryByRole("button", { name: /re-download/i })).toBeNull();
  });

  it("renders the channel name as a clickable link when onOpenChannel is provided", async () => {
    const onOpenChannel = vi.fn();
    render(
      <Player
        videoId="v1"
        onDeleted={() => {}}
        onOpenChannel={onOpenChannel}
      />,
    );
    const link = await screen.findByRole("button", { name: "Veritasium" });
    fireEvent.click(link);
    expect(onOpenChannel).toHaveBeenCalledWith("chan1");
  });

  it("renders the channel name as plain text when onOpenChannel is absent", async () => {
    render(<Player videoId="v1" onDeleted={() => {}} />);
    await screen.findByText("Veritasium");
    expect(screen.queryByRole("button", { name: "Veritasium" })).toBeNull();
  });

  it("offers .txt and .vtt transcript downloads once the transcript is loaded", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({ has_subtitles: true, title: "My Video" }),
    );
    const vtt = "WEBVTT\n\n00:00:05.000 --> 00:00:08.000\nHello there\n";
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, text: () => Promise.resolve(vtt) }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);
    const toggle = await screen.findByRole("button", { name: /Transcript/i });
    fireEvent.click(toggle);
    await screen.findByText(/Hello there/i);

    // .vtt links the subtitle endpoint with a safe filename from the title.
    const vttLink = screen.getByRole("link", { name: /\.vtt/i });
    expect(vttLink).toHaveAttribute("href", "/api/videos/v1/subtitles");
    expect(vttLink).toHaveAttribute("download", "My_Video.vtt");

    // .txt is generated client-side from the cues; clicking it builds a Blob
    // and triggers a download (jsdom lacks createObjectURL, so stub it).
    const createURL = vi.fn(() => "blob:mock");
    const revokeURL = vi.fn();
    URL.createObjectURL = createURL;
    URL.revokeObjectURL = revokeURL;
    fireEvent.click(screen.getByRole("button", { name: /\.txt/i }));
    expect(createURL).toHaveBeenCalledTimes(1);
    expect(revokeURL).toHaveBeenCalledTimes(1);
  });
});
