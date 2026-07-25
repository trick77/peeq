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
  listChannels,
  subscribeChannel,
  unsubscribeChannel,
  deleteChannel,
  listAutoUnsubscribedChannels,
  dismissDormantChannel,
  resubscribeChannel,
  scanChannel,
} from "../api/channels";

// openRowMenu clicks a row's ⋮ trigger so its actions (Open, Delete) become
// clickable: subscribe/unsubscribe now lives on the inline star, but the rest
// of the per-row actions are folded into a single popover menu.
async function openRowMenu(
  user: ReturnType<typeof userEvent.setup>,
  row: HTMLElement,
) {
  await user.click(within(row).getByRole("button", { name: /actions for/i }));
}

describe("Channels", () => {
  beforeEach(() => {
    vi.mocked(listChannels).mockReset();
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

  it("clicking a tracked channel's star calls subscribeChannel", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Tracked Channel");
    const row = screen
      .getByText("Tracked Channel")
      .closest(".channel-row") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /^subscribe$/i }));
    await waitFor(() => {
      expect(subscribeChannel).toHaveBeenCalledWith("c1");
    });
  });

  it("clicking a subscribed channel's star calls unsubscribeChannel", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Subbed Channel");
    const row = screen
      .getByText("Subbed Channel")
      .closest(".channel-row") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /unsubscribe/i }));
    await waitFor(() => {
      expect(unsubscribeChannel).toHaveBeenCalledWith("c2");
    });
  });

  // The page opens on Subscribed, not All: it is about the channels you follow,
  // and defaulting to All led with channels the user never subscribed to.
  it("loads the subscribed filter on mount", async () => {
    render(<Channels />);
    await waitFor(() =>
      expect(listChannels).toHaveBeenCalledWith("subscribed"),
    );
    expect(screen.getByRole("button", { name: "Subscribed" })).toHaveClass(
      "on",
    );
  });

  it("filter chips drive listChannels(filter)", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Tracked Channel");
    vi.mocked(listChannels).mockClear();

    await user.click(screen.getByRole("button", { name: "All" }));
    await waitFor(() => expect(listChannels).toHaveBeenCalledWith("all"));

    // "Not subscribed" is the label; the filter id stays "tracked".
    vi.mocked(listChannels).mockClear();
    await user.click(screen.getByRole("button", { name: "Not subscribed" }));
    await waitFor(() => expect(listChannels).toHaveBeenCalledWith("tracked"));

    vi.mocked(listChannels).mockClear();
    await user.click(screen.getByRole("button", { name: "Auto-add" }));
    await waitFor(() =>
      expect(listChannels).toHaveBeenCalledWith("autodownload"),
    );

    vi.mocked(listChannels).mockClear();
    await user.click(screen.getByRole("button", { name: "Subscribed" }));
    await waitFor(() =>
      expect(listChannels).toHaveBeenCalledWith("subscribed"),
    );
  });

  // A slow response for an abandoned filter must not overwrite the list the
  // active chip asked for. Without the sequence guard the stale "subscribed"
  // response (the mount default) resolves last and wins, showing every channel
  // under "Not subscribed".
  it("a stale filter response does not overwrite the active filter's list", async () => {
    const user = userEvent.setup();
    let releaseSubscribed: (() => void) | undefined;
    vi.mocked(listChannels).mockImplementation((f) => {
      if (f === "subscribed") {
        return new Promise((resolve) => {
          releaseSubscribed = () => resolve([tracked, subscribed]);
        });
      }
      return Promise.resolve([tracked]);
    });

    render(<Channels />);
    // The initial "subscribed" load is still in flight; switch to
    // "Not subscribed".
    await user.click(screen.getByRole("button", { name: "Not subscribed" }));
    expect(await screen.findByText("Tracked Channel")).toBeInTheDocument();
    expect(screen.queryByText("Subbed Channel")).not.toBeInTheDocument();

    // Now let the abandoned "subscribed" request resolve — it must be ignored.
    releaseSubscribed?.();
    await waitFor(() => expect(listChannels).toHaveBeenCalledWith("tracked"));
    expect(screen.queryByText("Subbed Channel")).not.toBeInTheDocument();
  });

  // Every empty state but "All"'s points at the All chip: a filtered view can
  // be blank while channels sit one chip away, and "No channels yet." there
  // would repeat the very impression the All-by-default bug created.
  it("shows the filter-aware empty state", async () => {
    const user = userEvent.setup();
    vi.mocked(listChannels).mockResolvedValue([]);
    render(<Channels />);
    expect(
      await screen.findByText("No subscribed channels — see All."),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "All" }));
    expect(await screen.findByText("No channels yet.")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Auto-add" }));
    expect(
      await screen.findByText("No channels match this filter — see All."),
    ).toBeInTheDocument();
  });

  // Auto-add is configured on the channel's own Settings tab, so the list must
  // say which channels download by themselves — otherwise the only way to tell
  // is to click the Auto-add chip.
  describe("the auto-add marker", () => {
    it("marks a channel with autodownload on, and only that one", async () => {
      render(<Channels />);
      await screen.findByText("Subbed Channel");

      const subbedRow = screen
        .getByText("Subbed Channel")
        .closest(".channel-row") as HTMLElement;
      expect(
        within(subbedRow).getByRole("img", { name: "Auto-add is on" }),
      ).toBeInTheDocument();

      const trackedRow = screen
        .getByText("Tracked Channel")
        .closest(".channel-row") as HTMLElement;
      expect(
        within(trackedRow).queryByRole("img", { name: "Auto-add is on" }),
      ).not.toBeInTheDocument();
    });
  });

  describe("Check now", () => {
    beforeEach(() => {
      vi.mocked(scanChannel).mockReset();
      vi.mocked(scanChannel).mockResolvedValue({ status: "scheduled" });
    });

    it("schedules a check and reports it", async () => {
      const user = userEvent.setup();
      render(<Channels />);
      await screen.findByText("Subbed Channel");
      const row = screen
        .getByText("Subbed Channel")
        .closest(".channel-row") as HTMLElement;

      await openRowMenu(user, row);
      await user.click(screen.getByRole("menuitem", { name: /check now/i }));

      await waitFor(() => expect(scanChannel).toHaveBeenCalledWith("c2"));
      expect(
        await screen.findByText(
          "Added to the check queue — usually done within a minute.",
        ),
      ).toBeInTheDocument();
    });

    it("reports the backend's reason when the check is blocked", async () => {
      const user = userEvent.setup();
      vi.mocked(scanChannel).mockResolvedValue({
        status: "blocked",
        reason: "YouTube access is paused.",
      });
      render(<Channels />);
      await screen.findByText("Subbed Channel");
      const row = screen
        .getByText("Subbed Channel")
        .closest(".channel-row") as HTMLElement;

      await openRowMenu(user, row);
      await user.click(screen.getByRole("menuitem", { name: /check now/i }));

      expect(
        await screen.findByText("YouTube access is paused."),
      ).toBeInTheDocument();
    });

    // The banner never names a channel, so one row's answer must not still be
    // up while the next row's request is in flight — it would read as that
    // row's answer.
    it("drops the previous row's answer while the next check is in flight", async () => {
      const user = userEvent.setup();
      const secondSubbed = baseChannel({
        id: "c3",
        handle: "@alsosubbed",
        name: "Also Subbed",
        subscribed: true,
      });
      vi.mocked(listChannels).mockResolvedValue([
        tracked,
        subscribed,
        secondSubbed,
      ]);

      let releaseSecond: (() => void) | undefined;
      vi.mocked(scanChannel).mockImplementation((id) =>
        id === "c3"
          ? new Promise((resolve) => {
              releaseSecond = () => resolve({ status: "scheduled" });
            })
          : Promise.resolve({ status: "scheduled" }),
      );

      render(<Channels />);
      await screen.findByText("Also Subbed");

      const first = screen
        .getByText("Subbed Channel")
        .closest(".channel-row") as HTMLElement;
      await openRowMenu(user, first);
      await user.click(screen.getByRole("menuitem", { name: /check now/i }));
      const scheduled =
        "Added to the check queue — usually done within a minute.";
      await screen.findByText(scheduled);

      const second = screen
        .getByText("Also Subbed")
        .closest(".channel-row") as HTMLElement;
      await openRowMenu(user, second);
      await user.click(screen.getByRole("menuitem", { name: /check now/i }));

      // c3's request has not resolved yet — the banner must be blank, not
      // still showing the first row's answer.
      await waitFor(() =>
        expect(screen.queryByText(scheduled)).not.toBeInTheDocument(),
      );
      releaseSecond?.();
      expect(await screen.findByText(scheduled)).toBeInTheDocument();
    });

    // Deleting the channel a notice is about leaves it promising a check for a
    // row that no longer exists.
    it("clears the notice when the checked channel is deleted", async () => {
      const user = userEvent.setup();
      render(<Channels />);
      await screen.findByText("Subbed Channel");
      const row = screen
        .getByText("Subbed Channel")
        .closest(".channel-row") as HTMLElement;

      await openRowMenu(user, row);
      await user.click(screen.getByRole("menuitem", { name: /check now/i }));
      const scheduled =
        "Added to the check queue — usually done within a minute.";
      await screen.findByText(scheduled);

      await openRowMenu(user, row);
      await user.click(
        screen.getByRole("menuitem", { name: /delete channel/i }),
      );
      const dialog = await screen.findByRole("dialog");
      await user.click(
        within(dialog).getByRole("button", { name: /delete channel/i }),
      );

      await waitFor(() => expect(deleteChannel).toHaveBeenCalledWith("c2"));
      await waitFor(() =>
        expect(screen.queryByText(scheduled)).not.toBeInTheDocument(),
      );
    });

    // A blocked answer with no reason still has to say something: the backend
    // omits `reason` for some blocks, and a silent menu click reads as dead.
    it("falls back to generic wording when a block carries no reason", async () => {
      const user = userEvent.setup();
      vi.mocked(scanChannel).mockResolvedValue({ status: "blocked" });
      render(<Channels />);
      await screen.findByText("Subbed Channel");
      const row = screen
        .getByText("Subbed Channel")
        .closest(".channel-row") as HTMLElement;

      await openRowMenu(user, row);
      await user.click(screen.getByRole("menuitem", { name: /check now/i }));

      expect(
        await screen.findByText("peeq cannot check this channel right now."),
      ).toBeInTheDocument();
    });

    it("shows an error line when the request fails", async () => {
      const user = userEvent.setup();
      vi.mocked(scanChannel).mockRejectedValue(
        new Error("failed to schedule a check"),
      );
      render(<Channels />);
      await screen.findByText("Subbed Channel");
      const row = screen
        .getByText("Subbed Channel")
        .closest(".channel-row") as HTMLElement;

      await openRowMenu(user, row);
      await user.click(screen.getByRole("menuitem", { name: /check now/i }));

      expect(
        await screen.findByText("failed to schedule a check"),
      ).toBeInTheDocument();
    });

    // A notice reports on the row that was clicked, so it must not survive a
    // chip click that may not even show that row.
    it("clears the notice when the filter changes", async () => {
      const user = userEvent.setup();
      render(<Channels />);
      await screen.findByText("Subbed Channel");
      const row = screen
        .getByText("Subbed Channel")
        .closest(".channel-row") as HTMLElement;

      await openRowMenu(user, row);
      await user.click(screen.getByRole("menuitem", { name: /check now/i }));
      await screen.findByText(
        "Added to the check queue — usually done within a minute.",
      );

      await user.click(screen.getByRole("button", { name: "All" }));
      await waitFor(() =>
        expect(
          screen.queryByText(
            "Added to the check queue — usually done within a minute.",
          ),
        ).not.toBeInTheDocument(),
      );
    });

    // The endpoint answers 400 "channel is not subscribed", so the entry must
    // not be offered — the same reason the channel page hides its own button.
    it("is not offered for a channel that is not subscribed", async () => {
      const user = userEvent.setup();
      render(<Channels />);
      await screen.findByText("Tracked Channel");
      const row = screen
        .getByText("Tracked Channel")
        .closest(".channel-row") as HTMLElement;

      await openRowMenu(user, row);
      expect(
        screen.queryByRole("menuitem", { name: /check now/i }),
      ).not.toBeInTheDocument();
      expect(
        screen.getByRole("menuitem", { name: /delete channel/i }),
      ).toBeInTheDocument();
    });
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

  // A failed delete must close the modal, or its error line (rendered at the
  // top of the page) sits hidden behind the fixed scrim and the click looks
  // dead — the same reason the detail Settings delete closes on error.
  it("a failed delete closes the dialog and shows the error", async () => {
    const user = userEvent.setup();
    vi.mocked(deleteChannel).mockRejectedValue(new Error("nope, still busy"));
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
    await user.click(
      within(dialog).getByRole("button", { name: /delete channel/i }),
    );
    expect(await screen.findByText("nope, still busy")).toBeInTheDocument();
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

    // When every channel is dormant the main list is empty, but they show in
    // the review band — so the page must NOT claim "No channels yet."
    it("stays silent about emptiness when the only channels are dormant", async () => {
      vi.mocked(listChannels).mockResolvedValue([dormant]);
      render(<Channels />);
      await screen.findByText("1 channel needs review");
      expect(screen.queryByText(/no channels/i)).not.toBeInTheDocument();
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
