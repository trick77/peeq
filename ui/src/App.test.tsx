import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { render, screen, waitFor } from "@testing-library/react";
import { App } from "./App";
import type { Video } from "./api/types";

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
