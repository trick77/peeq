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
  return document.querySelector(
    ".channel-add button[type=submit]",
  ) as HTMLElement;
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
  getChannel: vi.fn(),
  scanChannel: vi.fn(),
  channelAvatarUrl: (id: string) => `/api/channels/${id}/avatar`,
  channelBannerUrl: (id: string) => `/api/channels/${id}/banner`,
  listAutoUnsubscribedChannels: vi.fn(),
  dismissDormantChannel: vi.fn(),
  resubscribeChannel: vi.fn(),
}));

import {
  addChannel,
  listChannels,
  subscribeChannel,
  unsubscribeChannel,
  deleteChannel,
  listAutoUnsubscribedChannels,
  dismissDormantChannel,
  resubscribeChannel,
} from "../api/channels";

// openRowMenu clicks a row's ⋮ trigger so its actions (Subscribe, Delete…)
// become clickable: the per-row controls were folded into a single popover
// menu, so nothing is inline anymore.
async function openRowMenu(
  user: ReturnType<typeof userEvent.setup>,
  row: HTMLElement,
) {
  await user.click(within(row).getByRole("button", { name: /actions for/i }));
}

describe("Channels", () => {
  beforeEach(() => {
    vi.mocked(listChannels).mockReset();
    vi.mocked(addChannel).mockReset();
    vi.mocked(addChannel).mockResolvedValue({
      id: "c3",
      name: "New Channel",
      subscribed: false,
    });
    vi.mocked(subscribeChannel).mockReset();
    vi.mocked(unsubscribeChannel).mockReset();
    vi.mocked(deleteChannel).mockReset();
    vi.mocked(listChannels).mockResolvedValue([tracked, subscribed]);
    vi.mocked(subscribeChannel).mockResolvedValue({ status: "subscribed" });
    vi.mocked(unsubscribeChannel).mockResolvedValue({ status: "unsubscribed" });
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

  it("the tracked channel's menu Subscribe calls subscribeChannel", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Tracked Channel");
    const row = screen
      .getByText("Tracked Channel")
      .closest(".channel-row") as HTMLElement;
    await openRowMenu(user, row);
    await user.click(
      within(row).getByRole("menuitem", { name: /^subscribe$/i }),
    );
    await waitFor(() => {
      expect(subscribeChannel).toHaveBeenCalledWith("c1");
    });
  });

  it("the subscribed channel's menu Unsubscribe calls unsubscribeChannel", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Subbed Channel");
    const row = screen
      .getByText("Subbed Channel")
      .closest(".channel-row") as HTMLElement;
    await openRowMenu(user, row);
    await user.click(
      within(row).getByRole("menuitem", { name: /unsubscribe/i }),
    );
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
    await waitFor(() =>
      expect(listChannels).toHaveBeenCalledWith("subscribed"),
    );

    vi.mocked(listChannels).mockClear();
    await user.click(screen.getByRole("button", { name: "Tracked" }));
    await waitFor(() => expect(listChannels).toHaveBeenCalledWith("tracked"));

    vi.mocked(listChannels).mockClear();
    await user.click(screen.getByRole("button", { name: "Auto-add" }));
    await waitFor(() =>
      expect(listChannels).toHaveBeenCalledWith("autodownload"),
    );

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

    await user.type(
      screen.getByLabelText("Channel URL"),
      "https://www.youtube.com/@new",
    );
    await user.click(addFormButton());

    await waitFor(() => {
      expect(addChannel).toHaveBeenCalledWith(
        "https://www.youtube.com/@new",
        false,
      );
    });
  });

  it("ticking Subscribe adds the channel subscribed", async () => {
    const user = userEvent.setup();
    vi.mocked(addChannel).mockResolvedValue({
      id: "c3",
      name: "New Channel",
      subscribed: true,
    });
    render(<Channels />);
    await screen.findByText("Tracked Channel");

    await user.type(
      screen.getByLabelText("Channel URL"),
      "https://www.youtube.com/@new",
    );
    await user.click(screen.getByLabelText("Subscribe"));
    expect(addFormButton()).toHaveTextContent("Subscribe");
    await user.click(addFormButton());

    await waitFor(() => {
      expect(addChannel).toHaveBeenCalledWith(
        "https://www.youtube.com/@new",
        true,
      );
    });
    expect(
      await screen.findByText(/Subscribed to New Channel/),
    ).toBeInTheDocument();
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

    await user.type(
      screen.getByLabelText("Channel URL"),
      "https://www.youtube.com/@new",
    );
    await user.click(addFormButton());

    expect(await screen.findByText(/Tracked New Channel/)).toBeInTheDocument();
  });

  it("rejects a non-channel URL before calling the API", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Tracked Channel");

    await user.type(
      screen.getByLabelText("Channel URL"),
      "https://www.youtube.com/watch?v=abc12345678",
    );
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
    expect(
      await screen.findByText("No channels match this filter."),
    ).toBeInTheDocument();
  });

  it("the search box filters the list by name or handle", async () => {
    const { rerender } = render(<Channels search="" />);
    await screen.findByText("Tracked Channel");
    expect(screen.getByText("Subbed Channel")).toBeInTheDocument();

    // "subbed" matches the Subbed Channel (name + @subbedguy handle) but not
    // the Tracked Channel.
    rerender(<Channels search="subbed" />);
    expect(screen.getByText("Subbed Channel")).toBeInTheDocument();
    expect(screen.queryByText("Tracked Channel")).not.toBeInTheDocument();

    // A query that matches nothing shows the search-specific empty state, not
    // the "No channels yet." one (channels do exist — the query hid them).
    rerender(<Channels search="zzzzz" />);
    expect(
      screen.getByText("No channels match your search."),
    ).toBeInTheDocument();
  });

  it("delete opens a confirm dialog, then calls deleteChannel", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Tracked Channel");
    const row = screen
      .getByText("Tracked Channel")
      .closest(".channel-row") as HTMLElement;
    await openRowMenu(user, row);
    await user.click(
      within(row).getByRole("menuitem", { name: /delete channel/i }),
    );
    // The dialog must name the channel, its video count, that "kept forever"
    // videos go too, and that it cannot be undone — the same four things the
    // channel page's own delete says.
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Tracked Channel");
    expect(dialog).toHaveTextContent("0 videos");
    expect(dialog).toHaveTextContent(/including any you kept forever/);
    expect(dialog).toHaveTextContent(/cannot be undone/);
    await user.click(
      within(dialog).getByRole("button", { name: /delete channel/i }),
    );
    await waitFor(() => {
      expect(deleteChannel).toHaveBeenCalledWith("c1");
    });
  });

  it("clicking a channel's name opens its page", async () => {
    const user = userEvent.setup();
    const onOpenChannel = vi.fn();
    render(<Channels onOpenChannel={onOpenChannel} />);
    await screen.findByText("Tracked Channel");

    await user.click(screen.getByRole("button", { name: "Tracked Channel" }));

    expect(onOpenChannel).toHaveBeenCalledWith("c1");
  });

  it("cancelling the delete dialog does not call deleteChannel", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Tracked Channel");
    const row = screen
      .getByText("Tracked Channel")
      .closest(".channel-row") as HTMLElement;
    await openRowMenu(user, row);
    await user.click(
      within(row).getByRole("menuitem", { name: /delete channel/i }),
    );
    const dialog = await screen.findByRole("dialog");
    await user.click(within(dialog).getByRole("button", { name: /cancel/i }));
    expect(deleteChannel).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
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
      expect(
        await screen.findByText("1 channel needs review"),
      ).toBeInTheDocument();
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
      const row = screen
        .getByText("Quiet Channel")
        .closest(".channel-row") as HTMLElement;
      await user.click(
        within(row).getByRole("button", { name: /keep subscribed/i }),
      );

      await waitFor(() =>
        expect(dismissDormantChannel).toHaveBeenCalledWith("c4"),
      );
      // The band itself disappears (nothing left to review) — the channel
      // moves into the main list rather than vanishing from the page.
      await waitFor(() =>
        expect(screen.queryByText(/needs? review/)).not.toBeInTheDocument(),
      );
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
      const row = screen
        .getByText("Vanished Channel")
        .closest(".tomb-row") as HTMLElement;
      expect(
        within(row).getByRole("button", { name: /re-subscribe/i }),
      ).toBeInTheDocument();
    });

    it("states that archived videos are kept on an auto-unsubscribed row", async () => {
      vi.mocked(listAutoUnsubscribedChannels).mockResolvedValue([tombstone]);
      render(<Channels />);
      expect(
        await screen.findByText(/kept — nothing was deleted/i),
      ).toBeInTheDocument();
    });

    it("re-subscribing moves the channel back into the main list", async () => {
      const user = userEvent.setup();
      vi.mocked(listAutoUnsubscribedChannels).mockResolvedValue([tombstone]);
      render(<Channels />);
      await screen.findByText("Vanished Channel");

      const revived = baseChannel({
        id: "c5",
        handle: "@vanished",
        name: "Vanished Channel",
        subscribed: true,
      });
      vi.mocked(listChannels).mockResolvedValue([tracked, subscribed, revived]);

      const row = screen
        .getByText("Vanished Channel")
        .closest(".tomb-row") as HTMLElement;
      await user.click(
        within(row).getByRole("button", { name: /re-subscribe/i }),
      );

      await waitFor(() =>
        expect(resubscribeChannel).toHaveBeenCalledWith("c5"),
      );
      await waitFor(() =>
        expect(
          screen.queryByText(/deleted on YouTube/i),
        ).not.toBeInTheDocument(),
      );
      expect(await screen.findByText("Vanished Channel")).toBeInTheDocument();
      const revivedRow = screen
        .getByText("Vanished Channel")
        .closest(".channel-row") as HTMLElement;
      expect(revivedRow.className).toContain("sect");
    });
  });
});
