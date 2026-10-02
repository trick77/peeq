import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import type { LibraryVideo } from "../api/types";

// Counts how often each card's poster renders, which is how often the card
// did: ThumbFill is rendered once per VideoCard render.
const posterRenders = vi.hoisted(() => ({ count: 0 }));
vi.mock("../components/ThumbFill", () => ({
  ThumbFill: () => {
    posterRenders.count += 1;
    return null;
  },
}));

vi.mock("../api", () => ({
  listVideos: vi.fn(),
  getVideoCounts: vi.fn(),
  getSettings: vi.fn().mockResolvedValue({ retention_days: 14 }),
  setFavorite: vi.fn(),
  setWatched: vi.fn(),
  redownload: vi.fn(),
}));

import { Library } from "./Library";
import { listVideos, getVideoCounts } from "../api";

const card = (id: string): LibraryVideo => ({
  id,
  title: `Video ${id}`,
  channel_id: "c1",
  channel_name: "Channel",
  has_thumbnail: false,
  has_media: true,
  status: "downloaded",
  watched: false,
  resume_position_seconds: 0,
  favorite: false,
  category: "uncategorized",
});

describe("Library re-renders", () => {
  beforeEach(() => {
    posterRenders.count = 0;
    vi.mocked(listVideos).mockResolvedValue([card("a"), card("b"), card("c")]);
    vi.mocked(getVideoCounts).mockResolvedValue({
      filters: {
        all: 3,
        unwatched: 3,
        in_progress: 0,
        watched: 0,
        favorites: 0,
      },
      categories: {
        all: {},
        unwatched: {},
        in_progress: {},
        watched: {},
        favorites: {},
      },
    });
  });

  it("does not re-render its cards when the shell re-renders it", async () => {
    // App re-renders on every queue poll and hands down freshly made
    // callbacks each time. With hundreds of cards on screen, each of those
    // used to re-run every card.
    const { rerender } = render(
      <Library
        onOpenVideo={() => {}}
        onOpenChannel={() => {}}
        search=""
        onSearchChange={() => {}}
        queueSignal=""
      />,
    );
    await screen.findByText("Video a");
    const settled = posterRenders.count;
    expect(settled).toBeGreaterThanOrEqual(3);

    for (let i = 0; i < 5; i += 1) {
      rerender(
        <Library
          onOpenVideo={() => {}}
          onOpenChannel={() => {}}
          search=""
          onSearchChange={() => {}}
          queueSignal=""
        />,
      );
    }
    expect(posterRenders.count).toBe(settled);
  });
});
