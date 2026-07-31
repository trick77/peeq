import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { NewTab } from "./NewTab";
import type { ChannelDetail } from "../../api/types";

vi.mock("../../api/pending", () => ({
  listPending: vi.fn().mockResolvedValue([]),
  downloadPending: vi.fn(),
  ignorePending: vi.fn(),
}));
vi.mock("../../api/channels", () => ({
  scanChannel: vi.fn(),
}));

import { listPending } from "../../api/pending";
import { DOT } from "../../sep";

function makeDetail(overrides: Partial<ChannelDetail> = {}): ChannelDetail {
  return {
    id: "UC1",
    name: "Veritasium",
    has_avatar: false,
    has_banner: false,
    verified: false,
    resolve_ok: true,
    gone: false,
    added: true,
    archived_count: 0,
    runtime_seconds: 0,
    disk_bytes: 0,
    subscribed: true,
    auto_summary: true,
    keep_reads: false,
    autodownload: false,
    pending_count: 0,
    ...overrides,
  };
}

describe("NewTab", () => {
  beforeEach(() => {
    vi.mocked(listPending).mockReset();
    vi.mocked(listPending).mockResolvedValue([]);
  });

  it("offers Scan now when the channel is subscribed", async () => {
    render(
      <NewTab detail={makeDetail({ subscribed: true })} onChanged={() => {}} />,
    );
    expect(
      await screen.findByRole("button", { name: /scan now/i }),
    ).toBeInTheDocument();
  });

  it("hides Scan now and prompts to subscribe when tracked but not subscribed", async () => {
    // A tracked-but-unsubscribed channel still shows the New tab, but the scan
    // endpoint 400s without a subscription, so Scan now must not be offered.
    render(
      <NewTab
        detail={makeDetail({ subscribed: false })}
        onChanged={() => {}}
      />,
    );
    expect(
      await screen.findByText(/subscribe to this channel/i),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /scan now/i })).toBeNull();
  });

  it("shows the publish date beside the duration, and omits it when unknown", async () => {
    // The row list carries the same date as the Inbox card, just on the sub
    // line; an undated row must degrade to duration alone, never to a
    // stand-in built from discovered_at.
    const daysAgoISO = (n: number) =>
      new Date(Date.now() - n * 86400000).toISOString().slice(0, 10);
    vi.mocked(listPending).mockResolvedValue([
      {
        video_id: "p1",
        channel_id: "UCa",
        channel_name: "Chan",
        title: "Dated upload",
        duration_seconds: 125,
        url: "https://youtu.be/p1",
        thumbnail_url: "https://img.example/p1.jpg",
        published_at: daysAgoISO(3),
        discovered_at: "2026-07-24 08:00:00",
        summary_status: "",
        auto_summary: false,
        has_subtitles: false,
      },
      {
        video_id: "p2",
        channel_id: "UCa",
        channel_name: "Chan",
        title: "Undated upload",
        duration_seconds: 60,
        url: "https://youtu.be/p2",
        thumbnail_url: "https://img.example/p2.jpg",
        discovered_at: "2026-07-24 08:00:00",
        summary_status: "",
        auto_summary: false,
        has_subtitles: false,
      },
    ]);
    render(<NewTab detail={makeDetail()} onChanged={() => {}} />);
    await screen.findByText("Dated upload");

    const rowFor = (title: string) =>
      screen.getByText(title).closest(".chan-prow") as HTMLElement;
    expect(rowFor("Dated upload").querySelector(".sub")?.textContent).toBe(
      `2:05${DOT}3 days ago`,
    );
    expect(rowFor("Undated upload").querySelector(".sub")?.textContent).toBe(
      "1:00",
    );
  });

  it("links each row title to the video on YouTube, in a new tab", async () => {
    vi.mocked(listPending).mockResolvedValue([
      {
        video_id: "p1",
        channel_id: "UCa",
        channel_name: "Chan",
        title: "Linked upload",
        duration_seconds: 125,
        url: "https://youtu.be/p1",
        thumbnail_url: "https://img.example/p1.jpg",
        discovered_at: "2026-07-24 08:00:00",
        summary_status: "",
        auto_summary: false,
        has_subtitles: false,
      },
      // No url on the ledger row — the link is built from the video id.
      {
        video_id: "p2",
        channel_id: "UCa",
        channel_name: "Chan",
        title: "Urlless upload",
        duration_seconds: 60,
        url: "",
        thumbnail_url: "https://img.example/p2.jpg",
        discovered_at: "2026-07-24 08:00:00",
        summary_status: "",
        auto_summary: false,
        has_subtitles: false,
      },
    ]);
    render(<NewTab detail={makeDetail()} onChanged={() => {}} />);

    const linked = await screen.findByRole("link", { name: "Linked upload" });
    expect(linked).toHaveAttribute("href", "https://youtu.be/p1");
    expect(linked).toHaveAttribute("target", "_blank");
    expect(linked).toHaveAttribute("rel", "noopener noreferrer");
    expect(
      screen.getByRole("link", { name: "Urlless upload" }),
    ).toHaveAttribute("href", "https://www.youtube.com/watch?v=p2");
  });
});
