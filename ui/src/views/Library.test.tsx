import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { VideoCard } from "../components/VideoCard";
import type { Video } from "../api/types";

// baseVideo is a minimal, valid Video row; each test overrides only the
// fields that drive the lifecycle line being asserted.
function baseVideo(overrides: Partial<Video> = {}): Video {
  return {
    id: "v1",
    url: "https://youtu.be/v1",
    title: "A Test Video",
    channel_id: "chan1",
    channel_name: "Test Channel",
    duration_seconds: 754,
    has_thumbnail: false,
    has_media: true,
    availability: "available",
    status: "downloaded",
    watched: false,
    resume_position_seconds: 0,
    favorite: false,
    summary: "",
    chapters: [],
    key_points: [],
    summary_status: "",
    audio_language: "",
    has_subtitles: false,
    category: "uncategorized",
    ...overrides,
  };
}

const noop = () => {};

describe("VideoCard lifecycle line", () => {
  it('renders "Kept forever" for a favorite video', () => {
    render(
      <VideoCard
        video={baseVideo({ favorite: true, watched: true })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    expect(screen.getByText("Kept forever")).toBeInTheDocument();
  });

  it('renders "Expires in N days" for a watched, non-favorite video', () => {
    const fiveDaysAgo = new Date(Date.now() - 5 * 24 * 60 * 60 * 1000).toISOString();
    render(
      <VideoCard
        video={baseVideo({ watched: true, watched_at: fiveDaysAgo, favorite: false })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    // 14 - 5 = 9 days left.
    expect(screen.getByText(/Expires in 9 days/)).toBeInTheDocument();
  });

  it("renders the progress ring for a downloading video", () => {
    render(
      <VideoCard
        video={baseVideo({ status: "downloading", has_media: false })}
        retentionDays={14}
        progress={{ percent: 42, eta: "1m12s" }}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    const ring = document.querySelector(".ring");
    expect(ring).not.toBeNull();
    expect(ring?.getAttribute("data-p")).toBe("42%");
    // Busy states are a spinner plus a plain label — never an ellipsis.
    expect(screen.getByText("Downloading")).toBeInTheDocument();
  });

  it('renders "Not watched yet" for a fresh video', () => {
    render(
      <VideoCard
        video={baseVideo()}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    expect(screen.getByText("Not watched yet")).toBeInTheDocument();
  });

  it("calls onOpen with the video id when the thumbnail is clicked", async () => {
    const onOpen = vi.fn();
    render(
      <VideoCard video={baseVideo()} retentionDays={14} onOpen={onOpen} onToggleFavorite={noop} onToggleWatched={noop} />,
    );
    screen.getByRole("button", { name: /Open A Test Video/ }).click();
    expect(onOpen).toHaveBeenCalledWith("v1");
  });

  it('shows "Download failed" + a Re-download button for an errored video and calls onRedownload', () => {
    const onRedownload = vi.fn();
    render(
      <VideoCard
        video={baseVideo({ id: "v1", status: "error" })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
        onRedownload={onRedownload}
      />,
    );
    expect(screen.getByText("Download failed")).toBeInTheDocument();
    const btn = screen.getByRole("button", { name: /re-download/i });
    fireEvent.click(btn);
    expect(onRedownload).toHaveBeenCalledWith("v1");
  });

  it('shows "Removed to save space · summary kept" for a tombstoned video', () => {
    render(
      <VideoCard
        video={baseVideo({ id: "v1", status: "tombstoned" })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
        onRedownload={noop}
      />,
    );
    expect(screen.getByText("Removed to save space · summary kept")).toBeInTheDocument();
  });

  it("shows a category badge when categorized, hides it when uncategorized", () => {
    const base = baseVideo({ category: "ai" });
    const { rerender } = render(
      <VideoCard
        video={base}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    expect(screen.getByText("AI")).toBeInTheDocument();
    rerender(
      <VideoCard
        video={baseVideo({ category: "uncategorized" })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    expect(screen.queryByText("Uncategorized")).not.toBeInTheDocument();
  });

  it("renders no category pill for an unknown category id", () => {
    // Given: a video carrying a category the UI has never heard of — e.g.
    // written by a newer backend enum than this build's mirror.
    render(
      <VideoCard
        video={baseVideo({ category: "not-a-real-category" })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );

    // Then: the card still renders, and the raw id never leaks into the UI.
    expect(screen.getByText("A Test Video")).toBeInTheDocument();
    expect(screen.queryByText("not-a-real-category")).not.toBeInTheDocument();
  });
});

// Library view: category chip row + filtering. Mocks the "../api" barrel
// the same way App.test.tsx does, since Library.tsx imports from it
// directly (listVideos, getSettings, listDownloads, streamDownloads,
// setFavorite, setWatched, redownload).
vi.mock("../api", () => ({
  listVideos: vi.fn(),
  getSettings: vi.fn().mockResolvedValue({ retention_days: 14 }),
  listDownloads: vi.fn().mockResolvedValue([]),
  streamDownloads: vi.fn().mockImplementation(() => new Promise(() => {})),
  setFavorite: vi.fn(),
  setWatched: vi.fn(),
  redownload: vi.fn(),
}));

import { Library } from "./Library";
import { listVideos, listDownloads } from "../api";

function categoryVideo(overrides: Partial<Video> = {}): Video {
  return {
    id: "v1",
    url: "https://youtu.be/v1",
    title: "A Test Video",
    channel_id: "chan1",
    channel_name: "Test Channel",
    duration_seconds: 754,
    has_thumbnail: false,
    has_media: true,
    availability: "available",
    status: "downloaded",
    watched: false,
    resume_position_seconds: 0,
    favorite: false,
    summary: "",
    chapters: [],
    key_points: [],
    summary_status: "",
    audio_language: "",
    has_subtitles: false,
    category: "uncategorized",
    ...overrides,
  };
}

describe("Library category chips", () => {
  beforeEach(() => {
    vi.mocked(listVideos).mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders a category chip row and filters by category", async () => {
    const aiVideo = categoryVideo({ id: "v1", title: "ai video title", category: "ai" });
    const newsVideo = categoryVideo({ id: "v2", title: "news video title", category: "news" });

    vi.mocked(listVideos).mockImplementation(async (opts) => {
      if (opts.category === "ai") return [aiVideo];
      return [aiVideo, newsVideo];
    });

    render(<Library onOpenVideo={() => {}} />);

    expect(await screen.findByText("news video title")).toBeInTheDocument();

    const aiChip = await screen.findByRole("button", { name: /AI/ });
    fireEvent.click(aiChip);

    await waitFor(() => {
      expect(screen.queryByText(/news video title/i)).not.toBeInTheDocument();
    });
    expect(screen.getByText("ai video title")).toBeInTheDocument();
  });

  it("applies the category filter independently of the status chip", async () => {
    const aiVideo = categoryVideo({ id: "v1", title: "ai video title", category: "ai" });
    const newsVideo = categoryVideo({ id: "v2", title: "news video title", category: "news" });

    vi.mocked(listVideos).mockImplementation(async (opts) => {
      if (opts.category === "ai") return [aiVideo];
      return [aiVideo, newsVideo];
    });

    render(<Library onOpenVideo={() => {}} />);
    await screen.findByText("news video title");

    fireEvent.click(screen.getByRole("button", { name: /Unwatched/ }));
    const aiChip = await screen.findByRole("button", { name: /AI/ });
    fireEvent.click(aiChip);

    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(expect.objectContaining({ filter: "unwatched", category: "ai" }));
    });
    // Selecting a category must not reset the status chip, and vice versa.
    expect(screen.getByRole("button", { name: /Unwatched/ })).toHaveClass("on");
    expect(aiChip).toHaveClass("on");
  });

  it("keeps the selected category when the 3s poller refreshes", async () => {
    // Given: a category filter is active. The 3s poller (Library.tsx:163)
    // only arms while a download job is pending/running, so listDownloads
    // must report one to make the interval fire at all.
    const aiVideo = categoryVideo({ id: "v1", title: "ai video title", category: "ai" });
    vi.mocked(listVideos).mockResolvedValue([aiVideo]);
    vi.mocked(listDownloads).mockResolvedValue([
      {
        job_id: 1,
        video_id: "v1",
        state: "running",
        priority: 0,
        attempts: 0,
      },
    ]);
    vi.useFakeTimers({ shouldAdvanceTime: true });

    render(<Library onOpenVideo={() => {}} />);
    const aiChip = await screen.findByRole("button", { name: /AI/ });
    fireEvent.click(aiChip);
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(expect.objectContaining({ filter: "all", category: "ai" }));
    });
    vi.mocked(listVideos).mockClear();

    // When: the 3s poller fires.
    await vi.advanceTimersByTimeAsync(3000);

    // Then: the refresh still carries the category, not just the status.
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(expect.objectContaining({ filter: "all", category: "ai" }));
    });
  });

  it("typing in the search box refetches with the query", async () => {
    vi.mocked(listVideos).mockResolvedValue([]);
    const user = userEvent.setup();
    render(<Library onOpenVideo={() => {}} />);
    await screen.findByPlaceholderText(/search/i);

    await user.type(screen.getByPlaceholderText(/search/i), "abyss");

    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(expect.objectContaining({ q: "abyss" }));
    });
  });

  it("choosing a sort option refetches with that sort", async () => {
    vi.mocked(listVideos).mockResolvedValue([]);
    const user = userEvent.setup();
    render(<Library onOpenVideo={() => {}} />);
    await screen.findByLabelText(/sort/i);

    await user.selectOptions(screen.getByLabelText(/sort/i), "longest");

    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(expect.objectContaining({ sort: "longest" }));
    });
  });
});
