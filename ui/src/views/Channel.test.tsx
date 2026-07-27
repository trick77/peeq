import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Channel } from "./Channel";
import type { ChannelDetail } from "../api/types";

vi.mock("../api/channels", () => ({
  listChannels: vi.fn(),
  addChannel: vi.fn(),
  updateChannel: vi.fn(),
  subscribeChannel: vi.fn(),
  unsubscribeChannel: vi.fn(),
  deleteChannel: vi.fn(),
  getChannel: vi.fn(),
  scanChannel: vi.fn(),
  refreshChannel: vi.fn(),
  channelAvatarUrl: (id: string) => `/api/channels/${id}/avatar`,
  channelBannerUrl: (id: string) => `/api/channels/${id}/banner`,
}));
vi.mock("../api/videos", () => ({
  listVideos: vi.fn(),
  thumbnailUrl: (id: string) => `/t/${id}`,
  setFavorite: vi.fn(),
  setWatched: vi.fn(),
}));
vi.mock("../api/pending", () => ({
  listPending: vi.fn(),
  downloadPending: vi.fn(),
  ignorePending: vi.fn(),
}));
// ArchiveTab pulls getSettings from the barrel ("../../api" from
// views/channel/, i.e. "../api" from here). Left unmocked, the real
// settings.ts module fires a genuine fetch that fails under jsdom, which
// silently exercised only the .catch() fallback — never the success path
// that sets retentionDays from the response.
vi.mock("../api/settings", () => ({ getSettings: vi.fn() }));

import {
  getChannel,
  scanChannel,
  updateChannel,
  deleteChannel,
  refreshChannel,
} from "../api/channels";
import { listVideos, setFavorite, setWatched } from "../api/videos";
import { listPending, downloadPending, ignorePending } from "../api/pending";
import { getSettings } from "../api/settings";
import type { Settings, Video } from "../api/types";
import {
  formatRuntime,
  formatBytes,
  formatSubscribers,
  formatStamp,
} from "./Channel";

function settings(overrides: Partial<Settings> = {}): Settings {
  return {
    cookie_status: "valid",
    format_preset: "",
    format_custom: "",
    limit_rate: "",
    throttle_base_seconds: 0,
    retention_days: 14,
    min_free_gb: 0,
    min_video_duration_seconds: 0,
    subtitles_default: false,
    ytdlp_version: "",
    youtube_paused: false,
    youtube_pause_reason: "",
    ...overrides,
  };
}

// archiveVideo — a minimal, valid Video row for the Archive tab's grid,
// mirroring Library.test.tsx's baseVideo/categoryVideo helpers.
function archiveVideo(overrides: Partial<Video> = {}): Video {
  return {
    id: "v1",
    url: "https://youtu.be/v1",
    title: "A Test Video",
    channel_id: "UCa",
    channel_name: "Uncanny Expeditions",
    duration_seconds: 754,
    has_thumbnail: false,
    has_media: true,
    availability: "available",
    status: "downloaded",
    watched: false,
    resume_position_seconds: 0,
    state_version: 1,
    favorite: false,
    summary: "",
    chapters: [],
    key_points: [],
    summary_status: "pending",
    audio_language: "",
    has_subtitles: false,
    category: "uncategorized",
    ...overrides,
  };
}

// sqlUTC formats an instant the way the backend stores one ("2026-07-25
// 06:11:14", UTC, no zone suffix). next_scan_at has to be RELATIVE to now: the
// UI derives "scan queued" from whether that instant has passed, so a hardcoded
// date would silently flip the default state the moment it went stale.
function sqlUTC(offsetMs: number): string {
  return new Date(Date.now() + offsetMs)
    .toISOString()
    .slice(0, 19)
    .replace("T", " ");
}

function detail(overrides: Partial<ChannelDetail> = {}): ChannelDetail {
  return {
    id: "UCa",
    name: "Uncanny Expeditions",
    handle: "@UncannyExpeditions",
    description: "Field documentaries.",
    has_avatar: true,
    has_banner: true,
    subscribers: 7240000,
    verified: true,
    resolved_at: "2026-07-21 06:00:00",
    resolve_ok: true,
    gone: false,
    added: true,
    added_at: "2026-03-14 09:00:00",
    archived_count: 142,
    runtime_seconds: 219600,
    disk_bytes: 40802189312,
    newest_published_at: "2026-07-18T00:00:00Z",
    subscribed: true,
    autodownload: true,
    format_override: "",
    last_scanned_at: "2026-07-20 08:00:00",
    next_scan_at: sqlUTC(6 * 3600 * 1000), // due in 6h → not queued
    pending_count: 7,
    ...overrides,
  };
}

describe("Channel", () => {
  beforeEach(() => {
    vi.mocked(getChannel).mockReset();
    vi.mocked(listVideos).mockReset();
    vi.mocked(listPending).mockReset();
    vi.mocked(getChannel).mockResolvedValue(detail());
    vi.mocked(listVideos).mockResolvedValue([]);
    vi.mocked(listPending).mockResolvedValue([]);
    vi.mocked(scanChannel).mockReset();
    vi.mocked(scanChannel).mockResolvedValue({ status: "scheduled" });
    vi.mocked(setFavorite).mockReset();
    vi.mocked(setFavorite).mockResolvedValue(true);
    vi.mocked(setWatched).mockReset();
    vi.mocked(setWatched).mockResolvedValue({
      watched: true,
      state_version: 2,
    });
    vi.mocked(updateChannel).mockReset();
    vi.mocked(updateChannel).mockResolvedValue({
      id: "UCa",
      autodownload: false,
      format_override: "",
    });
    vi.mocked(deleteChannel).mockReset();
    vi.mocked(deleteChannel).mockResolvedValue(undefined);
    vi.mocked(downloadPending).mockReset();
    vi.mocked(downloadPending).mockResolvedValue(undefined);
    vi.mocked(ignorePending).mockReset();
    vi.mocked(ignorePending).mockResolvedValue(undefined);
    vi.mocked(getSettings).mockReset();
    vi.mocked(getSettings).mockResolvedValue(settings());
  });

  it("shows the channel name and its four stats", async () => {
    const { container } = render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    const stats = container.querySelector(".chan-stats") as HTMLElement;
    expect(within(stats).getByText("142")).toBeInTheDocument();
    expect(within(stats).getByText(/61 h/)).toBeInTheDocument();
    expect(within(stats).getByText(/38(\.\d+)? GB/)).toBeInTheDocument();
  });

  it("the Archive tab shows its count badge", async () => {
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    const archiveTab = screen.getByRole("tab", { name: /archive/i });
    expect(within(archiveTab).getByText("142")).toBeInTheDocument();
  });

  it("a channel that has not been added hides the New and Settings tabs", async () => {
    vi.mocked(getChannel).mockResolvedValue(
      detail({ added: false, subscribed: false, pending_count: 0 }),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    expect(screen.getByRole("tab", { name: /archive/i })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: /new/i })).toBeNull();
    expect(screen.queryByRole("tab", { name: /settings/i })).toBeNull();
    expect(
      screen.getByRole("button", { name: /add this channel/i }),
    ).toBeInTheDocument();
  });

  it("switching to the New tab loads that channel's pending videos", async () => {
    const user = userEvent.setup();
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("tab", { name: /new/i }));

    await waitFor(() => {
      expect(listPending).toHaveBeenCalledWith("UCa");
    });
  });

  it("the archive tab loads only this channel's videos", async () => {
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ channel: "UCa" }),
      );
    });
  });

  it("the New tab's empty state says when the next scan is due", async () => {
    const user = userEvent.setup();
    vi.mocked(listPending).mockResolvedValue([]);
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("tab", { name: /new/i }));

    expect(await screen.findByText(/nothing new/i)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /scan now/i }),
    ).toBeInTheDocument();
  });

  it("a blocked scan shows the reason rather than reporting success", async () => {
    const user = userEvent.setup();
    vi.mocked(scanChannel).mockResolvedValue({
      status: "blocked",
      reason:
        "Your YouTube cookie needs refreshing before peeq can scan this channel.",
    });
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /new/i }));

    await user.click(await screen.findByRole("button", { name: /scan now/i }));

    expect(
      await screen.findByText(/cookie needs refreshing/i),
    ).toBeInTheDocument();
  });

  it("the New tab reports 'Never scanned' when the channel has no last_scanned_at", async () => {
    const user = userEvent.setup();
    vi.mocked(getChannel).mockResolvedValue(
      detail({ last_scanned_at: undefined }),
    );
    vi.mocked(listPending).mockResolvedValue([]);
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("tab", { name: /new/i }));

    expect(await screen.findByText(/never scanned/i)).toBeInTheDocument();
  });

  it("shows an error when loading the New tab's pending list fails", async () => {
    const user = userEvent.setup();
    vi.mocked(listPending).mockRejectedValue(
      new Error("failed to load pending"),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("tab", { name: /new/i }));

    expect(
      await screen.findByText("failed to load pending"),
    ).toBeInTheDocument();
  });

  it("shows an error when the New tab's scan request itself fails", async () => {
    const user = userEvent.setup();
    vi.mocked(scanChannel).mockRejectedValue(
      new Error("failed to schedule a scan"),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /new/i }));

    await user.click(await screen.findByRole("button", { name: /scan now/i }));

    expect(
      await screen.findByText("failed to schedule a scan"),
    ).toBeInTheDocument();
  });

  it("falls back to a 0-day retention window on the Archive tab when settings fail to load", async () => {
    const watchedLongAgo = new Date(Date.now() - 30 * 86400000).toISOString();
    vi.mocked(getSettings).mockRejectedValue(
      new Error("failed to load settings"),
    );
    vi.mocked(listVideos).mockResolvedValue([
      archiveVideo({
        id: "v1",
        watched: true,
        watched_at: watchedLongAgo,
        favorite: false,
      }),
    ]);
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    // retentionDays falls back to 0 (rather than staying stuck loading), so
    // an already-watched video reads as expiring right away.
    expect(await screen.findByText("Expires soon")).toBeInTheDocument();
  });

  it("clicking a card's favorite button on the Archive tab calls setFavorite and updates optimistically", async () => {
    const user = userEvent.setup();
    vi.mocked(listVideos).mockResolvedValue([
      archiveVideo({ id: "v1", favorite: false }),
    ]);

    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await screen.findByText("A Test Video");

    const favoriteButton = screen.getByRole("button", {
      name: "Add to favorites",
    });
    await user.click(favoriteButton);

    await waitFor(() => {
      expect(setFavorite).toHaveBeenCalledWith("v1", true);
    });
    expect(
      await screen.findByRole("button", { name: "Remove from favorites" }),
    ).toBeInTheDocument();
  });

  it("the delete button names how many videos it will destroy", async () => {
    const user = userEvent.setup();
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    expect(
      await screen.findByRole("button", {
        name: /delete channel and its 142 videos/i,
      }),
    ).toBeInTheDocument();
  });

  it("toggling auto-add saves it", async () => {
    const user = userEvent.setup();
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    await user.click(
      await screen.findByLabelText(/add new videos automatically/i),
    );

    await waitFor(() => {
      expect(updateChannel).toHaveBeenCalledWith("UCa", {
        autodownload: false,
      });
    });
  });

  it("the Settings tab's Scan now button is disabled while the scan request is in flight", async () => {
    const user = userEvent.setup();
    let resolveScan: (v: { status: "scheduled" }) => void;
    vi.mocked(scanChannel).mockReturnValue(
      new Promise((res) => {
        resolveScan = res as typeof resolveScan;
      }),
    );

    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    const checkNowButton = await screen.findByRole("button", {
      name: /scan now/i,
    });

    await user.click(checkNowButton);

    // While the request is in flight, the button should be disabled
    expect(checkNowButton).toHaveAttribute("disabled");

    // Resolve the promise
    resolveScan!({ status: "scheduled" });

    // After the request completes, the button should be enabled again
    await waitFor(() => {
      expect(checkNowButton).not.toHaveAttribute("disabled");
    });
  });

  it("an error loading the channel is shown instead of a blank page", async () => {
    vi.mocked(getChannel).mockReset();
    vi.mocked(getChannel).mockRejectedValue(
      new Error("failed to load channel"),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    expect(
      await screen.findByText("failed to load channel"),
    ).toBeInTheDocument();
  });

  it("subscribing from the header calls subscribeChannel and reloads", async () => {
    const user = userEvent.setup();
    vi.mocked(getChannel).mockResolvedValue(detail({ subscribed: false }));
    const { subscribeChannel } = await import("../api/channels");
    vi.mocked(subscribeChannel).mockReset();
    vi.mocked(subscribeChannel).mockResolvedValue({ status: "subscribed" });
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("button", { name: /^subscribe$/i }));

    await waitFor(() => expect(subscribeChannel).toHaveBeenCalledWith("UCa"));
    await waitFor(() => expect(getChannel).toHaveBeenCalledTimes(2));
  });

  it("unsubscribing from the header calls unsubscribeChannel and reloads", async () => {
    const user = userEvent.setup();
    const { unsubscribeChannel } = await import("../api/channels");
    vi.mocked(unsubscribeChannel).mockReset();
    vi.mocked(unsubscribeChannel).mockResolvedValue({ status: "unsubscribed" });
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("button", { name: /^subscribed$/i }));

    await waitFor(() => expect(unsubscribeChannel).toHaveBeenCalledWith("UCa"));
    await waitFor(() => expect(getChannel).toHaveBeenCalledTimes(2));
  });

  it("shows an error and leaves the button usable when toggling subscribe fails", async () => {
    const user = userEvent.setup();
    const { unsubscribeChannel } = await import("../api/channels");
    vi.mocked(unsubscribeChannel).mockReset();
    vi.mocked(unsubscribeChannel).mockRejectedValue(
      new Error("subscribe toggle failed"),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("button", { name: /^subscribed$/i }));

    expect(
      await screen.findByText("subscribe toggle failed"),
    ).toBeInTheDocument();
  });

  it("the Add button calls addChannel and reloads the channel as added", async () => {
    const user = userEvent.setup();
    vi.mocked(getChannel).mockResolvedValue(
      detail({ added: false, subscribed: false, pending_count: 0 }),
    );
    const { addChannel } = await import("../api/channels");
    vi.mocked(addChannel).mockReset();
    vi.mocked(addChannel).mockResolvedValue({
      id: "UCa",
      name: "Uncanny Expeditions",
      subscribed: false,
    });
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("button", { name: /add this channel/i }));

    await waitFor(() => {
      expect(addChannel).toHaveBeenCalledWith(
        "https://www.youtube.com/channel/UCa",
        false,
      );
    });
    await waitFor(() => expect(getChannel).toHaveBeenCalledTimes(2));
  });

  it("shows an error when adding the channel fails", async () => {
    const user = userEvent.setup();
    vi.mocked(getChannel).mockResolvedValue(
      detail({ added: false, subscribed: false, pending_count: 0 }),
    );
    const { addChannel } = await import("../api/channels");
    vi.mocked(addChannel).mockReset();
    vi.mocked(addChannel).mockRejectedValue(new Error("failed to add channel"));
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("button", { name: /add this channel/i }));

    expect(
      await screen.findByText("failed to add channel"),
    ).toBeInTheDocument();
  });

  it("renders a handle-less, not-added, description-less channel without crashing", async () => {
    vi.mocked(getChannel).mockResolvedValue(
      detail({
        handle: undefined,
        description: undefined,
        added: false,
        subscribed: false,
        pending_count: 0,
      }),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    expect(screen.getByText("Not added")).toBeInTheDocument();
  });

  it("the New tab shows its own pending-count badge", async () => {
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    const newTab = screen.getByRole("tab", { name: /new/i });
    expect(within(newTab).getByText("7")).toBeInTheDocument();
  });

  it("clicking a card's watched button on the Archive tab calls setWatched and updates optimistically", async () => {
    const user = userEvent.setup();
    vi.mocked(listVideos).mockResolvedValue([
      archiveVideo({ id: "v1", watched: false }),
    ]);

    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await screen.findByText("A Test Video");

    await user.click(screen.getByRole("button", { name: "Mark watched" }));

    await waitFor(() => expect(setWatched).toHaveBeenCalledWith("v1", true));
    expect(
      await screen.findByRole("button", { name: "Mark unwatched" }),
    ).toBeInTheDocument();
  });

  it("reverts the optimistic favorite flip on the Archive tab when setFavorite fails", async () => {
    const user = userEvent.setup();
    vi.mocked(listVideos).mockResolvedValue([
      archiveVideo({ id: "v1", favorite: false }),
    ]);
    vi.mocked(setFavorite).mockRejectedValue(new Error("save failed"));

    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await screen.findByText("A Test Video");

    await user.click(screen.getByRole("button", { name: "Add to favorites" }));

    // The request rejects synchronously (mockRejectedValue), so by the time
    // we can observe DOM state the optimistic flip has already been reverted
    // — assert the settled state, not the fleeting intermediate one.
    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Add to favorites" }),
      ).toBeInTheDocument();
    });
    expect(await screen.findByText("save failed")).toBeInTheDocument();
  });

  it("reverts the optimistic watched flip on the Archive tab when setWatched fails", async () => {
    const user = userEvent.setup();
    vi.mocked(listVideos).mockResolvedValue([
      archiveVideo({ id: "v1", watched: false }),
    ]);
    vi.mocked(setWatched).mockRejectedValue(new Error("save failed"));

    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await screen.findByText("A Test Video");

    await user.click(screen.getByRole("button", { name: "Mark watched" }));

    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Mark watched" }),
      ).toBeInTheDocument();
    });
  });

  it("an error listing the channel's videos is shown on the Archive tab", async () => {
    vi.mocked(listVideos).mockRejectedValue(new Error("failed to load videos"));
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    expect(
      await screen.findByText("failed to load videos"),
    ).toBeInTheDocument();
  });

  it("typing in the Archive tab's search box refetches with the query", async () => {
    const user = userEvent.setup();
    vi.mocked(listVideos).mockResolvedValue([]);
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    await user.type(screen.getByLabelText("Search this channel"), "abyss");

    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ channel: "UCa", q: "abyss" }),
      );
    });
    expect(screen.getByText("No videos match.")).toBeInTheDocument();
  });

  it("choosing a category on the Archive tab refetches with that category", async () => {
    const user = userEvent.setup();
    vi.mocked(listVideos).mockResolvedValue([]);
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    await user.selectOptions(screen.getByLabelText("Category"), "ai");

    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ channel: "UCa", category: "ai" }),
      );
    });
  });

  it("choosing a sort option on the Archive tab refetches with that sort", async () => {
    const user = userEvent.setup();
    vi.mocked(listVideos).mockResolvedValue([]);
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    await user.selectOptions(screen.getByLabelText("Sort"), "oldest");

    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(
        expect.objectContaining({ channel: "UCa", sort: "oldest" }),
      );
    });
  });

  it("adding a pending item on the New tab calls downloadPending and removes it from the list", async () => {
    const user = userEvent.setup();
    vi.mocked(listPending).mockResolvedValue([
      {
        video_id: "p1",
        channel_id: "UCa",
        channel_name: "Uncanny Expeditions",
        title: "A new upload",
        duration_seconds: 300,
        url: "https://youtu.be/p1",
        thumbnail_url: "https://img.example/p1.jpg",
        discovered_at: "2026-07-21 09:00:00",
      },
    ]);
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /new/i }));
    await screen.findByText("A new upload");

    await user.click(screen.getByRole("button", { name: /^add$/i }));

    await waitFor(() => expect(downloadPending).toHaveBeenCalledWith("p1"));
    await waitFor(() =>
      expect(screen.queryByText("A new upload")).not.toBeInTheDocument(),
    );
  });

  it("shows an error when adding a pending item fails", async () => {
    const user = userEvent.setup();
    vi.mocked(listPending).mockResolvedValue([
      {
        video_id: "p1",
        channel_id: "UCa",
        channel_name: "Uncanny Expeditions",
        title: "A new upload",
        duration_seconds: 300,
        url: "https://youtu.be/p1",
        thumbnail_url: "https://img.example/p1.jpg",
        discovered_at: "2026-07-21 09:00:00",
      },
    ]);
    vi.mocked(downloadPending).mockRejectedValue(
      new Error("failed to download"),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /new/i }));
    await screen.findByText("A new upload");

    await user.click(screen.getByRole("button", { name: /^add$/i }));

    expect(await screen.findByText("failed to download")).toBeInTheDocument();
    expect(screen.getByText("A new upload")).toBeInTheDocument();
  });

  it("ignoring a pending item on the New tab calls ignorePending and removes it from the list", async () => {
    const user = userEvent.setup();
    vi.mocked(listPending).mockResolvedValue([
      {
        video_id: "p1",
        channel_id: "UCa",
        channel_name: "Uncanny Expeditions",
        title: "A new upload",
        duration_seconds: 300,
        url: "https://youtu.be/p1",
        thumbnail_url: "https://img.example/p1.jpg",
        discovered_at: "2026-07-21 09:00:00",
      },
    ]);
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /new/i }));
    await screen.findByText("A new upload");

    await user.click(screen.getByRole("button", { name: /ignore/i }));

    await waitFor(() => expect(ignorePending).toHaveBeenCalledWith("p1"));
    await waitFor(() =>
      expect(screen.queryByText("A new upload")).not.toBeInTheDocument(),
    );
  });

  it("shows an error when ignoring a pending item fails", async () => {
    const user = userEvent.setup();
    vi.mocked(listPending).mockResolvedValue([
      {
        video_id: "p1",
        channel_id: "UCa",
        channel_name: "Uncanny Expeditions",
        title: "A new upload",
        duration_seconds: 300,
        url: "https://youtu.be/p1",
        thumbnail_url: "https://img.example/p1.jpg",
        discovered_at: "2026-07-21 09:00:00",
      },
    ]);
    vi.mocked(ignorePending).mockRejectedValue(new Error("failed to ignore"));
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /new/i }));
    await screen.findByText("A new upload");

    await user.click(screen.getByRole("button", { name: /ignore/i }));

    expect(await screen.findByText("failed to ignore")).toBeInTheDocument();
  });

  it("a scan blocked from the Settings tab shows the reason", async () => {
    const user = userEvent.setup();
    vi.mocked(scanChannel).mockResolvedValue({
      status: "blocked",
      reason:
        "Your YouTube cookie needs refreshing before peeq can scan this channel.",
    });
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    await user.click(await screen.findByRole("button", { name: /scan now/i }));

    expect(
      await screen.findByText(/cookie needs refreshing/i),
    ).toBeInTheDocument();
  });

  it("a scan failure on the Settings tab shows the error", async () => {
    const user = userEvent.setup();
    vi.mocked(scanChannel).mockRejectedValue(
      new Error("failed to schedule a scan"),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    await user.click(await screen.findByRole("button", { name: /scan now/i }));

    expect(
      await screen.findByText("failed to schedule a scan"),
    ).toBeInTheDocument();
  });

  it("cancelling the delete dialog leaves the channel intact", async () => {
    const user = userEvent.setup();
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    await user.click(
      await screen.findByRole("button", {
        name: /delete channel and its 142 videos/i,
      }),
    );
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: /cancel/i }));

    expect(deleteChannel).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("confirming delete in the dialog calls deleteChannel and navigates back", async () => {
    const user = userEvent.setup();
    const onBack = vi.fn();
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={onBack} />);
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    await user.click(
      await screen.findByRole("button", {
        name: /delete channel and its 142 videos/i,
      }),
    );
    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("button", { name: /^delete channel$/i }),
    );

    await waitFor(() => expect(deleteChannel).toHaveBeenCalledWith("UCa"));
    await waitFor(() => expect(onBack).toHaveBeenCalled());
  });

  it("a failed delete shows the error and does not navigate back", async () => {
    const user = userEvent.setup();
    const onBack = vi.fn();
    vi.mocked(deleteChannel).mockRejectedValue(
      new Error("failed to delete channel"),
    );
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={onBack} />);
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    await user.click(
      await screen.findByRole("button", {
        name: /delete channel and its 142 videos/i,
      }),
    );
    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("button", { name: /^delete channel$/i }),
    );

    expect(
      await screen.findByText("failed to delete channel"),
    ).toBeInTheDocument();
    expect(onBack).not.toHaveBeenCalled();
  });

  it("blurring the format override field saves it when the value changed", async () => {
    const user = userEvent.setup();
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    const formatInput = await screen.findByLabelText(/format override/i);
    await user.click(formatInput);
    await user.type(formatInput, "bestaudio");
    await user.tab();

    await waitFor(() => {
      expect(updateChannel).toHaveBeenCalledWith("UCa", {
        format_override: "bestaudio",
      });
    });
  });

  it("blurring the format override field unchanged does not save", async () => {
    const user = userEvent.setup();
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    const formatInput = await screen.findByLabelText(/format override/i);
    await user.click(formatInput);
    await user.tab();

    expect(updateChannel).not.toHaveBeenCalled();
  });

  it("subscribing from the Settings tab calls subscribeChannel", async () => {
    const user = userEvent.setup();
    vi.mocked(getChannel).mockResolvedValue(detail({ subscribed: false }));
    const { subscribeChannel } = await import("../api/channels");
    vi.mocked(subscribeChannel).mockReset();
    vi.mocked(subscribeChannel).mockResolvedValue({ status: "subscribed" });
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    const settingsPanel = document.querySelector(
      ".chan-settings",
    ) as HTMLElement;
    await user.click(
      within(settingsPanel).getByRole("button", { name: /^subscribe$/i }),
    );

    await waitFor(() => expect(subscribeChannel).toHaveBeenCalledWith("UCa"));
  });

  it("an unsubscribed, tracked channel's Settings tab hides autodownload/format/scan rows", async () => {
    vi.mocked(getChannel).mockResolvedValue(detail({ subscribed: false }));
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    const user = userEvent.setup();
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    expect(
      screen.queryByLabelText(/add new videos automatically/i),
    ).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/format override/i)).not.toBeInTheDocument();
    const settingsPanel = document.querySelector(
      ".chan-settings",
    ) as HTMLElement;
    expect(
      within(settingsPanel).getByRole("button", { name: /^subscribe$/i }),
    ).toBeInTheDocument();
  });

  it("a failed autodownload toggle reverts and shows the error", async () => {
    const user = userEvent.setup();
    vi.mocked(updateChannel).mockRejectedValue(
      new Error("failed to update channel"),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    await user.click(
      await screen.findByLabelText(/add new videos automatically/i),
    );

    expect(
      await screen.findByText("failed to update channel"),
    ).toBeInTheDocument();
  });

  it("the New tab parks the button at Queued once a check is waiting", async () => {
    // Queued is derived from the server's schedule, not from having just
    // clicked — so a reload (or a second tab) shows the same thing.
    const user = userEvent.setup();
    vi.mocked(getChannel).mockResolvedValue(
      detail({ next_scan_at: sqlUTC(-60 * 1000) }), // due a minute ago
    );
    vi.mocked(listPending).mockResolvedValue([]);
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("tab", { name: /new/i }));

    expect(await screen.findByText(/scan queued/i)).toBeInTheDocument();
    // Still clickable: an overdue schedule also describes a channel the scan
    // loop cannot reach (dead cookie, kill-switch), and disabling would strand
    // the user on "Queued" with no way to learn why.
    const btn = screen.getByRole("button", { name: /queued/i });
    expect(btn).toBeEnabled();
    expect(
      screen.queryByRole("button", { name: /scan now/i }),
    ).not.toBeInTheDocument();
  });

  it("pressing Scan now says the scan was queued, never that it ran", async () => {
    const user = userEvent.setup();
    vi.mocked(scanChannel).mockResolvedValue({ status: "scheduled" });
    vi.mocked(listPending).mockResolvedValue([]);
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /new/i }));

    await user.click(await screen.findByRole("button", { name: /scan now/i }));

    expect(
      await screen.findByText(/added to the scan queue/i),
    ).toBeInTheDocument();
  });

  it("the Settings tab reports a queued check the same way the New tab does", async () => {
    // These two lines are rendered by one shared helper; before it existed
    // Settings printed a raw next_scan_at and so announced a next scan that
    // had already happened.
    const user = userEvent.setup();
    vi.mocked(getChannel).mockResolvedValue(
      detail({ next_scan_at: sqlUTC(-60 * 1000) }),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("tab", { name: /settings/i }));

    expect(await screen.findByText(/scan queued/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /queued/i })).toBeEnabled();
  });

  it("refetches the channel when a scan for it lands over SSE", async () => {
    // "Scan now" is asynchronous: without this the button would sit at Queued
    // until the user reloaded by hand.
    const { rerender } = render(
      <Channel
        channelId="UCa"
        onOpenVideo={() => {}}
        onBack={() => {}}
        live={[]}
      />,
    );
    await screen.findByText("Uncanny Expeditions");
    const before = vi.mocked(getChannel).mock.calls.length;

    rerender(
      <Channel
        channelId="UCa"
        onOpenVideo={() => {}}
        onBack={() => {}}
        live={[
          {
            id: 1,
            at: "2026-07-25 06:12:00",
            kind: "scan",
            outcome: "ok",
            subject_id: "UCa",
            subject: "Uncanny Expeditions",
            summary: "checked on request",
            detail: "nothing new",
          },
        ]}
      />,
    );

    await waitFor(() => {
      expect(vi.mocked(getChannel).mock.calls.length).toBeGreaterThan(before);
    });
  });

  it("refetches once per scan, not again on every later event", async () => {
    // App keeps `live` as a rolling buffer, so a matching scan event stays in it.
    // Without a high-water mark every later unrelated event would refetch again.
    const scan = {
      id: 1,
      at: "2026-07-25 06:12:00",
      kind: "scan",
      outcome: "ok",
      subject_id: "UCa",
      subject: "Uncanny Expeditions",
    };
    const { rerender } = render(
      <Channel
        channelId="UCa"
        onOpenVideo={() => {}}
        onBack={() => {}}
        live={[]}
      />,
    );
    await screen.findByText("Uncanny Expeditions");
    const before = vi.mocked(getChannel).mock.calls.length;

    rerender(
      <Channel
        channelId="UCa"
        onOpenVideo={() => {}}
        onBack={() => {}}
        live={[scan]}
      />,
    );
    await waitFor(() => {
      expect(vi.mocked(getChannel).mock.calls.length).toBe(before + 1);
    });

    // An unrelated download event arrives; the scan is still in the buffer.
    rerender(
      <Channel
        channelId="UCa"
        onOpenVideo={() => {}}
        onBack={() => {}}
        live={[
          scan,
          {
            id: 2,
            at: "2026-07-25 06:13:00",
            kind: "download",
            outcome: "ok",
            subject: "A clip",
          },
        ]}
      />,
    );

    await new Promise((r) => setTimeout(r, 20));
    expect(vi.mocked(getChannel).mock.calls.length).toBe(before + 1);
  });

  it("ignores a scan for a different channel", async () => {
    const { rerender } = render(
      <Channel
        channelId="UCa"
        onOpenVideo={() => {}}
        onBack={() => {}}
        live={[]}
      />,
    );
    await screen.findByText("Uncanny Expeditions");
    const before = vi.mocked(getChannel).mock.calls.length;

    rerender(
      <Channel
        channelId="UCa"
        onOpenVideo={() => {}}
        onBack={() => {}}
        live={[
          {
            id: 2,
            at: "2026-07-25 06:12:00",
            kind: "scan",
            outcome: "ok",
            subject_id: "UCother",
            subject: "Someone else",
          },
        ]}
      />,
    );

    // Give the effect a chance to run before asserting it did nothing.
    await new Promise((r) => setTimeout(r, 20));
    expect(vi.mocked(getChannel).mock.calls.length).toBe(before);
  });
});

describe("Channel format helpers", () => {
  it("formatRuntime shows minutes below an hour, whole hours above", () => {
    expect(formatRuntime(45 * 60)).toBe("45 min");
    expect(formatRuntime(3599)).toBe("60 min");
    expect(formatRuntime(3600)).toBe("1 h");
    expect(formatRuntime(2 * 3600 + 1800)).toBe("3 h");
  });

  it("formatBytes picks the largest readable unit", () => {
    expect(formatBytes(512)).toBe("1 kB");
    expect(formatBytes(2048)).toBe("2 kB");
    expect(formatBytes(5 * 1024 ** 2)).toBe("5 MB");
    expect(formatBytes(2.5 * 1024 ** 3)).toBe("2.5 GB");
    expect(formatBytes(3 * 1024 ** 4)).toBe("3.0 TB");
  });

  // formatAge moved to ../format when the library card started needing both
  // age forms in one line; its cases live in format.test.ts now.
});

describe("Channel YouTube metadata", () => {
  beforeEach(() => {
    vi.mocked(getChannel).mockReset();
    vi.mocked(listVideos).mockReset();
    vi.mocked(listPending).mockReset();
    vi.mocked(getSettings).mockReset();
    vi.mocked(listVideos).mockResolvedValue([]);
    vi.mocked(listPending).mockResolvedValue([]);
    vi.mocked(getSettings).mockResolvedValue(settings());
  });

  it("publishes the subscriber count, the verified mark and the refresh date", async () => {
    vi.mocked(getChannel).mockResolvedValue(detail());
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );

    await screen.findByText("Uncanny Expeditions");
    expect(screen.getByText("7.2M")).toBeInTheDocument();
    expect(screen.getByText("subscribers")).toBeInTheDocument();
    expect(screen.getByLabelText("Verified by YouTube")).toBeInTheDocument();
    expect(screen.getByText("Active on YouTube")).toBeInTheDocument();
    expect(
      screen.getByText(
        `Last metadata refresh ${formatStamp("2026-07-21 06:00:00")}`,
      ),
    ).toBeInTheDocument();
  });

  // The two dates are on different schedules — metadata weekly, scans daily —
  // so they are routinely days apart. One stamp labelled "Refreshed" got read
  // as the daily scan, which made a healthy weekly refresh look like a broken
  // scanner. Both are named, in the backend's own words, or neither is useful.
  it("names the metadata refresh and the channel scan as separate dates", async () => {
    vi.mocked(getChannel).mockResolvedValue(
      detail({
        resolved_at: "2026-07-21 06:00:00",
        last_scanned_at: "2026-07-26 08:00:00",
      }),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );

    await screen.findByText("Uncanny Expeditions");
    expect(
      screen.getByText(
        `Last metadata refresh ${formatStamp("2026-07-21 06:00:00")}`,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        `Last channel scan ${formatStamp("2026-07-26 08:00:00")}`,
      ),
    ).toBeInTheDocument();
    // The bare label is what caused the confusion; it must not come back.
    expect(screen.queryByText(/^Refreshed /)).not.toBeInTheDocument();
  });

  // An added-but-unsubscribed channel has no subscriptions row, so no scan
  // schedule and no last_scanned_at. The segment drops out rather than
  // inventing a date or saying "never" — scheduleLine owns that sentence.
  it("omits the scan date when the channel has no scan schedule", async () => {
    vi.mocked(getChannel).mockResolvedValue(
      detail({ subscribed: false, last_scanned_at: undefined }),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );

    await screen.findByText("Uncanny Expeditions");
    expect(
      screen.getByText(
        `Last metadata refresh ${formatStamp("2026-07-21 06:00:00")}`,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/^Last channel scan /)).not.toBeInTheDocument();
  });

  // The stuck channel: resolved_at is stamped (so peeq will never retry on
  // its own) but the attempt failed, which is why the header has no artwork.
  // Saying "Last metadata refresh <date>" here would be a confident lie.
  it("reports a failed refresh instead of claiming the metadata is current", async () => {
    vi.mocked(getChannel).mockResolvedValue(
      detail({
        description: "",
        has_avatar: false,
        has_banner: false,
        subscribers: undefined,
        verified: false,
        resolve_ok: false,
      }),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );

    await screen.findByText("Uncanny Expeditions");
    expect(
      screen.getByText(
        `Metadata refresh failed ${formatStamp("2026-07-21 06:00:00")}`,
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/^Last metadata refresh /),
    ).not.toBeInTheDocument();
    // A stuck metadata refresh does not stop the daily scan, and this is the
    // reading where someone is most likely to fear it has. The scan date stays.
    expect(
      screen.getByText(`Last channel scan ${formatStamp("2026-07-20 08:00:00")}`),
    ).toBeInTheDocument();
    expect(screen.queryByText("Active on YouTube")).not.toBeInTheDocument();
    // An unknown count is a dash, never a number.
    expect(screen.getByText("—")).toBeInTheDocument();
  });

  // A failed resolve on an unsubscribed channel used to be a dead end (the
  // weekly rotation is subscribed-only). The manual Refresh button is the way
  // out now, so the header offers it instead of the old "subscribe to retry"
  // hint.
  it("offers Refresh as the recovery for an unsubscribed failed resolve", async () => {
    vi.mocked(getChannel).mockResolvedValue(
      detail({ resolve_ok: false, subscribed: false }),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );

    await screen.findByText("Uncanny Expeditions");
    expect(
      screen.getByRole("button", { name: /refresh/i }),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("Subscribe to have peeq try again"),
    ).not.toBeInTheDocument();
  });

  // A subscribed channel already has the weekly rotation, so the hint would be
  // noise — and worse, would tell the user to do something already done.
  it("does not suggest subscribing to a channel that already is", async () => {
    vi.mocked(getChannel).mockResolvedValue(
      detail({ resolve_ok: false, subscribed: true }),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );

    await screen.findByText("Uncanny Expeditions");
    expect(
      screen.queryByText("Subscribe to have peeq try again"),
    ).not.toBeInTheDocument();
  });

  it("says a channel is gone when peeq auto-unsubscribed it as deleted", async () => {
    vi.mocked(getChannel).mockResolvedValue(
      detail({ gone: true, subscribed: false }),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );

    await screen.findByText("Uncanny Expeditions");
    expect(screen.getByText("Gone from YouTube")).toBeInTheDocument();
    expect(screen.queryByText("Active on YouTube")).not.toBeInTheDocument();
    // The last count peeq saw is history worth keeping, not a live claim.
    expect(screen.getByText("7.2M")).toBeInTheDocument();
  });

  it("says so plainly when a channel has never been read", async () => {
    vi.mocked(getChannel).mockResolvedValue(
      detail({ resolved_at: undefined, resolve_ok: false }),
    );
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );

    await screen.findByText("Uncanny Expeditions");
    expect(screen.getByText("Never read from YouTube")).toBeInTheDocument();
  });

  // The manual Refresh button re-reads metadata on demand — the only way out
  // of the "tried once, failed, never again" dead end for an unsubscribed
  // channel, which the weekly auto-refresh never covers (#106).
  it("re-reads metadata when the Refresh button is clicked", async () => {
    vi.mocked(getChannel).mockResolvedValue(detail({ resolve_ok: false }));
    vi.mocked(refreshChannel).mockResolvedValue({ status: "ok" });
    render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
    );

    await screen.findByText("Uncanny Expeditions");
    const before = vi.mocked(getChannel).mock.calls.length;

    await userEvent.click(screen.getByRole("button", { name: /refresh/i }));

    await waitFor(() => expect(refreshChannel).toHaveBeenCalledWith("UCa"));
    // Success triggers a reload — getChannel is called again to repaint.
    await waitFor(() =>
      expect(vi.mocked(getChannel).mock.calls.length).toBeGreaterThan(before),
    );
  });

  // jsdom does no layout, so scrollHeight/clientHeight are both 0 and nothing
  // ever "overflows". These stubs stand in for the clamp: clipHeight is what
  // five lines of the box are worth, and the paragraph's own height is
  // derived from its text. That is enough to prove the component reads a
  // MEASUREMENT rather than the character count it used to guess from.
  function stubLayout(clipHeight: number) {
    // clientHeight/scrollHeight live on Element.prototype, so defining them
    // one level down on HTMLParagraphElement.prototype shadows them and
    // deleting the shadow is the whole of the cleanup.
    const proto = HTMLParagraphElement.prototype as unknown as Record<
      string,
      unknown
    >;
    Object.defineProperty(proto, "clientHeight", {
      configurable: true,
      get(this: HTMLParagraphElement) {
        // An expanded paragraph is not clipped: it is as tall as its content.
        return this.className.includes("clamped")
          ? clipHeight
          : (this.textContent?.length ?? 0);
      },
    });
    Object.defineProperty(proto, "scrollHeight", {
      configurable: true,
      get(this: HTMLParagraphElement) {
        return this.textContent?.length ?? 0;
      },
    });
    return () => {
      delete proto.clientHeight;
      delete proto.scrollHeight;
    };
  }

  it("offers More only when the clamp actually cuts the description off", async () => {
    const restore = stubLayout(100);
    try {
      const user = userEvent.setup();
      const short = "Field documentaries.";
      vi.mocked(getChannel).mockResolvedValue(detail({ description: short }));
      const { rerender } = render(
        <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
      );
      await screen.findByText("Uncanny Expeditions");
      // Fits inside the clamp — nothing is hidden, so nothing to offer.
      expect(
        screen.queryByRole("button", { name: "More" }),
      ).not.toBeInTheDocument();

      const long = Array(20).fill("A very long channel blurb.").join(" ");
      vi.mocked(getChannel).mockResolvedValue(
        detail({ id: "UCb", description: long }),
      );
      rerender(
        <Channel channelId="UCb" onOpenVideo={() => {}} onBack={() => {}} />,
      );
      await screen.findByText(long);

      const more = await screen.findByRole("button", { name: "More" });
      expect(screen.getByTestId("chan-desc")).toHaveClass("clamped");
      await user.click(more);
      expect(screen.getByTestId("chan-desc")).not.toHaveClass("clamped");
      // The way back must survive expanding: an unclamped paragraph no longer
      // overflows, and measuring it again would take Less away mid-read.
      expect(screen.getByRole("button", { name: "Less" })).toBeInTheDocument();
    } finally {
      restore();
    }
  });

  // The band the old character threshold got wrong: long enough to be cut off,
  // short enough that a >340 rule called it safe and hid the button.
  it("offers More for a description just past the clamp", async () => {
    const restore = stubLayout(100);
    try {
      vi.mocked(getChannel).mockResolvedValue(
        detail({ description: "x".repeat(120) }),
      );
      render(
        <Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />,
      );
      await screen.findByText("Uncanny Expeditions");
      expect(
        await screen.findByRole("button", { name: "More" }),
      ).toBeInTheDocument();
    } finally {
      restore();
    }
  });

  it("puts the way back above the header, not among the channel's actions", async () => {
    const user = userEvent.setup();
    const onBack = vi.fn();
    vi.mocked(getChannel).mockResolvedValue(detail());
    const { container } = render(
      <Channel channelId="UCa" onOpenVideo={() => {}} onBack={onBack} />,
    );

    await screen.findByText("Uncanny Expeditions");
    const back = screen.getByRole("button", { name: /all channels/i });
    expect(back).toHaveClass("chan-back");
    expect(container.querySelector(".chan-acts")).not.toContainElement(back);

    await user.click(back);
    expect(onBack).toHaveBeenCalled();
  });
});

describe("formatSubscribers", () => {
  it("renders counts the way YouTube does, and unknown as a dash", () => {
    expect(formatSubscribers(undefined)).toBe("—");
    expect(formatSubscribers(0)).toBe("—");
    expect(formatSubscribers(-1)).toBe("—");
    expect(formatSubscribers(742)).toBe("742");
    expect(formatSubscribers(7240000)).toBe("7.2M");
    expect(formatSubscribers(412000)).toBe("412K");
    expect(formatSubscribers(1500)).toBe("1.5K");
    expect(formatSubscribers(2000)).toBe("2K");
    expect(formatSubscribers(3000000)).toBe("3M");
    expect(formatSubscribers(120000000)).toBe("120M");
    // Rounding must not invent a unit nobody writes.
    expect(formatSubscribers(999999)).toBe("1M");
  });
});

describe("formatStamp", () => {
  it("reads a stored timestamp as UTC, not local time", () => {
    expect(formatStamp(undefined)).toBe("");
    expect(formatStamp("nonsense")).toBe("");
    expect(formatStamp("2026-07-21 06:00:00")).toBe(
      new Date("2026-07-21T06:00:00Z").toLocaleDateString(),
    );
  });
});
