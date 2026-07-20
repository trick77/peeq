import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";
import { App } from "./App";
import { downloadsStatus, resumeYoutube, listDownloads, getMe, listPending, cookieHealth, streamDownloads } from "./api";
import { addDownload } from "./api/downloads";
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
  downloadsStatus: vi.fn().mockResolvedValue({ paused: false, low_disk: false }),
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
  streamUrl: (id: string) => `/api/videos/${id}/stream`,
  thumbnailUrl: (id: string) => `/api/videos/${id}/thumbnail`,
}));

vi.mock("./api/search", () => ({
  subtitlesUrl: (id: string) => `/api/videos/${id}/subtitles`,
  resummarize: vi.fn(),
}));

vi.mock("./api/downloads", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./api/downloads")>()),
  addDownload: vi.fn(),
}));

vi.mock("./api/channels", () => ({
  addChannel: vi.fn(),
  listChannels: vi.fn().mockResolvedValue([]),
}));

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
    vi.mocked(cookieHealth).mockResolvedValue({ status: "active", present: true });
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
    fireEvent.click(screen.getByRole("button", { name: /Add a video/ }));

    // Starts empty — this is the state that used to be terminal.
    expect(await screen.findByText("Nothing queued")).toBeTruthy();

    // Once queued, the dock must reflect it without a reload.
    vi.mocked(listDownloads).mockResolvedValue([
      { job_id: 7, video_id: "vynCRZwkWhE", title: "Queued video", state: "pending", priority: 10 } as Job,
    ]);

    fireEvent.change(screen.getByLabelText("Video URL"), {
      target: { value: "https://www.youtube.com/watch?v=vynCRZwkWhE&t=68s" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Download now/ }));

    expect(await screen.findByText("1 queued")).toBeTruthy();
  }, 20000);
});

describe("App reload-restore", () => {
  beforeEach(() => {
    sessionStorage.clear();
  });

  // Only the Player view renders a <video>; the library search box only
  // appears on the library view. Use those as unambiguous discriminators
  // (both "Library" and "Now playing" always appear as rail nav labels).
  it("reopens the Player when a video was playing before reload", async () => {
    sessionStorage.setItem("peeq.nowPlaying", JSON.stringify({ videoId: "v1", playing: true }));
    render(<App />);
    await waitFor(() => expect(document.querySelector("video")).not.toBeNull());
  });

  it("lands on Library when the marker says the video was paused", async () => {
    sessionStorage.setItem("peeq.nowPlaying", JSON.stringify({ videoId: "v1", playing: false }));
    render(<App />);
    await screen.findByPlaceholderText(/Search titles/i);
    expect(document.querySelector("video")).toBeNull();
  });

  it("lands on Library when there is no marker", async () => {
    render(<App />);
    await screen.findByPlaceholderText(/Search titles/i);
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
    vi.mocked(downloadsStatus).mockResolvedValue({ paused: false, low_disk: false, youtube_paused: true, youtube_pause_reason: "" });
    vi.mocked(resumeYoutube).mockResolvedValue(undefined);
    render(<App />);
    const resume = await screen.findByRole("button", { name: /resume/i });
    fireEvent.click(resume);
    await waitFor(() => expect(resumeYoutube).toHaveBeenCalled());
  });
});
