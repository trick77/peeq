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
    dormant: false,
    ...overrides,
  };
}

// addFormButton scopes to the add form's submit button: its label is
// "Track"/"Subscribe" depending on the checkbox, and "Subscribe" would
// otherwise collide with the per-row subscribe buttons.
function addFormButton(): HTMLElement {
  return document.querySelector(".channel-add button[type=submit]") as HTMLElement;
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
  listAutoUnsubscribedChannels: vi.fn(),
  dismissDormantChannel: vi.fn(),
  resubscribeChannel: vi.fn(),
}));

import {
  addChannel,
  listChannels,
  updateChannel,
  subscribeChannel,
  unsubscribeChannel,
  deleteChannel,
  listAutoUnsubscribedChannels,
  dismissDormantChannel,
  resubscribeChannel,
} from "../api/channels";

describe("Channels", () => {
  beforeEach(() => {
    vi.mocked(listChannels).mockReset();
    vi.mocked(addChannel).mockReset();
    vi.mocked(addChannel).mockResolvedValue({ id: "c3", name: "New Channel", subscribed: false });
    vi.mocked(updateChannel).mockReset();
    vi.mocked(subscribeChannel).mockReset();
    vi.mocked(unsubscribeChannel).mockReset();
    vi.mocked(deleteChannel).mockReset();
    vi.mocked(listChannels).mockResolvedValue([tracked, subscribed]);
    vi.mocked(subscribeChannel).mockResolvedValue({ status: "subscribed" });
    vi.mocked(unsubscribeChannel).mockResolvedValue({ status: "unsubscribed" });
    vi.mocked(updateChannel).mockResolvedValue({ id: "c1", autodownload: true, format_override: "" });
    vi.mocked(deleteChannel).mockResolvedValue(undefined);
    vi.mocked(listAutoUnsubscribedChannels).mockReset();
    vi.mocked(listAutoUnsubscribedChannels).mockResolvedValue([]);
    vi.mocked(dismissDormantChannel).mockReset();
    vi.mocked(dismissDormantChannel).mockResolvedValue({ status: "dismissed" });
    vi.mocked(resubscribeChannel).mockReset();
    vi.mocked(resubscribeChannel).mockResolvedValue({ status: "subscribed" });
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
    await user.click(screen.getByRole("button", { name: "Auto-add" }));
    await waitFor(() => expect(listChannels).toHaveBeenCalledWith("autodownload"));

    vi.mocked(listChannels).mockClear();
    await user.click(screen.getByRole("button", { name: "All" }));
    await waitFor(() => expect(listChannels).toHaveBeenCalledWith("all"));
  });

  // A slow response for an abandoned filter must not overwrite the list the
  // active chip asked for. Without the sequence guard the stale "all"
  // response resolves last and wins, showing every channel under "Tracked".
  it("a stale filter response does not overwrite the active filter's list", async () => {
    const user = userEvent.setup();
    let releaseAll: (() => void) | undefined;
    vi.mocked(listChannels).mockImplementation((f) => {
      if (f === "all") {
        return new Promise((resolve) => {
          releaseAll = () => resolve([tracked, subscribed]);
        });
      }
      return Promise.resolve([tracked]);
    });

    render(<Channels />);
    // The initial "all" load is still in flight; switch to "Tracked".
    await user.click(screen.getByRole("button", { name: "Tracked" }));
    expect(await screen.findByText("Tracked Channel")).toBeInTheDocument();
    expect(screen.queryByText("Subbed Channel")).not.toBeInTheDocument();

    // Now let the abandoned "all" request resolve — it must be ignored.
    releaseAll?.();
    await waitFor(() => expect(listChannels).toHaveBeenCalledWith("tracked"));
    expect(screen.queryByText("Subbed Channel")).not.toBeInTheDocument();
  });

  it("the add form tracks without subscribing by default", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Tracked Channel");

    await user.type(screen.getByLabelText("Channel URL"), "https://www.youtube.com/@new");
    await user.click(addFormButton());

    await waitFor(() => {
      expect(addChannel).toHaveBeenCalledWith("https://www.youtube.com/@new", false);
    });
  });

  it("ticking Subscribe adds the channel subscribed", async () => {
    const user = userEvent.setup();
    vi.mocked(addChannel).mockResolvedValue({ id: "c3", name: "New Channel", subscribed: true });
    render(<Channels />);
    await screen.findByText("Tracked Channel");

    await user.type(screen.getByLabelText("Channel URL"), "https://www.youtube.com/@new");
    await user.click(screen.getByLabelText("Subscribe"));
    expect(addFormButton()).toHaveTextContent("Subscribe");
    await user.click(addFormButton());

    await waitFor(() => {
      expect(addChannel).toHaveBeenCalledWith("https://www.youtube.com/@new", true);
    });
    expect(await screen.findByText(/Subscribed to New Channel/)).toBeInTheDocument();
  });

  // The confirmation must not depend on the new row showing up: under a
  // non-"all" chip a freshly tracked channel usually does not match the
  // active filter, so the list does not visibly change and a silent success
  // would read as a failure.
  it("confirms the add even when the active filter excludes the new channel", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Tracked Channel");

    await user.click(screen.getByRole("button", { name: "Subscribed" }));
    vi.mocked(listChannels).mockResolvedValue([]);

    await user.type(screen.getByLabelText("Channel URL"), "https://www.youtube.com/@new");
    await user.click(addFormButton());

    expect(await screen.findByText(/Tracked New Channel/)).toBeInTheDocument();
  });

  it("rejects a non-channel URL before calling the API", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Tracked Channel");

    await user.type(screen.getByLabelText("Channel URL"), "https://www.youtube.com/watch?v=abc12345678");
    await user.click(addFormButton());

    expect(await screen.findByText(/Paste a channel link/)).toBeInTheDocument();
    expect(addChannel).not.toHaveBeenCalled();
  });

  it("shows the filter-aware empty state", async () => {
    const user = userEvent.setup();
    vi.mocked(listChannels).mockResolvedValue([]);
    render(<Channels />);
    expect(await screen.findByText("No channels yet.")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Auto-add" }));
    expect(await screen.findByText("No channels match this filter.")).toBeInTheDocument();
  });

  it("toggling autodownload calls updateChannel", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Tracked Channel");
    const row = screen.getByText("Tracked Channel").closest(".channel-row") as HTMLElement;
    await user.click(within(row).getByLabelText("Auto-add"));
    await waitFor(() => {
      expect(updateChannel).toHaveBeenCalledWith("c1", { autodownload: true });
    });
    // Refetch, so a row that no longer matches the active filter disappears
    // and a 0-row no-op on an unsubscribed channel can't leave a stale tick.
    // 3, not 2: mount fires both the filtered load and the review band's own
    // filter="all" dormant load (see Channels.tsx's loadDormant), then the
    // toggle fires one more.
    await waitFor(() => expect(listChannels).toHaveBeenCalledTimes(3));
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

  describe("dormant review band", () => {
    const dormant = baseChannel({
      id: "c4",
      handle: "@quietguy",
      name: "Quiet Channel",
      subscribed: true,
      dormant: true,
      last_video_at: "2020-01-01 00:00:00",
      downloaded_count: 142,
    });

    it("shows the review band with a count when channels are dormant", async () => {
      vi.mocked(listChannels).mockResolvedValue([tracked, subscribed, dormant]);
      render(<Channels />);
      expect(await screen.findByText("1 channel needs review")).toBeInTheDocument();
      expect(screen.getByText("Quiet Channel")).toBeInTheDocument();
    });

    it("hides the band entirely when nothing is dormant", async () => {
      render(<Channels />);
      await screen.findByText("Tracked Channel");
      expect(screen.queryByText(/needs? review/)).not.toBeInTheDocument();
    });

    it("dismissing a dormant channel removes it from the band", async () => {
      const user = userEvent.setup();
      vi.mocked(listChannels).mockResolvedValue([tracked, subscribed, dormant]);
      render(<Channels />);
      await screen.findByText("1 channel needs review");
      const row = screen.getByText("Quiet Channel").closest(".channel-row") as HTMLElement;
      await user.click(within(row).getByRole("button", { name: /keep subscribed/i }));

      await waitFor(() => expect(dismissDormantChannel).toHaveBeenCalledWith("c4"));
      // The band itself disappears (nothing left to review) — the channel
      // moves into the main list rather than vanishing from the page.
      await waitFor(() => expect(screen.queryByText(/needs? review/)).not.toBeInTheDocument());
      expect(screen.getByText("Quiet Channel").closest(".band")).toBeNull();
    });
  });

  describe("auto-unsubscribed section", () => {
    const tombstone = {
      id: "c5",
      handle: "@vanished",
      name: "Vanished Channel",
      reason: "deleted",
      at: "2026-07-12 09:00:00",
    };

    it("lists auto-unsubscribed channels with the reason and a re-subscribe button", async () => {
      vi.mocked(listAutoUnsubscribedChannels).mockResolvedValue([tombstone]);
      render(<Channels />);
      await screen.findByText("Tracked Channel");

      expect(await screen.findByText("Vanished Channel")).toBeInTheDocument();
      expect(screen.getByText(/deleted on YouTube/i)).toBeInTheDocument();
      const row = screen.getByText("Vanished Channel").closest(".tomb-row") as HTMLElement;
      expect(within(row).getByRole("button", { name: /re-subscribe/i })).toBeInTheDocument();
    });

    it("states that archived videos are kept on an auto-unsubscribed row", async () => {
      vi.mocked(listAutoUnsubscribedChannels).mockResolvedValue([tombstone]);
      render(<Channels />);
      expect(await screen.findByText(/kept — nothing was deleted/i)).toBeInTheDocument();
    });

    it("re-subscribing moves the channel back into the main list", async () => {
      const user = userEvent.setup();
      vi.mocked(listAutoUnsubscribedChannels).mockResolvedValue([tombstone]);
      render(<Channels />);
      await screen.findByText("Vanished Channel");

      const revived = baseChannel({ id: "c5", handle: "@vanished", name: "Vanished Channel", subscribed: true });
      vi.mocked(listChannels).mockResolvedValue([tracked, subscribed, revived]);

      const row = screen.getByText("Vanished Channel").closest(".tomb-row") as HTMLElement;
      await user.click(within(row).getByRole("button", { name: /re-subscribe/i }));

      await waitFor(() => expect(resubscribeChannel).toHaveBeenCalledWith("c5"));
      await waitFor(() => expect(screen.queryByText(/deleted on YouTube/i)).not.toBeInTheDocument());
      expect(await screen.findByText("Vanished Channel")).toBeInTheDocument();
      const revivedRow = screen.getByText("Vanished Channel").closest(".channel-row") as HTMLElement;
      expect(revivedRow.className).toContain("sect");
    });
  });
});
