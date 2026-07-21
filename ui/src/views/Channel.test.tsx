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
vi.mock("../api/videos", () => ({ listVideos: vi.fn(), thumbnailUrl: (id: string) => `/t/${id}` }));
vi.mock("../api/pending", () => ({ listPending: vi.fn(), downloadPending: vi.fn(), ignorePending: vi.fn() }));

import { getChannel } from "../api/channels";
import { listVideos } from "../api/videos";
import { listPending } from "../api/pending";

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
});
