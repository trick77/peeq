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

function makeDetail(overrides: Partial<ChannelDetail> = {}): ChannelDetail {
  return {
    id: "UC1",
    name: "Veritasium",
    has_avatar: false,
    has_banner: false,
    tracked: true,
    archived_count: 0,
    runtime_seconds: 0,
    disk_bytes: 0,
    subscribed: true,
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

  it("offers Check now when the channel is subscribed", async () => {
    render(
      <NewTab detail={makeDetail({ subscribed: true })} onChanged={() => {}} />,
    );
    expect(
      await screen.findByRole("button", { name: /check now/i }),
    ).toBeInTheDocument();
  });

  it("hides Check now and prompts to subscribe when tracked but not subscribed", async () => {
    // A tracked-but-unsubscribed channel still shows the New tab, but the scan
    // endpoint 400s without a subscription, so Check now must not be offered.
    render(
      <NewTab
        detail={makeDetail({ subscribed: false })}
        onChanged={() => {}}
      />,
    );
    expect(
      await screen.findByText(/subscribe to this channel/i),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /check now/i })).toBeNull();
  });
});
