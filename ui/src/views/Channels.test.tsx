import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Channels } from "./Channels";
import type { Channel } from "../api/types";
import { DOT } from "../sep";

function baseChannel(overrides: Partial<Channel> = {}): Channel {
  return {
    id: "c1",
    handle: "@addedguy",
    name: "Added Channel",
    subscribed: false,
    autodownload: false,
    format_override: "",
    pending_count: 3,
    downloaded_count: 0,
    added: true,
    dormant: false,
    ...overrides,
  };
}

const notSubscribed = baseChannel();
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
    vi.mocked(listChannels).mockResolvedValue([notSubscribed, subscribed]);
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

  it("lists both not-subscribed and subscribed channels", async () => {
    render(<Channels />);
    expect(await screen.findByText("Added Channel")).toBeInTheDocument();
    expect(screen.getByText("Subbed Channel")).toBeInTheDocument();
  });

  // The counts line reads "@handle · N pending · N downloaded". A channel
  // whose handle YouTube never gave us leads with the counts instead — the
  // separator belongs to the handle, so it has to go when the handle does.
  it("puts the handle at the head of the counts line, or nothing", async () => {
    vi.mocked(listChannels).mockResolvedValue([
      baseChannel({ pending_count: 2, downloaded_count: 2 }),
      baseChannel({ id: "c3", name: "Nameless", handle: "" }),
    ]);
    render(<Channels />);
    const withHandle = (await screen.findByText("Added Channel")).closest(
      ".channel-row",
    ) as HTMLElement;
    expect(withHandle.querySelector(".channel-by")?.textContent).toBe(
      `@addedguy${DOT}2 pending${DOT}2 downloaded`,
    );

    const noHandle = screen
      .getByText("Nameless")
      .closest(".channel-row") as HTMLElement;
    expect(noHandle.querySelector(".channel-by")?.textContent).toBe(
      `3 pending${DOT}0 downloaded`,
    );
  });

  it("clicking a not-subscribed channel's star calls subscribeChannel", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Added Channel");
    const row = screen
      .getByText("Added Channel")
      .closest(".channel-row") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: /^subscribe$/i }));
    await waitFor(() => {
      expect(subscribeChannel).toHaveBeenCalledWith("c1");
    });
  });

  // A "From downloads" channel arrives with whatever name its video row
  // carried — often nothing, until the metadata backlog resolves it. The row's
  // only text is that name, so an empty one renders a zero-width clickable
  // heading and an ⋮ menu with no accessible name.
  it("falls back to the handle, then the id, when a channel has no name", async () => {
    vi.mocked(listChannels).mockResolvedValue([
      baseChannel({ id: "c9", name: "", handle: "@handleonly" }),
      baseChannel({ id: "UCbare", name: "", handle: "" }),
    ]);
    render(<Channels />);

    expect(await screen.findByText("@handleonly")).toBeInTheDocument();
    expect(screen.getByText("UCbare")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Actions for UCbare" }),
    ).toBeInTheDocument();
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
    expect(screen.getByRole("button", { name: /^Subscribed\b/ })).toHaveClass(
      "on",
    );
  });

  // Each chip carries the number of channels it would show. They are counted off
  // the unfiltered list — the active chip's own list holds one slice and could
  // never speak for the other four — using the same predicates the server's
  // ?filter= clauses apply.
  describe("chip counts", () => {
    const roster = [
      // added + subscribed + autodownload on
      baseChannel({
        id: "c1",
        name: "Auto One",
        subscribed: true,
        autodownload: true,
      }),
      // added + subscribed, autodownload off
      baseChannel({ id: "c2", name: "Subbed Two", subscribed: true }),
      // added, never subscribed
      baseChannel({ id: "c3", name: "Added Three" }),
      // never added — in the list only because a download came from it
      baseChannel({ id: "c4", name: "Download Four", added: false }),
    ];
    const countFor = (label: string) =>
      Array.from(document.querySelectorAll(".chips .chip"))
        .find((c) => c.textContent?.startsWith(label))
        ?.querySelector(".n")?.textContent;

    it("counts each filter the way the server defines it", async () => {
      vi.mocked(listChannels).mockResolvedValue(roster);
      render(<Channels />);
      await screen.findByText("Auto One");

      await waitFor(() => expect(countFor("All")).toBe("4"));
      expect(countFor("Subscribed")).toBe("2");
      // added && !subscribed — the download-only row has no subscription either,
      // and must not land here.
      expect(countFor("Not subscribed")).toBe("1");
      expect(countFor("From downloads")).toBe("1");
      expect(countFor("Auto-add")).toBe("1");
    });

    it("narrows every count to the search query", async () => {
      vi.mocked(listChannels).mockResolvedValue(roster);
      render(<Channels search="four" />);
      await screen.findByText("Download Four");

      await waitFor(() => expect(countFor("All")).toBe("1"));
      expect(countFor("Subscribed")).toBe("0");
      expect(countFor("From downloads")).toBe("1");
    });
  });

  it("filter chips drive listChannels(filter)", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Added Channel");
    vi.mocked(listChannels).mockClear();

    await user.click(screen.getByRole("button", { name: /^All\b/ }));
    await waitFor(() => expect(listChannels).toHaveBeenCalledWith("all"));

    // "Not subscribed" is the label; the filter id is "notsubscribed".
    vi.mocked(listChannels).mockClear();
    await user.click(screen.getByRole("button", { name: /^Not subscribed\b/ }));
    await waitFor(() =>
      expect(listChannels).toHaveBeenCalledWith("notsubscribed"),
    );

    // "From downloads" — channels in the list only because the library holds
    // a video downloaded from them. It sits next to "Not subscribed" (added,
    // but not followed), which is a different thing entirely, so the
    // label-to-filter mapping is worth pinning.
    vi.mocked(listChannels).mockClear();
    await user.click(screen.getByRole("button", { name: /^From downloads\b/ }));
    await waitFor(() =>
      expect(listChannels).toHaveBeenCalledWith("downloaded"),
    );

    vi.mocked(listChannels).mockClear();
    await user.click(screen.getByRole("button", { name: /^Auto-add\b/ }));
    await waitFor(() =>
      expect(listChannels).toHaveBeenCalledWith("autodownload"),
    );

    vi.mocked(listChannels).mockClear();
    await user.click(screen.getByRole("button", { name: /^Subscribed\b/ }));
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
          releaseSubscribed = () => resolve([notSubscribed, subscribed]);
        });
      }
      return Promise.resolve([notSubscribed]);
    });

    render(<Channels />);
    // The initial "subscribed" load is still in flight; switch to
    // "Not subscribed".
    await user.click(screen.getByRole("button", { name: /^Not subscribed\b/ }));
    expect(await screen.findByText("Added Channel")).toBeInTheDocument();
    expect(screen.queryByText("Subbed Channel")).not.toBeInTheDocument();

    // Now let the abandoned "subscribed" request resolve — it must be ignored.
    releaseSubscribed?.();
    await waitFor(() =>
      expect(listChannels).toHaveBeenCalledWith("notsubscribed"),
    );
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

    await user.click(screen.getByRole("button", { name: /^All\b/ }));
    expect(await screen.findByText("No channels yet.")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^Auto-add\b/ }));
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

      const addedRow = screen
        .getByText("Added Channel")
        .closest(".channel-row") as HTMLElement;
      expect(
        within(addedRow).queryByRole("img", { name: "Auto-add is on" }),
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
        notSubscribed,
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

      await user.click(screen.getByRole("button", { name: /^All\b/ }));
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
      await screen.findByText("Added Channel");
      const row = screen
        .getByText("Added Channel")
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
    await screen.findByText("Added Channel");
    expect(screen.getByText("Subbed Channel")).toBeInTheDocument();

    // "subbed" matches the Subbed Channel (name + @subbedguy handle) but not
    // the Added Channel.
    rerender(<Channels search="subbed" />);
    expect(screen.getByText("Subbed Channel")).toBeInTheDocument();
    expect(screen.queryByText("Added Channel")).not.toBeInTheDocument();

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
    await screen.findByText("Added Channel");
    const row = screen
      .getByText("Added Channel")
      .closest(".channel-row") as HTMLElement;
    await openRowMenu(user, row);
    await user.click(
      within(row).getByRole("menuitem", { name: /delete channel/i }),
    );
    // The dialog must name the channel, its video count, that "kept forever"
    // videos go too, and that it cannot be undone — the same four things the
    // channel page's own delete says.
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent("Added Channel");
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
    await screen.findByText("Added Channel");

    await user.click(screen.getByRole("button", { name: "Added Channel" }));

    expect(onOpenChannel).toHaveBeenCalledWith("c1");
  });

  it("cancelling the delete dialog does not call deleteChannel", async () => {
    const user = userEvent.setup();
    render(<Channels />);
    await screen.findByText("Added Channel");
    const row = screen
      .getByText("Added Channel")
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
    await screen.findByText("Added Channel");
    const row = screen
      .getByText("Added Channel")
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
      vi.mocked(listChannels).mockResolvedValue([
        notSubscribed,
        subscribed,
        dormant,
      ]);
      render(<Channels />);
      expect(
        await screen.findByText("1 channel needs review"),
      ).toBeInTheDocument();
      expect(screen.getByText("Quiet Channel")).toBeInTheDocument();
    });

    it("hides the band entirely when nothing is dormant", async () => {
      render(<Channels />);
      await screen.findByText("Added Channel");
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
      vi.mocked(listChannels).mockResolvedValue([
        notSubscribed,
        subscribed,
        dormant,
      ]);
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
      await screen.findByText("Added Channel");

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
      vi.mocked(listChannels).mockResolvedValue([
        notSubscribed,
        subscribed,
        revived,
      ]);

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

  describe("the toolbar", () => {
    // rowNames reads the rendered order of the channel list. Ordering is the
    // whole point of these tests, so they assert on the sequence, never on
    // mere presence.
    function rowNames(): string[] {
      // The name is the <h3>, not the .chan-link inside it — that button only
      // exists when onOpenChannel is wired, which these renders do not do.
      return Array.from(document.querySelectorAll(".channel-row h3"))
        .map((el) => el.textContent?.trim() ?? "")
        .filter(Boolean);
    }

    const alpha = baseChannel({
      id: "a",
      name: "Alpha",
      handle: "@alpha",
      subscribed: true,
      downloaded_count: 5,
      pending_count: 1,
      last_video_at: "2026-07-01 00:00:00",
      first_seen_at: "2026-01-01 00:00:00",
    });
    const mid = baseChannel({
      id: "m",
      name: "Mango",
      handle: "@mango",
      subscribed: true,
      downloaded_count: 40,
      pending_count: 9,
      last_video_at: "2026-07-20 00:00:00",
      first_seen_at: "2026-06-01 00:00:00",
    });
    // Zulu, not "Zeta": the search test filters on "a", and this row has to be
    // the one that does not match.
    const zulu = baseChannel({
      id: "z",
      name: "Zulu",
      handle: "@zulu",
      subscribed: true,
      downloaded_count: 1,
      pending_count: 0,
      last_video_at: "2026-07-10 00:00:00",
      first_seen_at: "2026-03-01 00:00:00",
    });

    beforeEach(() => {
      // Deliberately not already in name order: a passing A–Z assertion has to
      // come from the component's own sort, not from the fixture's order.
      vi.mocked(listChannels).mockResolvedValue([mid, zulu, alpha]);
    });

    it("defaults to name A–Z, matching the order the API has always returned", async () => {
      render(<Channels />);
      await screen.findByText("Alpha");
      expect(rowNames()).toEqual(["Alpha", "Mango", "Zulu"]);
    });

    it.each([
      ["Newest video", ["Mango", "Zulu", "Alpha"]],
      ["Name Z–A", ["Zulu", "Mango", "Alpha"]],
      ["Most videos", ["Mango", "Alpha", "Zulu"]],
      ["Recently added", ["Mango", "Zulu", "Alpha"]],
      ["Most pending", ["Mango", "Alpha", "Zulu"]],
    ])("reorders the list for %s", async (label, expected) => {
      const user = userEvent.setup();
      render(<Channels />);
      await screen.findByText("Alpha");

      await user.selectOptions(
        screen.getByRole("combobox", { name: "Sort" }),
        label,
      );

      expect(rowNames()).toEqual(expected);
    });

    it("sorts what the search box left, not the whole list", async () => {
      const user = userEvent.setup();
      // "a" matches Alpha and Mango (by name) but not Zulu.
      render(<Channels search="a" />);
      await screen.findByText("Alpha");

      await user.selectOptions(
        screen.getByRole("combobox", { name: "Sort" }),
        "Most videos",
      );

      expect(rowNames()).toEqual(["Mango", "Alpha"]);
    });

    it("reports typing in the search box to its owner", async () => {
      const user = userEvent.setup();
      const onSearchChange = vi.fn();
      render(<Channels onSearchChange={onSearchChange} />);
      await screen.findByText("Alpha");

      await user.type(
        screen.getByRole("searchbox", { name: "Search channels" }),
        "z",
      );

      expect(onSearchChange).toHaveBeenCalledWith("z");
    });

    it("focuses the search box when / is pressed", async () => {
      const user = userEvent.setup();
      render(<Channels />);
      await screen.findByText("Alpha");
      const box = screen.getByRole("searchbox", { name: "Search channels" });
      expect(box).not.toHaveFocus();

      await user.keyboard("/");

      expect(box).toHaveFocus();
    });

    it("leaves / alone while another field has focus", async () => {
      const user = userEvent.setup();
      render(
        <>
          <input aria-label="Elsewhere" />
          <Channels />
        </>,
      );
      await screen.findByText("Alpha");
      const elsewhere = screen.getByRole("textbox", { name: "Elsewhere" });
      await user.click(elsewhere);

      await user.keyboard("/");

      // The slash is typed where the user was, and focus never moves.
      expect(elsewhere).toHaveFocus();
      expect(elsewhere).toHaveValue("/");
    });

    it("leaves / alone while a row menu is open", async () => {
      const user = userEvent.setup();
      render(<Channels />);
      await screen.findByText("Alpha");
      const row = screen
        .getByText("Alpha")
        .closest(".channel-row") as HTMLElement;

      await openRowMenu(user, row);
      // RowMenu focuses its first menuitem — a BUTTON, which the shortcut's
      // "already typing somewhere?" tag check waves through.
      const firstItem = within(row).getAllByRole("menuitem")[0];
      expect(firstItem).toHaveFocus();

      await user.keyboard("/");

      expect(firstItem).toHaveFocus();
      expect(
        screen.getByRole("searchbox", { name: "Search channels" }),
      ).not.toHaveFocus();
    });

    it("leaves / alone while the delete confirmation is open", async () => {
      const user = userEvent.setup();
      render(<Channels />);
      await screen.findByText("Alpha");
      const row = screen
        .getByText("Alpha")
        .closest(".channel-row") as HTMLElement;
      await openRowMenu(user, row);
      await user.click(
        within(row).getByRole("menuitem", { name: /delete channel/i }),
      );
      await screen.findByRole("dialog");
      // The dialog puts focus on its Cancel BUTTON, which the shortcut's
      // "is the user already typing somewhere?" check waves through — so
      // without a modal guard the slash would pull focus to a search box
      // hidden behind the scrim and leave the open dialog unfocused.
      const cancel = screen.getByRole("button", { name: "Cancel" });
      expect(cancel).toHaveFocus();

      await user.keyboard("/");

      expect(cancel).toHaveFocus();
      expect(
        screen.getByRole("searchbox", { name: "Search channels" }),
      ).not.toHaveFocus();
    });
  });
});
