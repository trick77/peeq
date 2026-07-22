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
    const fiveDaysAgo = new Date(
      Date.now() - 5 * 24 * 60 * 60 * 1000,
    ).toISOString();
    render(
      <VideoCard
        video={baseVideo({
          watched: true,
          watched_at: fiveDaysAgo,
          favorite: false,
        })}
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

  it("renders the category pill on the lifecycle line of a fresh video", () => {
    render(
      <VideoCard
        video={baseVideo({ category: "ai" })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    // The pill sits in the lifecycle row, not on the channel/date line.
    const pill = document.querySelector(".life.fresh .metapill");
    expect(pill).not.toBeNull();
    expect(pill).toHaveTextContent("Artificial Intelligence");
    expect(document.querySelector(".by .metapill")).toBeNull();
  });

  it("renders no lifecycle row at all for a fresh uncategorized video", () => {
    render(
      <VideoCard
        video={baseVideo()}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    // An empty .life would still eat a 10px `.card` flex gap.
    expect(document.querySelector(".life")).toBeNull();
  });

  it("calls onOpen with the video id when the thumbnail is clicked", async () => {
    const onOpen = vi.fn();
    render(
      <VideoCard
        video={baseVideo()}
        retentionDays={14}
        onOpen={onOpen}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
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
    expect(
      screen.getByText("Removed to save space · summary kept"),
    ).toBeInTheDocument();
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
    expect(screen.getByText("Artificial Intelligence")).toBeInTheDocument();
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

  it("renders the channel name as a clickable link when onOpenChannel is provided", () => {
    const onOpenChannel = vi.fn();
    render(
      <VideoCard
        video={baseVideo({ channel_id: "chan1", channel_name: "Test Channel" })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
        onOpenChannel={onOpenChannel}
      />,
    );
    const link = screen.getByRole("button", { name: "Test Channel" });
    fireEvent.click(link);
    expect(onOpenChannel).toHaveBeenCalledWith("chan1");
  });

  it("renders the channel name as plain text (not a button) when onOpenChannel is absent", () => {
    render(
      <VideoCard
        video={baseVideo({ channel_id: "chan1", channel_name: "Test Channel" })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    expect(screen.getByText("Test Channel")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Test Channel" }),
    ).not.toBeInTheDocument();
  });

  it("opens the video when the title is clicked", () => {
    const onOpen = vi.fn();
    render(
      <VideoCard
        video={baseVideo()}
        retentionDays={14}
        onOpen={onOpen}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    // Exact name, not a regex: the thumbnail button is named "Open A Test
    // Video" and a loose match would hit that one instead.
    fireEvent.click(screen.getByRole("button", { name: "A Test Video" }));
    expect(onOpen).toHaveBeenCalledWith("v1");
  });

  it("does not show the file size", () => {
    render(
      <VideoCard
        video={baseVideo({ filesize_bytes: 1024 ** 3 })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    expect(screen.queryByText(/GB|MB|KB/)).not.toBeInTheDocument();
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
import { listVideos, listDownloads, setFavorite, setWatched } from "../api";

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
    const aiVideo = categoryVideo({
      id: "v1",
      title: "ai video title",
      category: "ai",
    });
    const newsVideo = categoryVideo({
      id: "v2",
      title: "news video title",
      category: "news",
    });

    vi.mocked(listVideos).mockImplementation(async (opts) => {
      if (opts?.category === "ai") return [aiVideo];
      return [aiVideo, newsVideo];
    });

    render(<Library onOpenVideo={() => {}} search="" />);

    expect(await screen.findByText("news video title")).toBeInTheDocument();

    const aiChip = await screen.findByRole("button", {
      name: /Artificial Intelligence/,
    });
    fireEvent.click(aiChip);

    await waitFor(() => {
      expect(screen.queryByText(/news video title/i)).not.toBeInTheDocument();
    });
    expect(screen.getByText("ai video title")).toBeInTheDocument();
  });

  it("applies the category filter independently of the status chip", async () => {
    const aiVideo = categoryVideo({
      id: "v1",
      title: "ai video title",
      category: "ai",
    });
    const newsVideo = categoryVideo({
      id: "v2",
      title: "news video title",
      category: "news",
    });

    vi.mocked(listVideos).mockImplementation(async (opts) => {
      if (opts?.category === "ai") return [aiVideo];
      return [aiVideo, newsVideo];
    });

    render(<Library onOpenVideo={() => {}} search="" />);
    await screen.findByText("news video title");

    fireEvent.click(screen.getByRole("button", { name: /Unwatched/ }));
    const aiChip = await screen.findByRole("button", {
      name: /Artificial Intelligence/,
    });
    fireEvent.click(aiChip);

    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ filter: "unwatched", category: "ai" }),
      );
    });
    // Selecting a category must not reset the status chip, and vice versa.
    expect(screen.getByRole("button", { name: /Unwatched/ })).toHaveClass("on");
    expect(aiChip).toHaveClass("on");
  });

  it("keeps the selected category when the 3s poller refreshes", async () => {
    // Given: a category filter is active. The 3s poller (Library.tsx:163)
    // only arms while a download job is pending/running, so listDownloads
    // must report one to make the interval fire at all.
    const aiVideo = categoryVideo({
      id: "v1",
      title: "ai video title",
      category: "ai",
    });
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

    render(<Library onOpenVideo={() => {}} search="" />);
    const aiChip = await screen.findByRole("button", {
      name: /Artificial Intelligence/,
    });
    fireEvent.click(aiChip);
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ filter: "all", category: "ai" }),
      );
    });
    vi.mocked(listVideos).mockClear();

    // When: the 3s poller fires.
    await vi.advanceTimersByTimeAsync(3000);

    // Then: the refresh still carries the category, not just the status.
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ filter: "all", category: "ai" }),
      );
    });
  });

  it("refetches with the query when the search prop changes", async () => {
    // The search box itself now lives in the top bar (App owns the state);
    // Library just receives the query as a prop and debounces its own fetch.
    vi.mocked(listVideos).mockResolvedValue([]);
    const { rerender } = render(<Library onOpenVideo={() => {}} search="" />);
    await screen.findByLabelText(/sort/i);

    rerender(<Library onOpenVideo={() => {}} search="abyss" />);

    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ q: "abyss" }),
      );
    });
  });

  it("choosing a sort option refetches with that sort", async () => {
    vi.mocked(listVideos).mockResolvedValue([]);
    const user = userEvent.setup();
    render(<Library onOpenVideo={() => {}} search="" />);
    await screen.findByLabelText(/sort/i);

    await user.selectOptions(screen.getByLabelText(/sort/i), "longest");

    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ sort: "longest" }),
      );
    });
  });

  it("clicking favorite calls setFavorite and updates optimistically", async () => {
    const v = categoryVideo({ id: "v1", favorite: false });
    vi.mocked(listVideos).mockResolvedValue([v]);
    vi.mocked(setFavorite).mockResolvedValue(true);
    render(<Library onOpenVideo={() => {}} search="" />);
    await screen.findByText("A Test Video");

    fireEvent.click(screen.getByRole("button", { name: "Add to favorites" }));

    await waitFor(() => expect(setFavorite).toHaveBeenCalledWith("v1", true));
    expect(
      await screen.findByRole("button", { name: "Remove from favorites" }),
    ).toBeInTheDocument();
  });

  it("reverts the optimistic favorite flip when setFavorite fails", async () => {
    const v = categoryVideo({ id: "v1", favorite: false });
    vi.mocked(listVideos).mockResolvedValue([v]);
    vi.mocked(setFavorite).mockRejectedValue(new Error("network down"));
    render(<Library onOpenVideo={() => {}} search="" />);
    await screen.findByText("A Test Video");

    fireEvent.click(screen.getByRole("button", { name: "Add to favorites" }));

    // Optimistic update fires immediately.
    expect(
      await screen.findByRole("button", { name: "Remove from favorites" }),
    ).toBeInTheDocument();

    // Then reverts once the request rejects.
    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Add to favorites" }),
      ).toBeInTheDocument();
    });
  });

  it("clicking watched calls setWatched and updates optimistically", async () => {
    const v = categoryVideo({ id: "v1", watched: false });
    vi.mocked(listVideos).mockResolvedValue([v]);
    vi.mocked(setWatched).mockResolvedValue(true);
    render(<Library onOpenVideo={() => {}} search="" />);
    await screen.findByText("A Test Video");

    fireEvent.click(screen.getByRole("button", { name: "Mark watched" }));

    await waitFor(() => expect(setWatched).toHaveBeenCalledWith("v1", true));
    expect(
      await screen.findByRole("button", { name: "Mark unwatched" }),
    ).toBeInTheDocument();
  });

  it("reverts the optimistic watched flip when setWatched fails", async () => {
    const v = categoryVideo({ id: "v1", watched: false });
    vi.mocked(listVideos).mockResolvedValue([v]);
    vi.mocked(setWatched).mockRejectedValue(new Error("network down"));
    render(<Library onOpenVideo={() => {}} search="" />);
    await screen.findByText("A Test Video");

    fireEvent.click(screen.getByRole("button", { name: "Mark watched" }));

    expect(
      await screen.findByRole("button", { name: "Mark unwatched" }),
    ).toBeInTheDocument();

    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Mark watched" }),
      ).toBeInTheDocument();
    });
  });

  it("refetches both lists after a successful re-download", async () => {
    const errored = categoryVideo({ id: "v1", status: "error" });
    // Categorized so the recovered card has a visible fresh-state lifecycle
    // row (the category pill) to assert on.
    const refreshed = categoryVideo({
      id: "v1",
      status: "downloaded",
      category: "ai",
    });
    const { redownload } = await import("../api");
    vi.mocked(redownload).mockResolvedValue(undefined);
    let fixed = false;
    vi.mocked(listVideos).mockImplementation(async () =>
      fixed ? [refreshed] : [errored],
    );

    render(<Library onOpenVideo={() => {}} search="" />);
    await screen.findByText("Download failed");

    fixed = true;
    fireEvent.click(screen.getByRole("button", { name: /re-download/i }));

    await waitFor(() => expect(redownload).toHaveBeenCalledWith("v1"));
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ filter: "all" }),
      );
    });
    // Back to the fresh lifecycle state — the error line is gone and the
    // category pill has taken its place. Scoped to .life.fresh because the
    // chip row above the grid also carries an "Artificial Intelligence" label.
    await waitFor(() => {
      expect(document.querySelector(".life.fresh .metapill")).toHaveTextContent(
        "Artificial Intelligence",
      );
    });
    expect(screen.queryByText("Download failed")).not.toBeInTheDocument();
  });
});
