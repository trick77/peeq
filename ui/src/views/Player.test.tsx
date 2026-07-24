import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { Player, parseVtt } from "./Player";
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
  setCategory: vi.fn().mockResolvedValue("ai"),
  setResume: vi.fn().mockResolvedValue(42),
  deleteVideo: vi.fn().mockResolvedValue(undefined),
  redownload: vi.fn().mockResolvedValue(undefined),
  streamUrl: (id: string) => `/api/videos/${id}/stream`,
  thumbnailUrl: (id: string) => `/api/videos/${id}/thumbnail`,
}));

vi.mock("../api/search", () => ({
  subtitlesUrl: (id: string) => `/api/videos/${id}/subtitles`,
  reprocess: vi.fn().mockResolvedValue(undefined),
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

import {
  getVideo,
  setResume,
  setWatched,
  setFavorite,
  redownload,
  deleteVideo,
  setCategory,
} from "../api/videos";
import { reprocess } from "../api/search";
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

// openMenu opens the Player's ⋮ actions menu so its items (Reprocess,
// Re-download, Download file, Watch on YouTube, Delete…) become queryable —
// they are not in the DOM until the menu is open.
async function openMenu() {
  fireEvent.click(
    await screen.findByRole("button", { name: /video actions/i }),
  );
}

describe("Player", () => {
  beforeEach(() => {
    vi.mocked(getVideo).mockReset();
    vi.mocked(setResume).mockClear();
    vi.mocked(reprocess).mockClear();
    vi.mocked(redownload).mockClear();
    vi.mocked(deleteVideo).mockClear();
    // mockReset, not mockClear: these carry per-test queued outcomes
    // (mockRejectedValueOnce). A queued rejection that its own test never
    // consumed would otherwise detonate in the next test that toggles, and
    // cumulative call counts make `toHaveBeenCalled()` gates pass on a
    // previous test's click.
    vi.mocked(setWatched).mockReset();
    vi.mocked(setWatched).mockResolvedValue(true);
    vi.mocked(setFavorite).mockReset();
    vi.mocked(setFavorite).mockResolvedValue(true);
    vi.mocked(setCategory).mockReset();
    vi.mocked(setCategory).mockResolvedValue("ai");
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

  it.each([
    { label: "watched", watched: false, name: "Mark watched" },
    { label: "unwatched", watched: true, name: "Mark unwatched" },
  ])(
    "stops playback and rewinds to 0:00 when marking $label",
    async ({ watched, name }) => {
      // Both directions are a deliberate "done with this for now": playback
      // stops and the playhead rewinds to match the position the server has
      // just zeroed. Leaving it playing is what let the old position get
      // written straight back by the next timeupdate.
      vi.mocked(getVideo).mockResolvedValue(
        makeVideo({ watched, duration_seconds: 100 }),
      );
      render(<Player videoId="v1" onDeleted={() => {}} />);

      const videoEl = await waitFor(() => {
        const el = document.querySelector("video");
        if (!el) throw new Error("video element not mounted yet");
        return el;
      });
      const pause = vi.fn();
      videoEl.pause = pause;
      Object.defineProperty(videoEl, "paused", {
        value: false,
        writable: true,
      });
      Object.defineProperty(videoEl, "currentTime", {
        value: 40,
        writable: true,
      });
      fireEvent.timeUpdate(videoEl);
      await waitFor(() => expect(setResume).toHaveBeenCalledWith("v1", 40));
      vi.mocked(setResume).mockClear();

      fireEvent.click(screen.getByRole("button", { name }));

      await waitFor(() =>
        expect(setWatched).toHaveBeenCalledWith("v1", !watched),
      );
      expect(pause).toHaveBeenCalled();
      expect(videoEl.currentTime).toBe(0);
    },
  );

  it("skips a SponsorBlock segment, and a later toast replaces the skip notice", async () => {
    // Two toasts back to back: the second must reset the shared timer, or the
    // first one's timeout would dismiss it early.
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({
        watched: true,
        duration_seconds: 100,
        sponsorblock_segments: [
          { category: "sponsor", start_time: 10, end_time: 25 },
        ],
      }),
    );
    vi.mocked(setWatched).mockRejectedValueOnce(new Error("network down"));
    render(<Player videoId="v1" onDeleted={() => {}} />);

    const videoEl = await waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });
    videoEl.pause = vi.fn();
    Object.defineProperty(videoEl, "currentTime", {
      value: 12,
      writable: true,
    });
    fireEvent.timeUpdate(videoEl);

    // Playhead jumped past the segment, and the skip is announced.
    expect(videoEl.currentTime).toBe(25);
    expect(await screen.findByText(/Skipped ad/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Mark unwatched" }));

    await waitFor(() =>
      expect(screen.getByText("Couldn't mark unwatched.")).toBeInTheDocument(),
    );
    expect(screen.queryByText(/Skipped ad/)).not.toBeInTheDocument();
  });

  it("plays through a marked segment and skips only the ad", async () => {
    // The categories outside AUTO_SKIP are drawn on the scrubber but must
    // never be cut: skipping an intro or a non-music section would silently
    // remove video the viewer chose to watch.
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({
        duration_seconds: 100,
        sponsorblock_segments: [
          { category: "intro", start_time: 0, end_time: 8 },
          { category: "sponsor", start_time: 10, end_time: 25 },
        ],
      }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);

    const videoEl = await waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });

    // Inside the intro: the playhead stays put and nothing is announced.
    Object.defineProperty(videoEl, "currentTime", { value: 3, writable: true });
    fireEvent.timeUpdate(videoEl);
    expect(videoEl.currentTime).toBe(3);
    expect(screen.queryByText(/Skipped/)).not.toBeInTheDocument();

    // Inside the sponsor read: skipped, as before.
    videoEl.currentTime = 12;
    fireEvent.timeUpdate(videoEl);
    expect(videoEl.currentTime).toBe(25);
    expect(await screen.findByText(/Skipped ad/)).toBeInTheDocument();
  });

  it("labels both band styles on the scrubber legend", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({
        duration_seconds: 100,
        sponsorblock_segments: [
          { category: "intro", start_time: 0, end_time: 8 },
          { category: "sponsor", start_time: 10, end_time: 25 },
        ],
      }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);

    expect(await screen.findByText("skipped")).toBeInTheDocument();
    expect(screen.getByText("marked")).toBeInTheDocument();
    // The bands themselves carry the human-readable category.
    expect(
      document.querySelector('[title="Marked: intro"]'),
    ).toBeInTheDocument();
    expect(
      document.querySelector('[title="Skipped automatically: ad"]'),
    ).toBeInTheDocument();
  });

  it("rolls the favorite back when the write fails", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ favorite: false }));
    vi.mocked(setFavorite).mockRejectedValueOnce(new Error("network down"));
    render(<Player videoId="v1" onDeleted={() => {}} />);
    await screen.findByText("Keep forever");

    fireEvent.click(screen.getByRole("button", { name: /Keep forever/ }));

    await waitFor(() =>
      expect(screen.getByText("Keep forever")).toBeInTheDocument(),
    );
    expect(setFavorite).toHaveBeenCalledWith("v1", true);
  });

  it("leaves the next video alone when a toggle fails after switching away", async () => {
    // The failure lands after the user has moved on. Everything in the catch
    // — the state rollback, the playhead restore, the toast — belongs to the
    // video that was toggled, not to whatever is on screen now.
    const first = makeVideo({
      id: "v1",
      watched: false,
      duration_seconds: 100,
      resume_position_seconds: 0,
    });
    const second = makeVideo({
      id: "v2",
      title: "A Different Video",
      watched: false,
      duration_seconds: 100,
    });
    vi.mocked(getVideo).mockImplementation(async (id: string) =>
      id === "v1" ? first : second,
    );
    let rejectToggle: (e: Error) => void = () => {};
    vi.mocked(setWatched).mockReturnValueOnce(
      new Promise((_, reject) => {
        rejectToggle = reject;
      }),
    );

    const { rerender } = render(<Player videoId="v1" onDeleted={() => {}} />);
    const videoEl = await waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });
    videoEl.pause = vi.fn();
    Object.defineProperty(videoEl, "currentTime", {
      value: 40,
      writable: true,
    });
    fireEvent.timeUpdate(videoEl);

    fireEvent.click(screen.getByRole("button", { name: "Mark watched" }));
    // Switch videos with the request still in flight, then let it fail.
    rerender(<Player videoId="v2" onDeleted={() => {}} />);
    await screen.findByText("A Different Video");
    rejectToggle(new Error("network down"));

    await new Promise((resolve) => setTimeout(resolve, 0));

    // v2's own state, playhead and stage are untouched by v1's failure.
    expect(
      screen.getByRole("button", { name: "Mark watched" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Couldn't mark watched."),
    ).not.toBeInTheDocument();
    const secondEl = document.querySelector("video") as HTMLVideoElement;
    expect(secondEl.currentTime).toBe(0);
  });

  it("restores the playhead and resumes playback when the toggle fails", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({ watched: false, duration_seconds: 100 }),
    );
    vi.mocked(setWatched).mockRejectedValueOnce(new Error("network down"));
    render(<Player videoId="v1" onDeleted={() => {}} />);

    const videoEl = await waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });
    videoEl.pause = vi.fn();
    const play = vi.fn();
    videoEl.play = play;
    // Playing at the moment of the click — that is what the rollback has to
    // put back, and jsdom's default (paused: true) would skip the branch.
    Object.defineProperty(videoEl, "paused", { value: false, writable: true });
    Object.defineProperty(videoEl, "currentTime", {
      value: 40,
      writable: true,
    });
    fireEvent.timeUpdate(videoEl);

    fireEvent.click(screen.getByRole("button", { name: "Mark watched" }));

    // A failed request must leave the player where it found it, not parked at
    // 0:00 with the pre-toggle state restored around it.
    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Mark watched" }),
      ).toBeInTheDocument();
      expect(videoEl.currentTime).toBe(40);
    });
    expect(play).toHaveBeenCalled();
    // And it says so, rather than reading as a button that did nothing.
    expect(screen.getByText("Couldn't mark watched.")).toBeInTheDocument();
  });

  it("does not re-post the old position after the video is un-watched", async () => {
    // The regression this guards: un-watching clears resume_position_seconds
    // server-side, but the Player's own flush effect would then re-POST the
    // pre-toggle playhead on unmount, restoring what the server just cleared
    // — and at 95 of 100 seconds that re-crosses the 90% auto-watched
    // threshold, silently undoing the un-watch.
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({ watched: true, duration_seconds: 100 }),
    );
    const { unmount } = render(<Player videoId="v1" onDeleted={() => {}} />);

    const videoEl = await waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });
    Object.defineProperty(videoEl, "currentTime", {
      value: 95,
      writable: true,
    });
    fireEvent.timeUpdate(videoEl);
    await waitFor(() => expect(setResume).toHaveBeenCalledWith("v1", 95));
    vi.mocked(setResume).mockClear();

    fireEvent.click(screen.getByRole("button", { name: "Mark unwatched" }));
    await waitFor(() => expect(setWatched).toHaveBeenCalledWith("v1", false));

    unmount();

    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(setResume).not.toHaveBeenCalled();
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

  it('the ⋮ menu holds a "Watch on YouTube" link to the video url', async () => {
    render(<Player videoId="v1" onDeleted={() => {}} />);
    await openMenu();
    const link = await screen.findByRole("menuitem", {
      name: /Watch on YouTube/i,
    });
    expect(link).toHaveAttribute("href", "https://youtu.be/v1");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noreferrer");
  });

  it("the ⋮ menu's download item carries the download=1 filename flag", async () => {
    render(<Player videoId="v1" onDeleted={() => {}} />);
    await openMenu();
    const link = await screen.findByRole("menuitem", {
      name: /download file/i,
    });
    // download=1 is what makes the server attach a Content-Disposition with
    // the real filename — a plain `download` attribute cannot, since the UI
    // never learns the file's extension.
    expect(link).toHaveAttribute("href", "/api/videos/v1/stream?download=1");
  });

  it("omits the download item for a video with no media file", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_media: false }));
    render(<Player videoId="v1" onDeleted={() => {}} />);
    await openMenu();
    await screen.findByRole("menuitem", { name: /watch on youtube/i });
    expect(
      screen.queryByRole("menuitem", { name: /download file/i }),
    ).toBeNull();
  });

  describe("delete confirmation", () => {
    it("the ⋮ Delete… item opens a confirm dialog and does not delete yet", async () => {
      render(<Player videoId="v1" onDeleted={() => {}} />);
      await openMenu();
      fireEvent.click(await screen.findByRole("menuitem", { name: /delete/i }));
      // The modal is up; nothing deleted until it is confirmed.
      expect(await screen.findByText(/can’t be undone/i)).toBeInTheDocument();
      expect(deleteVideo).not.toHaveBeenCalled();
    });

    it("deletes when the confirm dialog is confirmed", async () => {
      const onDeleted = vi.fn();
      render(<Player videoId="v1" onDeleted={onDeleted} />);
      await openMenu();
      fireEvent.click(await screen.findByRole("menuitem", { name: /delete/i }));
      // The dialog's confirm button (its label is "Delete", not "Delete…").
      const confirm = await screen.findByRole("button", { name: /^delete$/i });
      fireEvent.click(confirm);
      await waitFor(() => expect(deleteVideo).toHaveBeenCalledWith("v1"));
      await waitFor(() => expect(onDeleted).toHaveBeenCalled());
    });

    it("cancelling the confirm dialog does not delete", async () => {
      render(<Player videoId="v1" onDeleted={() => {}} />);
      await openMenu();
      fireEvent.click(await screen.findByRole("menuitem", { name: /delete/i }));
      fireEvent.click(await screen.findByRole("button", { name: /cancel/i }));
      await waitFor(() =>
        expect(screen.queryByText(/can’t be undone/i)).toBeNull(),
      );
      expect(deleteVideo).not.toHaveBeenCalled();
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

  it("shows No speech in this video for a no_transcript summary status", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({ summary_status: "no_transcript" }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);
    expect(
      await screen.findByText(/No speech in this video/i),
    ).toBeInTheDocument();
  });

  describe("Reprocess menu item", () => {
    it("shows on error status and calls reprocess", async () => {
      vi.mocked(getVideo).mockResolvedValue(
        makeVideo({ summary_status: "error", has_subtitles: true }),
      );
      render(<Player videoId="v1" onDeleted={() => {}} />);
      await openMenu();
      const item = await screen.findByRole("menuitem", {
        name: /Reprocess video/i,
      });
      fireEvent.click(item);
      await waitFor(() => expect(reprocess).toHaveBeenCalledWith("v1"));
    });

    // A finished summary can still be wrong or simply unwanted, so the redo
    // must not be gated on the job having failed.
    it("shows on a done summary too", async () => {
      vi.mocked(getVideo).mockResolvedValue(
        makeVideo({
          summary_status: "done",
          summary: "Prose one.",
          has_subtitles: true,
        }),
      );
      render(<Player videoId="v1" onDeleted={() => {}} />);
      expect(await screen.findByText("Prose one.")).toBeInTheDocument();
      await openMenu();
      expect(
        await screen.findByRole("menuitem", { name: /Reprocess video/i }),
      ).toBeInTheDocument();
    });

    it("shows on no_transcript, so a music video can be retried", async () => {
      vi.mocked(getVideo).mockResolvedValue(
        makeVideo({ summary_status: "no_transcript", has_subtitles: true }),
      );
      render(<Player videoId="v1" onDeleted={() => {}} />);
      await openMenu();
      expect(
        await screen.findByRole("menuitem", { name: /Reprocess video/i }),
      ).toBeInTheDocument();
    });

    it("is absent while a summary is already running", async () => {
      vi.mocked(getVideo).mockResolvedValue(
        makeVideo({ summary_status: "running", has_subtitles: true }),
      );
      render(<Player videoId="v1" onDeleted={() => {}} />);
      expect(await screen.findByText(/Summarizing/i)).toBeInTheDocument();
      await openMenu();
      expect(
        screen.queryByRole("menuitem", { name: /Reprocess video/i }),
      ).toBeNull();
    });

    // The endpoint answers 409 without subtitles, so the item would be dead.
    it("is absent for a video with no subtitles", async () => {
      vi.mocked(getVideo).mockResolvedValue(
        makeVideo({ summary_status: "error", has_subtitles: false }),
      );
      render(<Player videoId="v1" onDeleted={() => {}} />);
      expect(
        await screen.findByText(/Summarization failed/i),
      ).toBeInTheDocument();
      await openMenu();
      expect(
        screen.queryByRole("menuitem", { name: /Reprocess video/i }),
      ).toBeNull();
    });

    // A failed step marks the ⋮ trigger with an attention dot and flags the
    // Reprocess item, so a failure reads at a glance.
    it("marks the ⋮ trigger and flags the item when a step failed", async () => {
      vi.mocked(getVideo).mockResolvedValue(
        makeVideo({ summary_status: "error", has_subtitles: true }),
      );
      render(<Player videoId="v1" onDeleted={() => {}} />);
      await screen.findByRole("button", { name: /video actions/i });
      expect(document.querySelector(".kebab-dot")).not.toBeNull();
      await openMenu();
      const item = await screen.findByRole("menuitem", {
        name: /Reprocess video/i,
      });
      expect(item).toHaveTextContent(/failed/i);
    });

    it("leaves the ⋮ trigger unmarked when nothing failed", async () => {
      vi.mocked(getVideo).mockResolvedValue(
        makeVideo({ summary_status: "done", has_subtitles: true }),
      );
      render(<Player videoId="v1" onDeleted={() => {}} />);
      await screen.findByRole("button", { name: /video actions/i });
      expect(document.querySelector(".kebab-dot")).toBeNull();
    });
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

  // iPadOS 27 (public beta 1) Safari refuses to load the media at all when a
  // <track> child is present during resource selection — the player just sits
  // on the poster. See tubearchivist/tubearchivist#1196.
  it("mounts the subtitle track only after loadedmetadata", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_subtitles: true }));
    render(<Player videoId="v1" onDeleted={() => {}} />);
    await screen.findByRole("button", { name: /^Subtitles (on|off)$/ });
    expect(document.querySelector("video track")).toBeNull();

    fireEvent.loadedMetadata(
      document.querySelector("video") as HTMLVideoElement,
    );
    await waitFor(() =>
      expect(document.querySelector("video track")).not.toBeNull(),
    );
  });

  // Guard for the apply-the-default effect's subtitlesReadyFor dependency:
  // with the track deferred, the effect's first run finds no track, and
  // nothing else it depends on changes when the track later mounts. Unlike
  // the tests above, the stub only reports a track once one is really in the
  // DOM, so dropping that dependency makes this fail.
  it("applies the subtitles default once the deferred track mounts", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_subtitles: true }));
    vi.mocked(getSettings).mockResolvedValue(makeSettings(true));
    const fakeTrack = { mode: "disabled" } as unknown as TextTrack;
    const ttSpy = vi
      .spyOn(HTMLMediaElement.prototype, "textTracks", "get")
      .mockImplementation(
        () =>
          (document.querySelector("video track")
            ? [fakeTrack]
            : []) as unknown as TextTrackList,
      );
    try {
      render(<Player videoId="v1" onDeleted={() => {}} />);
      await screen.findByRole("button", { name: /^Subtitles (on|off)$/ });
      expect(fakeTrack.mode).toBe("disabled");

      fireEvent.loadedMetadata(
        document.querySelector("video") as HTMLVideoElement,
      );
      await waitFor(() => expect(fakeTrack.mode).toBe("showing"));
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

  it("shows Re-download in the ⋮ menu for an errored video and queues it", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({ id: "v1", status: "error" }),
    );
    vi.mocked(redownload).mockResolvedValue(undefined);
    render(<Player videoId="v1" onDeleted={() => {}} />);
    await openMenu();
    const item = await screen.findByRole("menuitem", { name: /re-download/i });
    fireEvent.click(item);
    await waitFor(() => expect(redownload).toHaveBeenCalledWith("v1"));
  });

  it("omits Re-download for a downloaded video", async () => {
    vi.mocked(getVideo).mockResolvedValue(
      makeVideo({ id: "v1", status: "downloaded" }),
    );
    render(<Player videoId="v1" onDeleted={() => {}} />);
    await openMenu(); // openMenu waits for the video to load
    expect(screen.queryByRole("menuitem", { name: /re-download/i })).toBeNull();
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
  it("writes a picked category and shows it at once", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ category: "gaming" }));
    render(<Player videoId="v1" onDeleted={() => {}} />);

    const pill = await screen.findByRole("button", {
      name: /Category: Gaming/,
    });
    fireEvent.click(pill);
    fireEvent.click(screen.getByRole("menuitemradio", { name: /^AI$/ }));

    expect(setCategory).toHaveBeenCalledWith("v1", "ai");
    await screen.findByRole("button", {
      name: /Category: AI/,
    });
  });

  // Optimistic, so a failed write must put the old category back rather than
  // leave the pill showing a choice the server never accepted.
  it("rolls the pill back when the category write fails", async () => {
    vi.mocked(getVideo).mockResolvedValue(makeVideo({ category: "gaming" }));
    vi.mocked(setCategory).mockRejectedValueOnce(new Error("nope"));
    render(<Player videoId="v1" onDeleted={() => {}} />);

    fireEvent.click(
      await screen.findByRole("button", { name: /Category: Gaming/ }),
    );
    fireEvent.click(screen.getByRole("menuitemradio", { name: /^AI$/ }));

    await screen.findByRole("button", { name: /Category: Gaming/ });
  });
});

// The stripping rules here mirror backend/internal/subtitles/vtt.go; a case
// added to one side belongs on the other.
describe("parseVtt sound-event stripping", () => {
  const vtt = (...cues: string[]) =>
    "WEBVTT\n\n" +
    cues
      .map((c, i) => `00:00:0${i}.000 --> 00:00:0${i + 1}.000\n${c}\n`)
      .join("\n");

  it("strips [Music] but keeps the words around it", () => {
    expect(parseVtt(vtt("[Music] I play games with"))).toEqual([
      { ts: 0, text: "I play games with" },
    ]);
  });

  it("drops a cue that was nothing but a marker", () => {
    expect(parseVtt(vtt("[Music]", "[Applause]", "real words"))).toEqual([
      { ts: 2, text: "real words" },
    ]);
  });

  it("strips music notes", () => {
    expect(parseVtt(vtt("♪ la la la ♪"))).toEqual([
      { ts: 0, text: "la la la" },
    ]);
  });

  it("strips only allow-listed parenthesised annotations", () => {
    expect(parseVtt(vtt("(applause) thanks"))).toEqual([
      { ts: 0, text: "thanks" },
    ]);
    // Real speech uses parentheses too — an open rule would eat this.
    expect(parseVtt(vtt("the result (roughly) doubled"))).toEqual([
      { ts: 0, text: "the result (roughly) doubled" },
    ]);
  });
});
