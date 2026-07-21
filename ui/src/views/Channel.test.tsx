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
  channelAvatarUrl: (id: string) => `/api/channels/${id}/avatar`,
  channelBannerUrl: (id: string) => `/api/channels/${id}/banner`,
}));
vi.mock("../api/videos", () => ({
  listVideos: vi.fn(),
  thumbnailUrl: (id: string) => `/t/${id}`,
  setFavorite: vi.fn(),
  setWatched: vi.fn(),
}));
vi.mock("../api/pending", () => ({ listPending: vi.fn(), downloadPending: vi.fn(), ignorePending: vi.fn() }));

import { getChannel, scanChannel, updateChannel, deleteChannel } from "../api/channels";
import { listVideos, setFavorite } from "../api/videos";
import { listPending } from "../api/pending";
import type { Video } from "../api/types";

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

function detail(overrides: Partial<ChannelDetail> = {}): ChannelDetail {
  return {
    id: "UCa",
    name: "Uncanny Expeditions",
    handle: "@UncannyExpeditions",
    description: "Field documentaries.",
    has_avatar: true,
    has_banner: true,
    tracked: true,
    tracked_at: "2026-03-14 09:00:00",
    archived_count: 142,
    runtime_seconds: 219600,
    disk_bytes: 40802189312,
    newest_published_at: "2026-07-18T00:00:00Z",
    subscribed: true,
    autodownload: true,
    format_override: "",
    last_scanned_at: "2026-07-20 08:00:00",
    next_scan_at: "2026-07-20 14:00:00",
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
    vi.mocked(updateChannel).mockReset();
    vi.mocked(updateChannel).mockResolvedValue({ id: "UCa", autodownload: false, format_override: "" });
    vi.mocked(deleteChannel).mockReset();
    vi.mocked(deleteChannel).mockResolvedValue(undefined);
  });

  it("shows the channel name and its four stats", async () => {
    const { container } = render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");
    const stats = container.querySelector(".chan-stats") as HTMLElement;
    expect(within(stats).getByText("142")).toBeInTheDocument();
    expect(within(stats).getByText(/61 h/)).toBeInTheDocument();
    expect(within(stats).getByText(/38(\.\d+)? GB/)).toBeInTheDocument();
  });

  it("the Archive tab shows its count badge", async () => {
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");
    const archiveTab = screen.getByRole("tab", { name: /archive/i });
    expect(within(archiveTab).getByText("142")).toBeInTheDocument();
  });

  it("an untracked channel hides the New and Settings tabs", async () => {
    vi.mocked(getChannel).mockResolvedValue(
      detail({ tracked: false, subscribed: false, pending_count: 0 }),
    );
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");

    expect(screen.getByRole("tab", { name: /archive/i })).toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: /new/i })).toBeNull();
    expect(screen.queryByRole("tab", { name: /settings/i })).toBeNull();
    expect(screen.getByRole("button", { name: /track this channel/i })).toBeInTheDocument();
  });

  it("switching to the New tab loads that channel's pending videos", async () => {
    const user = userEvent.setup();
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("tab", { name: /new/i }));

    await waitFor(() => {
      expect(listPending).toHaveBeenCalledWith("UCa");
    });
  });

  it("the archive tab loads only this channel's videos", async () => {
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith(expect.objectContaining({ channel: "UCa" }));
    });
  });

  it("the New tab's empty state says when the next check is due", async () => {
    const user = userEvent.setup();
    vi.mocked(listPending).mockResolvedValue([]);
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");

    await user.click(screen.getByRole("tab", { name: /new/i }));

    expect(await screen.findByText(/nothing new/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /check now/i })).toBeInTheDocument();
  });

  it("a blocked scan shows the reason rather than reporting success", async () => {
    const user = userEvent.setup();
    vi.mocked(scanChannel).mockResolvedValue({
      status: "blocked",
      reason: "Your YouTube cookie needs refreshing before peeq can check this channel.",
    });
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /new/i }));

    await user.click(await screen.findByRole("button", { name: /check now/i }));

    expect(await screen.findByText(/cookie needs refreshing/i)).toBeInTheDocument();
  });

  it("clicking a card's favorite button on the Archive tab calls setFavorite and updates optimistically", async () => {
    const user = userEvent.setup();
    vi.mocked(listVideos).mockResolvedValue([archiveVideo({ id: "v1", favorite: false })]);

    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");
    await screen.findByText("A Test Video");

    const favoriteButton = screen.getByRole("button", { name: "Add to favorites" });
    await user.click(favoriteButton);

    await waitFor(() => {
      expect(setFavorite).toHaveBeenCalledWith("v1", true);
    });
    expect(await screen.findByRole("button", { name: "Remove from favorites" })).toBeInTheDocument();
  });

  it("the delete button names how many videos it will destroy", async () => {
    const user = userEvent.setup();
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    expect(await screen.findByRole("button", { name: /delete channel and its 142 videos/i })).toBeInTheDocument();
  });

  it("toggling auto-add saves it", async () => {
    const user = userEvent.setup();
    render(<Channel channelId="UCa" onOpenVideo={() => {}} onBack={() => {}} />);
    await screen.findByText("Uncanny Expeditions");
    await user.click(screen.getByRole("tab", { name: /settings/i }));

    await user.click(await screen.findByLabelText(/add new videos automatically/i));

    await waitFor(() => {
      expect(updateChannel).toHaveBeenCalledWith("UCa", { autodownload: false });
    });
  });
});
