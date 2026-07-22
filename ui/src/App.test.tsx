import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { App } from "./App";
import {
  downloadsStatus,
  resumeYoutube,
  listDownloads,
  getMe,
  listPending,
  cookieHealth,
  streamDownloads,
  listVideos,
} from "./api";
import { addDownload } from "./api/downloads";
import { searchVideos } from "./api/search";
import type { Job, User, Video } from "./api/types";

describe("App (static)", () => {
  it("renders peeq", () => {
    const html = renderToStaticMarkup(<App />);
    expect(html).toContain("peeq");
  });
});

// Reload-restore integration: the nowPlaying marker drives App's initial view.
// The barrel mock also covers the Library view's imports so the library path
// renders without crashing.
vi.mock("./api", () => ({
  getMe: vi.fn().mockResolvedValue({ id: "u1", email: "a@b.c" }),
  listDownloads: vi.fn().mockResolvedValue([]),
  cookieHealth: vi.fn().mockResolvedValue({ status: "active" }),
  downloadsStatus: vi
    .fn()
    .mockResolvedValue({ paused: false, low_disk: false }),
  resumeYoutube: vi.fn().mockResolvedValue(undefined),
  listPending: vi.fn().mockResolvedValue([]),
  streamDownloads: vi.fn().mockResolvedValue(undefined),
  listVideos: vi.fn().mockResolvedValue([]),
  getSettings: vi.fn().mockResolvedValue({}),
  setFavorite: vi.fn(),
  setWatched: vi.fn(),
}));

const mockVideo = vi.hoisted<Video>(() => ({
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
}));

vi.mock("./api/videos", () => ({
  getVideo: vi.fn().mockResolvedValue(mockVideo),
  setFavorite: vi.fn(),
  setWatched: vi.fn(),
  setResume: vi.fn().mockResolvedValue(42),
  deleteVideo: vi.fn(),
  redownload: vi.fn(),
  listVideos: vi.fn().mockResolvedValue([]),
  streamUrl: (id: string) => `/api/videos/${id}/stream`,
  thumbnailUrl: (id: string) => `/api/videos/${id}/thumbnail`,
}));

vi.mock("./api/search", () => ({
  subtitlesUrl: (id: string) => `/api/videos/${id}/subtitles`,
  resummarize: vi.fn(),
  searchVideos: vi.fn().mockResolvedValue([]),
}));

vi.mock("./api/downloads", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./api/downloads")>()),
  addDownload: vi.fn(),
}));

vi.mock("./api/channels", () => ({
  addChannel: vi.fn(),
  listChannels: vi.fn().mockResolvedValue([]),
  updateChannel: vi.fn(),
  subscribeChannel: vi.fn(),
  unsubscribeChannel: vi.fn(),
  deleteChannel: vi.fn(),
  getChannel: vi.fn(),
  scanChannel: vi.fn(),
  listAutoUnsubscribedChannels: vi.fn().mockResolvedValue([]),
  dismissDormantChannel: vi.fn(),
  resubscribeChannel: vi.fn(),
  channelAvatarUrl: (id: string) => `/api/channels/${id}/avatar`,
  channelBannerUrl: (id: string) => `/api/channels/${id}/banner`,
}));

vi.mock("./api/pending", () => ({
  listPending: vi.fn().mockResolvedValue([]),
  downloadPending: vi.fn(),
  ignorePending: vi.fn(),
}));

// Reset the URL before every test in this file. useRoute() derives the
// initial view from window.location.pathname, and jsdom persists location
// across tests in a file — without this, the deep-link tests below (which
// enter at /video/<id>, /bogus, …) would leak their path into later
// describes that assume they start at "/". A root-level hook applies to
// every describe regardless of textual position.
beforeEach(() => {
  window.history.replaceState(null, "", "/");
});

// The dock only starts its 3s poll once `jobs` already holds an active
// entry, so adding the first video to an empty dock used to leave it
// invisible until a manual reload: App passed Add a no-op onQueued, and
// the SSE progress handler can't help when the job is queued but not yet
// downloading. This asserts the add itself refreshes the queue.
describe("App dock bootstrap", () => {
  beforeEach(() => {
    // testing-library truncates its failure DOM dump at 7000 chars by
    // default, which cuts off the rail's lower nav groups and makes a
    // CI-only failure here impossible to diagnose from the log.
    process.env.DEBUG_PRINT_LIMIT = "100000";
    sessionStorage.clear();
    vi.clearAllMocks();
    // Restate every mock this test depends on rather than inheriting
    // whatever an earlier describe left behind — the paused-banner tests
    // above overwrite downloadsStatus, and clearAllMocks only drops call
    // history, not implementations.
    vi.mocked(getMe).mockResolvedValue({ id: "u1", email: "a@b.c" } as User);
    vi.mocked(downloadsStatus).mockResolvedValue({
      paused: false,
      low_disk: false,
      youtube_paused: false,
      youtube_pause_reason: "",
    });
    vi.mocked(listPending).mockResolvedValue([]);
    vi.mocked(cookieHealth).mockResolvedValue({
      status: "active",
      present: true,
    });
    vi.mocked(streamDownloads).mockResolvedValue(undefined);
  });

  it("refreshes the download queue after the Add view queues a video", async () => {
    vi.mocked(listDownloads).mockResolvedValue([]);
    vi.mocked(addDownload).mockResolvedValue({
      job_id: 7,
      video_id: "vynCRZwkWhE",
      title: "Queued video",
      channel_name: "Some channel",
      state: "pending",
      priority: 10,
    } as Job);

    render(<App />);

    // Wait for the authed shell before touching the rail: on a slow runner
    // the initial getMe() has not resolved yet, and a bare findByText would
    // race its 1s default timeout against an unrendered nav. The waits must
    // stay comfortably under this test's own timeout (last arg to it()),
    // or vitest aborts the test before the query can report what it saw.
    await screen.findByRole("button", { name: /Library/ }, { timeout: 8000 });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));

    // Starts empty — this is the state that used to be terminal.
    expect(await screen.findByText("Nothing queued")).toBeTruthy();

    // Once queued, the dock must reflect it without a reload.
    vi.mocked(listDownloads).mockResolvedValue([
      {
        job_id: 7,
        video_id: "vynCRZwkWhE",
        title: "Queued video",
        state: "pending",
        priority: 10,
      } as Job,
    ]);

    fireEvent.change(screen.getByLabelText("Video or channel URL"), {
      target: { value: "https://www.youtube.com/watch?v=vynCRZwkWhE&t=68s" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Download now/ }));

    expect(await screen.findByText("1 queued")).toBeTruthy();
  }, 20000);
});

// Deep links: the URL is the source of truth for which page is open (route.ts),
// replacing the old nowPlaying sessionStorage reload-restore. A cold-loaded
// /video/<id> reopens the Player; a rail click pushes the matching URL; a
// back/forward re-derives the view from the URL via popstate.
describe("App deep links", () => {
  beforeEach(() => {
    // testing-library truncates its failure DOM dump at 7000 chars, which
    // cuts off the rail's lower nav groups and makes a CI-only failure here
    // impossible to diagnose from the log.
    process.env.DEBUG_PRINT_LIMIT = "100000";
    sessionStorage.clear();
    vi.clearAllMocks();
    // Restate every mock this describe depends on rather than inheriting
    // whatever an earlier describe left behind — the paused-banner tests
    // overwrite downloadsStatus, and clearAllMocks only drops call history,
    // not implementations.
    vi.mocked(getMe).mockResolvedValue({ id: "u1", email: "a@b.c" } as User);
    vi.mocked(listDownloads).mockResolvedValue([]);
    vi.mocked(downloadsStatus).mockResolvedValue({
      paused: false,
      low_disk: false,
      youtube_paused: false,
      youtube_pause_reason: "",
    });
    vi.mocked(listPending).mockResolvedValue([]);
    vi.mocked(cookieHealth).mockResolvedValue({
      status: "active",
      present: true,
    });
    vi.mocked(streamDownloads).mockResolvedValue(undefined);
    // Default both video sources to empty so a test that overrides one does
    // not leak its fixture into the next (clearAllMocks keeps implementations).
    vi.mocked(listVideos).mockResolvedValue([]);
    vi.mocked(searchVideos).mockResolvedValue([]);
  });

  // Only the Player view renders a <video>; the top bar's search box only
  // shows on the library view (showSearch={view === "library"}). Use those
  // as unambiguous discriminators (both "Library" and "Now playing" always
  // appear as rail nav labels).
  it("a cold /video/<id> deep link opens the Player on that video", async () => {
    window.history.replaceState(null, "", "/video/v1");
    render(<App />);
    await waitFor(() => expect(document.querySelector("video")).not.toBeNull());
    // A valid path is preserved verbatim — not rewritten by normalization.
    expect(window.location.pathname).toBe("/video/v1");
  });

  it("a cold / lands on the Library", async () => {
    render(<App />);
    await screen.findByPlaceholderText("Search titles");
    expect(document.querySelector("video")).toBeNull();
  });

  it("clicking a Library video opens the Player and pushes /video/<id>", async () => {
    vi.mocked(listVideos).mockResolvedValue([mockVideo]);
    render(<App />);

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Open The Trillion Dollar Equation",
      }),
    );

    await waitFor(() => expect(document.querySelector("video")).not.toBeNull());
    expect(window.location.pathname).toBe("/video/v1");
  });

  it("opening a search match pushes /video/<id> (openVideoAt)", async () => {
    vi.mocked(searchVideos).mockResolvedValue([
      {
        video: mockVideo,
        matches: [
          {
            start_seconds: 90,
            snippet: "matched moment here",
            distance: 0.1,
            kind: "transcript",
          },
        ],
      },
    ]);
    render(<App />);
    await screen.findByPlaceholderText("Search titles");

    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    const input = await screen.findByPlaceholderText(
      "Search everything you've watched…",
    );
    fireEvent.change(input, { target: { value: "equation" } });
    fireEvent.submit(input.closest("form")!);

    // Click the match snippet; the click bubbles to the match button's onOpen.
    fireEvent.click(await screen.findByText("matched moment here"));

    await waitFor(() => expect(document.querySelector("video")).not.toBeNull());
    expect(window.location.pathname).toBe("/video/v1");
  });

  it("normalizes an unknown path to / and shows the Library", async () => {
    window.history.replaceState(null, "", "/bogus");
    render(<App />);
    await screen.findByPlaceholderText("Search titles");
    // useRoute's mount normalization replaceState's the bogus entry to the
    // canonical path so the address bar matches what is shown.
    expect(window.location.pathname).toBe("/");
  });

  it("clicking a rail item pushes its URL", async () => {
    render(<App />);
    await screen.findByPlaceholderText("Search titles");

    fireEvent.click(screen.getByRole("button", { name: "Pending" }));

    expect(await screen.findByText("Nothing pending.")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/pending");
  });

  it("re-derives the view from the URL on back/forward (popstate)", async () => {
    render(<App />);
    await screen.findByPlaceholderText("Search titles");

    // Navigate into Pending — pushes /pending.
    fireEvent.click(screen.getByRole("button", { name: "Pending" }));
    await screen.findByText("Nothing pending.");
    expect(window.location.pathname).toBe("/pending");

    // Simulate the browser Back button: the URL returns to the previous entry
    // and a popstate fires. jsdom's real history.back() is async and quirky,
    // so drive the listener directly — this is the exact contract useRoute's
    // popstate handler implements (re-parse window.location). Real
    // back/forward is covered by browser verification.
    window.history.replaceState(null, "", "/");
    window.dispatchEvent(new Event("popstate"));

    await screen.findByPlaceholderText("Search titles");
    expect(document.querySelector("video")).toBeNull();
  });
});

// Task 13: global YouTube-paused banner. youtube_paused takes precedence
// over the healthy default and offers a one-click Resume that calls the
// kill-switch endpoint and refetches status.
describe("App youtube-paused banner", () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  it("shows the YouTube-paused banner with a Resume action", async () => {
    vi.mocked(downloadsStatus).mockResolvedValue({
      paused: false,
      low_disk: false,
      youtube_paused: true,
      youtube_pause_reason: "",
    });
    vi.mocked(resumeYoutube).mockResolvedValue(undefined);
    render(<App />);
    const resume = await screen.findByRole("button", { name: /resume/i });
    fireEvent.click(resume);
    await waitFor(() => expect(resumeYoutube).toHaveBeenCalled());
  });
});

// Task 11: the channel page is a detail destination reached only by clicking
// a channel name (like the player is reached from a video card), never a
// rail item — this locks that in as a regression guard.
describe("App routing", () => {
  it("channel is not in the nav rail", async () => {
    render(<App />);
    await screen.findByPlaceholderText("Search titles");
    expect(screen.queryByRole("button", { name: /^channel$/i })).toBeNull();
  });

  // Task 11/15: the channel page has no rail entry — it's reached only by
  // clicking a channel-name link. This drives the real flow (Channels list
  // -> click a channel name -> Channel page -> "All channels" back button)
  // to exercise App's openChannel/ViewSwitch wiring end to end, rather than
  // unit-testing those functions directly.
  it("clicking a channel name from Channels opens the Channel page, and the back button returns", async () => {
    const { listChannels, getChannel } = await import("./api/channels");
    vi.mocked(listChannels).mockReset();
    vi.mocked(listChannels).mockResolvedValue([
      {
        id: "UCa",
        name: "Uncanny Expeditions",
        handle: "@UncannyExpeditions",
        subscribed: true,
        autodownload: false,
        format_override: "",
        pending_count: 0,
        downloaded_count: 3,
        dormant: false,
      },
    ]);
    vi.mocked(getChannel).mockReset();
    vi.mocked(getChannel).mockResolvedValue({
      id: "UCa",
      name: "Uncanny Expeditions",
      handle: "@UncannyExpeditions",
      description: "",
      has_avatar: false,
      has_banner: false,
      verified: false,
      resolve_ok: true,
      gone: false,
      tracked: true,
      tracked_at: "2026-03-14 09:00:00",
      archived_count: 3,
      runtime_seconds: 600,
      disk_bytes: 1024,
      newest_published_at: "2026-07-18T00:00:00Z",
      subscribed: true,
      autodownload: false,
      format_override: "",
      last_scanned_at: "2026-07-20 08:00:00",
      next_scan_at: "2026-07-20 14:00:00",
      pending_count: 0,
    });

    render(<App />);
    await screen.findByPlaceholderText("Search titles");

    fireEvent.click(screen.getByRole("button", { name: "Channels" }));
    const channelLink = await screen.findByRole("button", {
      name: "Uncanny Expeditions",
    });
    fireEvent.click(channelLink);

    expect(
      await screen.findByRole("button", { name: /all channels/i }),
    ).toBeInTheDocument();
    await waitFor(() => expect(getChannel).toHaveBeenCalledWith("UCa"));

    fireEvent.click(screen.getByRole("button", { name: /all channels/i }));

    await waitFor(() => {
      expect(
        screen.queryByRole("button", { name: /all channels/i }),
      ).toBeNull();
    });
  });

  it("the Pending nav item renders the Pending view", async () => {
    render(<App />);
    await screen.findByPlaceholderText("Search titles");

    fireEvent.click(screen.getByRole("button", { name: "Pending" }));

    expect(await screen.findByText("Nothing pending.")).toBeInTheDocument();
  });
});
