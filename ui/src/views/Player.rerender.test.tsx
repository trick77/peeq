import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, waitFor, fireEvent } from "@testing-library/react";
import { Player } from "./Player";
import { resetSettingsStoreForTests } from "../settingsStore";

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
  state_version: 1,
  favorite: false,
  summary: "",
  chapters: [],
  key_points: [],
  summary_status: "pending",
  indexed: true,
  audio_language: "",
  has_subtitles: false,
  category: "uncategorized",
};

vi.mock("../api/videos", () => ({
  getVideo: vi.fn(),
  setFavorite: vi.fn().mockResolvedValue(true),
  setWatched: vi.fn().mockResolvedValue(true),
  setCategory: vi.fn().mockResolvedValue("ai"),
  setResume: vi
    .fn()
    .mockResolvedValue({ position: 42, state_version: 1, watched: false }),
  deleteVideo: vi.fn().mockResolvedValue(undefined),
  redownload: vi.fn().mockResolvedValue(undefined),
  streamUrl: (id: string) => `/api/videos/${id}/stream`,
  thumbnailUrl: (id: string) => `/api/videos/${id}/thumbnail`,
  // Needed by UnfetchedVideo, which Player renders for a 'new' video.
  pendingThumbnailUrl: (id: string) => `/api/pending/${id}/thumbnail`,
  createPlaybackGrant: vi.fn(),
  // Resolved by default so the Search index card's side load is harmless in
  // every test that is not about it — an unresolved mock would leave the
  // effect's promise dangling in all ~100 of them.
  getVideoEmbeddings: vi.fn().mockResolvedValue({
    model: "text-embedding-3-small",
    dimensions: 1536,
    chunks: 12,
    tokens: 4200,
    kinds: [{ kind: "transcript", count: 12, tokens: 4200 }],
  }),
}));

vi.mock("../api/playback", () => ({
  setPlaybackState: vi.fn().mockResolvedValue({ video_id: "v1" }),
}));

vi.mock("../api/search", () => ({
  subtitlesUrl: (id: string) => `/api/videos/${id}/subtitles`,
  reprocess: vi.fn().mockResolvedValue(undefined),
}));

// The Player no longer subscribes to the SSE feed at all: App owns the one
// stream and hands summary events down as a prop. The module is still mocked
// so that importing it cannot reach the network from a test.
vi.mock("../api/downloads", () => ({
  streamDownloads: vi.fn(),
}));

// Player reads the global subtitles preference on mount, so this mock is
// needed by EVERY test in this file, not just the subtitles ones — an
// unmocked getSettings would reject on mount everywhere.
vi.mock("../api/settings", () => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
}));

// The Player side-loads share status on mount; without this mock getShareStatus
// would hit an unmocked fetch. It resolves "not shared" so the Share button and
// chip render in their default state.
vi.mock("../api/share", () => ({
  getShareStatus: vi.fn().mockResolvedValue({ shared: false }),
}));

import { getVideo } from "../api/videos";
import { getSettings } from "../api/settings";
import type { Settings, Video } from "../api/types";
import { resetVideoHostForTests } from "../videoHost";

function makeVideo(overrides: Partial<Video> = {}): Video {
  return { ...mockVideo, ...overrides };
}

// Player reads subtitles_default and direct_stream_enabled off Settings, but
// the mock returns a whole (cast) object so the shape stays honest. Direct
// playback defaults to off, matching the server default.
function makeSettings(
  subtitlesDefault: boolean,
  directStream = false,
): Settings {
  return {
    subtitles_default: subtitlesDefault,
    direct_stream_enabled: directStream,
  } as Settings;
}

// The five cards below the stage are swapped for memoised render counters:
// what the first test checks is that the props the Player hands them stay
// referentially stable across a playing video's timeupdates (about four a
// second), which is the half of "the cards do not re-render" that lives in
// Player.tsx. That the real cards are memoised is the second test.
const renders = vi.hoisted(() => ({
  transcript: 0,
  contents: 0,
  summary: 0,
  highlights: 0,
  details: 0,
}));
vi.mock("../components/TranscriptCard", async () => {
  const { memo } = await import("react");
  return {
    TranscriptCard: memo(() => {
      renders.transcript += 1;
      return <div data-testid="transcript-stub" />;
    }),
  };
});
vi.mock("./player/ContentsCard", async () => {
  const { memo } = await import("react");
  return {
    ContentsCard: memo(() => {
      renders.contents += 1;
      return null;
    }),
  };
});
vi.mock("./player/SidebarPanels", async () => {
  const { memo } = await import("react");
  return {
    SummaryCard: memo(() => {
      renders.summary += 1;
      return null;
    }),
    HighlightsCard: memo(() => {
      renders.highlights += 1;
      return null;
    }),
  };
});
vi.mock("./player/DetailsCard", async () => {
  const { memo } = await import("react");
  return {
    DetailsCard: memo(() => {
      renders.details += 1;
      return null;
    }),
  };
});

beforeEach(() => {
  resetSettingsStoreForTests();
  resetVideoHostForTests();
  for (const k of Object.keys(renders) as (keyof typeof renders)[]) {
    renders[k] = 0;
  }
  vi.mocked(getVideo).mockReset();
  vi.mocked(getVideo).mockResolvedValue(makeVideo({ has_subtitles: true }));
  vi.mocked(getSettings).mockReset();
  vi.mocked(getSettings).mockResolvedValue(makeSettings(false));
});

describe("Player render isolation", () => {
  it("does not re-render the cards on timeupdate", async () => {
    render(<Player videoId="v1" onDeleted={() => {}} visible />);
    const videoEl = await waitFor(() => {
      const el = document.querySelector("video");
      if (!el) throw new Error("video element not mounted yet");
      return el;
    });
    // Every mount-time load has landed once each card is up and the settings
    // read has answered: from here a render can only come from playback.
    await waitFor(() => expect(renders.transcript).toBeGreaterThan(0));
    await waitFor(() => expect(renders.details).toBeGreaterThan(0));
    await waitFor(() => expect(getSettings).toHaveBeenCalled());
    const before = { ...renders };

    for (let i = 1; i <= 8; i += 1) {
      Object.defineProperty(videoEl, "currentTime", {
        value: i * 3,
        writable: true,
        configurable: true,
      });
      fireEvent.timeUpdate(videoEl);
    }
    expect(renders).toEqual(before);
  });

  it("exports the cards as memoised components", async () => {
    const memoType = Symbol.for("react.memo");
    const [transcript, contents, panels, details] = await Promise.all([
      vi.importActual<typeof import("../components/TranscriptCard")>(
        "../components/TranscriptCard",
      ),
      vi.importActual<typeof import("./player/ContentsCard")>(
        "./player/ContentsCard",
      ),
      vi.importActual<typeof import("./player/SidebarPanels")>(
        "./player/SidebarPanels",
      ),
      vi.importActual<typeof import("./player/DetailsCard")>(
        "./player/DetailsCard",
      ),
    ]);
    for (const card of [
      transcript.TranscriptCard,
      contents.ContentsCard,
      panels.SummaryCard,
      panels.HighlightsCard,
      details.DetailsCard,
    ]) {
      expect((card as unknown as { $$typeof: symbol }).$$typeof).toBe(memoType);
    }
  });
});
