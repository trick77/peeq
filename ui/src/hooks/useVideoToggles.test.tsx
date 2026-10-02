import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useState } from "react";
import { setFavorite, setWatched } from "../api";
import type { LibraryVideo } from "../api/types";
import { useVideoToggles } from "./useVideoToggles";

vi.mock("../api", () => ({
  setFavorite: vi.fn(),
  setWatched: vi.fn(),
}));

const video = (over: Partial<LibraryVideo> = {}): LibraryVideo =>
  ({
    id: "v1",
    title: "T",
    favorite: false,
    watched: false,
    resume_position_seconds: 42,
    ...over,
  }) as LibraryVideo;

function setup(onError = vi.fn(), onSettled = vi.fn()) {
  return renderHook(() => {
    const [videos, setVideos] = useState<LibraryVideo[]>([video()]);
    return {
      videos,
      ...useVideoToggles(videos, setVideos, { onError, onSettled }),
    };
  });
}

describe("useVideoToggles", () => {
  beforeEach(() => {
    vi.mocked(setFavorite).mockReset().mockResolvedValue(undefined as never);
    vi.mocked(setWatched).mockReset().mockResolvedValue(undefined as never);
  });

  it("keeps both callbacks' identity while the list changes", async () => {
    // A memoised card re-renders when a prop changes. Handlers made fresh per
    // render would make that every card, on every toggle.
    const { result } = setup();
    const { toggleFavorite, toggleWatched } = result.current;
    act(() => result.current.toggleFavorite("v1"));
    await waitFor(() => expect(result.current.videos[0].favorite).toBe(true));
    expect(result.current.toggleFavorite).toBe(toggleFavorite);
    expect(result.current.toggleWatched).toBe(toggleWatched);
  });

  it("marks watched at once, zeroes the resume position, and reports it settled", async () => {
    const onSettled = vi.fn();
    const { result } = setup(vi.fn(), onSettled);
    act(() => result.current.toggleWatched("v1"));
    expect(result.current.videos[0]).toMatchObject({
      watched: true,
      resume_position_seconds: 0,
    });
    await waitFor(() => expect(onSettled).toHaveBeenCalledTimes(1));
    expect(setWatched).toHaveBeenCalledWith("v1", true);
  });

  it("puts the card back and reports the failure", async () => {
    vi.mocked(setWatched).mockRejectedValue(new Error("nope"));
    const onError = vi.fn();
    const { result } = setup(onError);
    act(() => result.current.toggleWatched("v1"));
    await waitFor(() => expect(onError).toHaveBeenCalledWith("nope"));
    expect(result.current.videos[0]).toMatchObject({
      watched: false,
      resume_position_seconds: 42,
    });
  });

  it("ignores an id that is not in the list", () => {
    const { result } = setup();
    act(() => result.current.toggleFavorite("missing"));
    expect(setFavorite).not.toHaveBeenCalled();
  });
});
