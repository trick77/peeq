import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Channels } from "./Channels";
import type { Channel } from "../api/types";

function baseChannel(overrides: Partial<Channel> = {}): Channel {
  return {
    id: "c1",
    handle: "@trackedguy",
    name: "Tracked Channel",
    subscribed: false,
    autodownload: false,
    format_override: "",
    pending_count: 3,
    downloaded_count: 0,
    ...overrides,
  };
}

const tracked = baseChannel();
const subscribed = baseChannel({
  id: "c2",
  handle: "@subbedguy",
  name: "Subbed Channel",
  subscribed: true,
  autodownload: true,
  downloaded_count: 12,
  pending_count: 0,
});

vi.mock("../api/channels", () => ({
  listChannels: vi.fn(),
  addChannel: vi.fn(),
  updateChannel: vi.fn(),
  subscribeChannel: vi.fn(),
  unsubscribeChannel: vi.fn(),
  deleteChannel: vi.fn(),
}));

import {
  listChannels,
  updateChannel,
  subscribeChannel,
  unsubscribeChannel,
  deleteChannel,
} from "../api/channels";

describe("Channels", () => {
  beforeEach(() => {
    vi.mocked(listChannels).mockReset();
    vi.mocked(updateChannel).mockReset();
    vi.mocked(subscribeChannel).mockReset();
    vi.mocked(unsubscribeChannel).mockReset();
    vi.mocked(deleteChannel).mockReset();
    vi.mocked(listChannels).mockResolvedValue([tracked, subscribed]);
    vi.mocked(subscribeChannel).mockResolvedValue({ status: "subscribed" });
    vi.mocked(unsubscribeChannel).mockResolvedValue({ status: "unsubscribed" });
    vi.mocked(updateChannel).mockResolvedValue({ id: "c1", autodownload: true, format_override: "" });
    vi.mocked(deleteChannel).mockResolvedValue(undefined);
  });

  it("lists both tracked and subscribed channels", async () => {
    render(<Channels />);
    expect(await screen.findByText("Tracked Channel")).toBeInTheDocument();
    expect(screen.getByText("Subbed Channel")).toBeInTheDocument();
  });

  it("clicking the tracked channel's Subscribe button calls subscribeChannel", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Tracked Channel");
    const row = screen.getByText("Tracked Channel").closest(".channel-row") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /subscribe/i }));
    await waitFor(() => {
      expect(subscribeChannel).toHaveBeenCalledWith("c1");
    });
  });

  it("clicking the subscribed channel's Unsubscribe button calls unsubscribeChannel", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Subbed Channel");
    const row = screen.getByText("Subbed Channel").closest(".channel-row") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /unsubscribe/i }));
    await waitFor(() => {
      expect(unsubscribeChannel).toHaveBeenCalledWith("c2");
    });
  });

  it("filter chips drive listChannels(filter)", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Tracked Channel");
    vi.mocked(listChannels).mockClear();

    await user.click(screen.getByRole("button", { name: "Subscribed" }));
    await waitFor(() => expect(listChannels).toHaveBeenCalledWith("subscribed"));

    vi.mocked(listChannels).mockClear();
    await user.click(screen.getByRole("button", { name: "Tracked" }));
    await waitFor(() => expect(listChannels).toHaveBeenCalledWith("tracked"));

    vi.mocked(listChannels).mockClear();
    await user.click(screen.getByRole("button", { name: "All" }));
    await waitFor(() => expect(listChannels).toHaveBeenCalledWith("all"));
  });

  it("toggling autodownload calls updateChannel", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Tracked Channel");
    const row = screen.getByText("Tracked Channel").closest(".channel-row") as HTMLElement;
    await user.click(within(row).getByLabelText(/autodownload/i));
    await waitFor(() => {
      expect(updateChannel).toHaveBeenCalledWith("c1", { autodownload: true });
    });
  });

  it("editing the format override field and blurring calls updateChannel", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Tracked Channel");
    const row = screen.getByText("Tracked Channel").closest(".channel-row") as HTMLElement;
    const input = within(row).getByLabelText(/format override/i);
    await user.type(input, "bestvideo+bestaudio/best");
    await user.tab();
    await waitFor(() => {
      expect(updateChannel).toHaveBeenCalledWith("c1", { format_override: "bestvideo+bestaudio/best" });
    });
  });

  it("delete requires confirm, then calls deleteChannel", async () => {
    const user = userEvent.setup();
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    render(<Channels />);
    await screen.findByText("Tracked Channel");
    const row = screen.getByText("Tracked Channel").closest(".channel-row") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /delete/i }));
    expect(confirmSpy).toHaveBeenCalledWith("Delete this channel and ALL its downloaded videos?");
    await waitFor(() => {
      expect(deleteChannel).toHaveBeenCalledWith("c1");
    });
    confirmSpy.mockRestore();
  });

  it("does not call deleteChannel when confirm is cancelled", async () => {
    const user = userEvent.setup();
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    render(<Channels />);
    await screen.findByText("Tracked Channel");
    const row = screen.getByText("Tracked Channel").closest(".channel-row") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /delete/i }));
    expect(deleteChannel).not.toHaveBeenCalled();
    confirmSpy.mockRestore();
  });
});
