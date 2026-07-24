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

  // The card used to draw a progress ring and a "Downloading" lifecycle line.
  // Both are gone: the library list no longer returns in-flight rows at all, so
  // this state is unreachable through the app, and download progress lives in
  // the rail's status panel where it is visible from every page. Rendering the
  // card directly with an in-flight status is the only way to reach the old
  // code path, which is exactly what makes it a useful guard against the
  // progress UI creeping back onto the card.
  it("shows no download progress even when handed an in-flight video", () => {
    render(
      <VideoCard
        video={baseVideo({ status: "downloading", has_media: false })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );
    expect(document.querySelector(".ring")).toBeNull();
    expect(screen.queryByText("Downloading")).not.toBeInTheDocument();
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
    expect(pill).toHaveTextContent("AI");
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
    vi.unstubAllGlobals();
  });

  it("offers a Watched chip and lists watched videos in the grid", async () => {
    vi.mocked(listVideos).mockResolvedValue([
      categoryVideo({ id: "v1", title: "watched video", watched: true }),
    ]);
    render(<Library onOpenVideo={() => {}} search="" />);
    // The default chip is Unwatched, so select Watched to see watched videos.
    fireEvent.click(screen.getByRole("button", { name: /Watched/ }));
    await screen.findByText("watched video");

    const chipNames = Array.from(document.querySelectorAll(".chips .chip")).map(
      (c) => c.textContent?.replace(/\d+$/, "").trim(),
    );
    // No "Downloading" chip: the library only lists videos that are actually
    // here, so a chip for in-flight ones would always be empty. Unwatched leads
    // (it is the default), All sits to its right, then the In progress split.
    expect(chipNames).toEqual([
      "Unwatched",
      "All",
      "In progress",
      "Favorites",
      "Watched",
    ]);

    // Watched videos live in the main grid now, not a separate drawer.
    expect(document.querySelector(".drawer")).toBeNull();
    expect(document.querySelector(".grid .card")).not.toBeNull();
  });

  it("defaults to the Unwatched chip and fetches with the unwatched filter", async () => {
    vi.mocked(listVideos).mockResolvedValue([
      categoryVideo({ id: "v1", title: "fresh video", watched: false }),
    ]);
    render(<Library onOpenVideo={() => {}} search="" />);
    await screen.findByText("fresh video");

    // Unwatched leads and is active on first paint — no click needed.
    expect(screen.getByRole("button", { name: /Unwatched/ })).toHaveClass("on");
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ filter: "unwatched" }),
      );
    });
  });

  it("selecting In progress refetches with the in_progress filter", async () => {
    vi.mocked(listVideos).mockResolvedValue([
      categoryVideo({
        id: "v1",
        title: "half-watched video",
        watched: false,
        duration_seconds: 100,
        resume_position_seconds: 40,
      }),
    ]);
    render(<Library onOpenVideo={() => {}} search="" />);
    // The default Unwatched filter hides a partially-watched card (resume > 0),
    // so switch to In progress first, then the card appears.
    fireEvent.click(screen.getByRole("button", { name: /In progress/ }));
    await screen.findByText("half-watched video");

    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ filter: "in_progress" }),
      );
    });
    expect(screen.getByRole("button", { name: /In progress/ })).toHaveClass(
      "on",
    );
  });

  it("names the active filter in the empty state instead of claiming the library is empty", async () => {
    // A library whose only videos are watched shows nothing under the default
    // Unwatched filter — the message must say "Nothing unwatched", not falsely
    // report an empty library.
    vi.mocked(listVideos).mockResolvedValue([
      categoryVideo({ id: "v1", watched: true }),
    ]);
    render(<Library onOpenVideo={() => {}} search="" />);

    expect(await screen.findByText("Nothing unwatched.")).toBeInTheDocument();
    expect(screen.queryByText("No videos yet.")).not.toBeInTheDocument();
  });

  it("drops a card from the Unwatched grid the moment it is marked watched", async () => {
    const v = categoryVideo({
      id: "v1",
      title: "unwatched vid",
      watched: false,
    });
    vi.mocked(listVideos).mockResolvedValue([v]);
    vi.mocked(setWatched).mockResolvedValue(true);
    render(<Library onOpenVideo={() => {}} search="" />);
    await screen.findByText("unwatched vid");

    fireEvent.click(screen.getByRole("button", { name: /Unwatched/ }));
    await waitFor(() =>
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ filter: "unwatched" }),
      ),
    );

    // Optimistic mark-watched: the card no longer matches the Unwatched filter,
    // so it must leave the grid at once (not linger until the next refetch).
    fireEvent.click(screen.getByRole("button", { name: "Mark watched" }));
    await waitFor(() => expect(setWatched).toHaveBeenCalledWith("v1", true));
    await waitFor(() =>
      expect(screen.queryByText("unwatched vid")).not.toBeInTheDocument(),
    );
  });

  it("selecting the Watched chip refetches with the watched filter", async () => {
    vi.mocked(listVideos).mockResolvedValue([
      categoryVideo({ id: "v1", watched: true }),
    ]);
    render(<Library onOpenVideo={() => {}} search="" />);
    // The chip row renders immediately; the watched card is hidden under the
    // default Unwatched filter until this chip is selected.
    fireEvent.click(screen.getByRole("button", { name: /Watched/ }));
    await screen.findByText("A Test Video");

    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ filter: "watched" }),
      );
    });
    expect(screen.getByRole("button", { name: /Watched/ })).toHaveClass("on");
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
      name: /AI/,
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
      name: /AI/,
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

  it("scopes the category row to the active status chip", async () => {
    // allVideos (the unfiltered load) drives the category row: an unwatched AI
    // video and a watched News video. Each status chip should only offer the
    // categories present among videos matching it.
    const aiUnwatched = categoryVideo({
      id: "v1",
      title: "ai vid",
      category: "ai",
      watched: false,
    });
    const newsWatched = categoryVideo({
      id: "v2",
      title: "news vid",
      category: "news",
      watched: true,
    });
    vi.mocked(listVideos).mockImplementation(async (opts) => {
      if (opts?.filter === "all") return [aiUnwatched, newsWatched];
      if (opts?.filter === "watched") return [newsWatched];
      if (opts?.filter === "unwatched") return [aiUnwatched];
      return [];
    });

    render(<Library onOpenVideo={() => {}} search="" />);
    await screen.findByText("ai vid");

    // Default Unwatched chip: only the AI video qualifies, so the row offers AI
    // (not News) and "All categories" counts the subset (1), not the full 2.
    await waitFor(() => {
      const cats = Array.from(
        document.querySelectorAll(".catchips .catchip"),
      ).map((c) => c.textContent ?? "");
      expect(cats.some((t) => /AI/.test(t))).toBe(true);
      expect(cats.some((t) => /News/.test(t))).toBe(false);
    });
    expect(document.querySelector(".catchips .catchip")?.textContent).toMatch(
      /All categories\s*1/,
    );

    // Switch to Watched: now only the News category is present.
    fireEvent.click(screen.getByRole("button", { name: /Watched/ }));
    await waitFor(() => {
      const cats = Array.from(
        document.querySelectorAll(".catchips .catchip"),
      ).map((c) => c.textContent ?? "");
      expect(cats.some((t) => /News/.test(t))).toBe(true);
      expect(cats.some((t) => /AI/.test(t))).toBe(false);
    });
  });

  it("falls back to All categories when the selected category vanishes under the new chip", async () => {
    const aiUnwatched = categoryVideo({
      id: "v1",
      title: "ai vid",
      category: "ai",
      watched: false,
    });
    const newsWatched = categoryVideo({
      id: "v2",
      title: "news vid",
      category: "news",
      watched: true,
    });
    vi.mocked(listVideos).mockImplementation(async (opts) => {
      if (opts?.filter === "all") return [aiUnwatched, newsWatched];
      if (opts?.category === "news") return [newsWatched];
      if (opts?.filter === "watched") return [newsWatched];
      return [aiUnwatched];
    });

    render(<Library onOpenVideo={() => {}} search="" />);
    // Under All, the News category chip is available; select it.
    fireEvent.click(screen.getByRole("button", { name: /^All \d/ }));
    const newsChip = await screen.findByRole("button", { name: /News/ });
    fireEvent.click(newsChip);
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ category: "news" }),
      );
    });
    vi.mocked(listVideos).mockClear();

    // Switch to Unwatched: News has no unwatched video, so the stale category
    // is dropped and the list refetches with "all" rather than stranding the
    // grid on an invisible filter.
    fireEvent.click(screen.getByRole("button", { name: /Unwatched/ }));
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ filter: "unwatched", category: "all" }),
      );
    });
  });

  it("keeps the selected category when a finishing download refreshes the list", async () => {
    // Given: a category filter is active. A video joins the library the moment
    // its download completes, and App signals that by handing down a changed
    // activeDownloads count — Library no longer runs its own queue poll.
    const aiVideo = categoryVideo({
      id: "v1",
      title: "ai video title",
      category: "ai",
    });
    vi.mocked(listVideos).mockResolvedValue([aiVideo]);

    const { rerender } = render(
      <Library onOpenVideo={() => {}} search="" activeDownloads={1} />,
    );
    const aiChip = await screen.findByRole("button", {
      name: /AI/,
    });
    fireEvent.click(aiChip);
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ filter: "unwatched", category: "ai" }),
      );
    });
    vi.mocked(listVideos).mockClear();

    // When: the last download finishes.
    rerender(<Library onOpenVideo={() => {}} search="" activeDownloads={0} />);

    // Then: the refresh still carries the category, not just the status — the
    // bug this pins is a refresh that silently resets the user's filter. The
    // status chip is left at its default (Unwatched); only the category moved.
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ filter: "unwatched", category: "ai" }),
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
    // Use the All filter so the card stays put after being marked watched
    // (under Unwatched it would correctly leave the grid).
    fireEvent.click(screen.getByRole("button", { name: /^All \d/ }));
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
    // All filter so the optimistic flip is observable in place rather than
    // dropping the card out of the Unwatched grid.
    fireEvent.click(screen.getByRole("button", { name: /^All \d/ }));
    await screen.findByText("A Test Video");

    fireEvent.click(screen.getByRole("button", { name: "Mark watched" }));

    // The optimistic flip lands synchronously with the click and the
    // rejection reverts it on the next macrotask, so this has to be a plain
    // query rather than a findBy: awaiting first lets the rollback win.
    expect(
      screen.getByRole("button", { name: "Mark unwatched" }),
    ).toBeInTheDocument();

    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Mark watched" }),
      ).toBeInTheDocument();
    });
    // And it says why. A card that flips and flips back with no explanation
    // reads as a broken button.
    expect(screen.getByText("network down")).toBeInTheDocument();
  });

  it("clears the resume bar when a video is marked watched", async () => {
    // 40% in and unwatched: the card starts with a resume bar. Marking it
    // watched must drop the stored position — the API answers with the watched
    // flag alone, so a Library that forgot to zero it locally would show the
    // bar again the moment the card is un-watched. (Watched cards stay in the
    // main grid now; there is no drawer to move into.)
    const v = categoryVideo({
      id: "v1",
      watched: false,
      duration_seconds: 100,
      resume_position_seconds: 40,
    });
    vi.mocked(listVideos).mockResolvedValue([v]);
    vi.mocked(setWatched).mockResolvedValue(true);
    render(<Library onOpenVideo={() => {}} search="" />);
    // A partially-watched card (resume > 0) is hidden under the default
    // Unwatched filter; the All filter shows it so the resume bar is present.
    fireEvent.click(screen.getByRole("button", { name: /^All \d/ }));
    await screen.findByText("A Test Video");

    expect(document.querySelector(".resume")).not.toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Mark watched" }));
    await waitFor(() => expect(setWatched).toHaveBeenCalledWith("v1", true));

    // Watched now, so the resume bar is gone (VideoCard only draws it when
    // unwatched) — and the card is still here, not folded away.
    await waitFor(() => expect(document.querySelector(".resume")).toBeNull());
    expect(document.querySelector(".grid .card")).not.toBeNull();

    // And the position is gone: un-watching brings the card back with no bar.
    vi.mocked(setWatched).mockResolvedValue(false);
    fireEvent.click(screen.getByRole("button", { name: "Mark unwatched" }));
    await waitFor(() => expect(setWatched).toHaveBeenCalledWith("v1", false));
    expect(document.querySelector(".resume")).toBeNull();
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
    // An errored card is hidden under the default Unwatched filter (it is not
    // play-eligible); the All filter surfaces it so it can be re-downloaded.
    fireEvent.click(screen.getByRole("button", { name: /^All \d/ }));
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
    // chip row above the grid also carries an "AI" label.
    await waitFor(() => {
      expect(document.querySelector(".life.fresh .metapill")).toHaveTextContent(
        "AI",
      );
    });
    expect(screen.queryByText("Download failed")).not.toBeInTheDocument();
  });
});
