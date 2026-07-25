import { useEffect, useRef, useState } from "react";
import { Icon } from "../icons";
import {
  listChannels,
  subscribeChannel,
  unsubscribeChannel,
  deleteChannel,
  listAutoUnsubscribedChannels,
  dismissDormantChannel,
  resubscribeChannel,
  scanChannel,
  channelAvatarUrl,
  channelBannerUrl,
  type ChannelFilter,
} from "../api/channels";
import { gradientClassFor } from "../format";
import type { AutoUnsubscribedChannel, Channel } from "../api/types";
import { RowMenu } from "../components/RowMenu";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { ChannelDeleteWarning } from "../components/ChannelDeleteWarning";
import { ReviewBand } from "./ReviewBand";
import { AutoUnsubscribedSection } from "./AutoUnsubscribedSection";

// Subscribed leads and is the default: the page is about the channels you
// follow, and opening on "All" put channels you never subscribed to first.
const CHIPS: { id: ChannelFilter; label: string }[] = [
  { id: "subscribed", label: "Subscribed" },
  { id: "all", label: "All" },
  // The filter id stays "tracked" — it is the API's ?filter= value, and the
  // store clause is `s.channel_id IS NULL`, i.e. added but NOT subscribed.
  // "Tracked" read as though it should include subscribed channels too, so
  // only the label says what the filter actually does.
  { id: "tracked", label: "Not subscribed" },
  // The filter id stays "autodownload" — it is the API's ?filter= value and
  // the subscriptions column name. Only the label is user-facing.
  { id: "autodownload", label: "Auto-add" },
];

// Channels — added/subscribed channel management: filter chips and a top-bar
// search box, with each row showing the channel's banner-and-avatar art, its
// counts, an auto-add marker when autodownload is on, a clickable subscription
// star, and a ⋮ actions menu (Open, Check now, Delete).
// Adding a channel lives on the Add page; auto-add and the format override live
// on the channel's own Settings tab, not here. Mirrors Library's chip/search
// pattern for visual consistency.
export function Channels({
  onOpenChannel,
  search = "",
}: {
  // onOpenChannel — optional: wired by App (Task 11), rendered as channel
  // name links in Task 15.
  onOpenChannel?: (id: string) => void;
  // search — the top bar's query for this page (the Channels list now shares
  // Library's top-bar search box). Filters the list by name/handle, client-
  // side, since the whole list is already in memory.
  search?: string;
} = {}) {
  const [filter, setFilter] = useState<ChannelFilter>("subscribed");
  const [channels, setChannels] = useState<Channel[]>([]);
  const [error, setError] = useState<string | null>(null);
  // notice carries the outcome of the ⋮ menu's "Check now" — an inline banner
  // under the chips, matching the channel tabs' own scan feedback. There is no
  // shared toast component in the app.
  const [notice, setNotice] = useState<string | null>(null);
  // pendingDelete holds the channel the ⋮ menu's Delete opened the confirm
  // dialog for; deleteBusy disables the dialog while the request is in flight.
  const [pendingDelete, setPendingDelete] = useState<Channel | null>(null);
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [tombstones, setTombstones] = useState<AutoUnsubscribedChannel[]>([]);
  // dormant is deliberately its own list, fetched with filter "all" rather
  // than derived from `channels`. `channels` follows the active chip (e.g.
  // "Auto-add"), so a dormant channel with autodownload off would silently
  // drop out of the review band the moment that chip is selected — the one
  // alert on the page that should never depend on which chip is lit.
  const [dormant, setDormant] = useState<Channel[]>([]);

  // filterRef mirrors filter so the async handlers below can refetch the
  // filter that is active NOW. Reading `filter` after an await would use the
  // value captured when the handler was created: toggle a row, switch chips
  // while the request is in flight, and the resumed handler would overwrite
  // the list with the old filter's channels while the new chip stays lit.
  const filterRef = useRef(filter);
  filterRef.current = filter;

  // loadSeq drops out-of-order responses. Two listChannels calls can be in
  // flight at once (rapid chip clicks, or a chip click racing a toggle's
  // refetch); without this the slower one wins whichever chip is active.
  const loadSeq = useRef(0);

  function load(f: ChannelFilter) {
    setError(null);
    const seq = ++loadSeq.current;
    listChannels(f)
      .then((cs) => {
        if (seq !== loadSeq.current) return; // a newer load superseded this one
        setChannels(cs);
      })
      .catch((e: Error) => {
        if (seq !== loadSeq.current) return;
        setError(e.message);
      });
  }

  useEffect(() => {
    // Drop any "Check now" notice: it names a channel that may not even be in
    // the list the new chip is about to show.
    setNotice(null);
    load(filter);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filter]);

  // The auto-unsubscribed list does not depend on the filter chips (it is
  // its own, separate surface), so it loads once on mount rather than
  // re-fetching every time the chip selection changes.
  useEffect(() => {
    listAutoUnsubscribedChannels()
      .then(setTombstones)
      .catch((e: Error) => setError(e.message));
  }, []);

  // loadDormant refreshes the review band's own list. Called on mount and
  // again after anything that could add or remove a dormant channel
  // (dismiss, unsubscribe, resubscribe) — never as a side effect of the
  // filter chips.
  function loadDormant() {
    listChannels("all")
      .then((cs) => setDormant(cs.filter((c) => c.dormant)))
      .catch((e: Error) => setError(e.message));
  }

  useEffect(() => {
    loadDormant();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function applyLocalUpdate(id: string, patch: Partial<Channel>) {
    setChannels((prev) =>
      prev.map((c) => (c.id === id ? { ...c, ...patch } : c)),
    );
  }

  async function handleToggleSubscribe(c: Channel) {
    const next = !c.subscribed;
    applyLocalUpdate(c.id, { subscribed: next });
    // A dormant channel unsubscribed from the review band itself must leave
    // the band immediately — it lives in `dormant`, a separate list from
    // `channels`, so applyLocalUpdate above does not touch it.
    if (!next) setDormant((prev) => prev.filter((x) => x.id !== c.id));
    try {
      if (next) {
        await subscribeChannel(c.id);
      } else {
        await unsubscribeChannel(c.id);
      }
      load(filterRef.current);
      // Unsubscribing (from the main list or the review band itself) always
      // clears dormancy — a channel can only be dormant while subscribed.
      loadDormant();
    } catch (err) {
      applyLocalUpdate(c.id, { subscribed: c.subscribed });
      if (!next) loadDormant();
      setError((err as Error).message);
    }
  }

  // handleScan is the ⋮ menu's "Check now" — the same action the channel's own
  // New and Settings tabs offer, so a check no longer means opening the channel
  // first. Wording is kept identical to those two on purpose: three surfaces
  // reporting the same backend outcome must not describe it differently.
  async function handleScan(c: Channel) {
    setError(null);
    try {
      const res = await scanChannel(c.id);
      setNotice(
        res.status === "blocked"
          ? (res.reason ?? "peeq cannot check this channel right now.")
          : "Checking soon — peeq will look for new videos on its next pass.",
      );
    } catch (err) {
      setNotice(null);
      setError((err as Error).message);
    }
  }

  // Delete is a two-step: the ⋮ menu's Delete opens the confirm dialog
  // (setPendingDelete), and the dialog's Delete button runs it (confirmDelete).
  // Auto-add and the format override are no longer on the row — they live on
  // the channel's own Settings tab now.
  async function confirmDelete() {
    const c = pendingDelete;
    if (!c) return;
    setDeleteBusy(true);
    try {
      await deleteChannel(c.id);
      setChannels((prev) => prev.filter((x) => x.id !== c.id));
      setPendingDelete(null);
    } catch (err) {
      // Close the dialog on failure, the same as the detail Settings delete:
      // the error line renders at the top of the page, and the fixed, dimmed
      // modal scrim would otherwise hide it — leaving the click looking dead.
      setPendingDelete(null);
      setError((err as Error).message);
    } finally {
      setDeleteBusy(false);
    }
  }

  // handleKeepDormant dismisses one channel's dormancy flag (the "Keep
  // subscribed" action in the review band). Applied optimistically against
  // the band's own `dormant` list — the row leaves the band immediately —
  // and reverted with an error line on failure. Also patches `channels` in
  // case the same channel happens to be visible under the active filter.
  async function handleKeepDormant(c: Channel) {
    setDormant((prev) => prev.filter((x) => x.id !== c.id));
    applyLocalUpdate(c.id, { dormant: false });
    try {
      await dismissDormantChannel(c.id);
    } catch (err) {
      setDormant((prev) =>
        prev.some((x) => x.id === c.id) ? prev : [...prev, c],
      );
      applyLocalUpdate(c.id, { dormant: true });
      setError((err as Error).message);
    }
  }

  function handleKeepAllDormant() {
    for (const c of dormant) {
      handleKeepDormant(c);
    }
  }

  // handleResubscribe restores a channel peeq auto-unsubscribed: the record
  // drops out of the tombstone list and a full channel reload brings the
  // channel back into the main list under whatever filter is active.
  async function handleResubscribe(c: AutoUnsubscribedChannel) {
    try {
      await resubscribeChannel(c.id);
      setTombstones((prev) => prev.filter((x) => x.id !== c.id));
      load(filterRef.current);
      loadDormant();
    } catch (err) {
      setError((err as Error).message);
    }
  }

  // The rendered list: never the dormant channels (they live in the review
  // band), and — when the top-bar search box has a query — only those whose
  // name or handle matches it. Filtering is client-side: the whole list is
  // already loaded, so there is no request to make per keystroke.
  const q = search.trim().toLowerCase();
  const visibleChannels = channels.filter(
    (c) =>
      !c.dormant &&
      (q === "" ||
        c.name.toLowerCase().includes(q) ||
        (c.handle ?? "").toLowerCase().includes(q)),
  );
  const hasNonDormant = channels.some((c) => !c.dormant);

  return (
    <>
      <div className="chips">
        {CHIPS.map((chip) => (
          <button
            key={chip.id}
            type="button"
            className={`chip${filter === chip.id ? " on" : ""}`}
            onClick={() => setFilter(chip.id)}
          >
            {chip.label}
          </button>
        ))}
      </div>
      {error ? <div className="errline">{error}</div> : null}
      {notice ? <div className="hint">{notice}</div> : null}
      <ReviewBand
        channels={dormant}
        onKeep={handleKeepDormant}
        onKeepAll={handleKeepAllDormant}
        onUnsubscribe={handleToggleSubscribe}
      />
      <div className="channel-list">
        {visibleChannels.map((c) => (
          <div key={c.id} className="channel-row chan-artrow sect">
            {c.has_banner ? (
              <div
                className="chan-banner"
                style={{ backgroundImage: `url(${channelBannerUrl(c.id)})` }}
                aria-hidden="true"
              />
            ) : null}
            {c.has_avatar ? (
              <img className="chan-av" src={channelAvatarUrl(c.id)} alt="" />
            ) : (
              <div
                className={`chan-av ${gradientClassFor(c.id)}`}
                aria-hidden="true"
              />
            )}
            <div className="channel-info">
              <div className="chan-nameline">
                <h3>
                  {onOpenChannel ? (
                    <button
                      type="button"
                      className="chan-link"
                      onClick={() => onOpenChannel(c.id)}
                    >
                      {c.name}
                    </button>
                  ) : (
                    c.name
                  )}
                </h3>
                {/* Auto-add lives on the channel's own Settings tab, so without
                    a marker here the list gives no hint which channels download
                    by themselves — the only way to tell was to click the
                    Auto-add chip. */}
                {c.autodownload ? (
                  <span
                    className="chan-autotag"
                    title="Auto-add is on — new videos download automatically"
                  >
                    <Icon name="download" size="12px" label="Auto-add is on" />
                  </span>
                ) : null}
              </div>
              <div className="channel-by">
                {c.handle ? `${c.handle} · ` : ""}
                <b>{c.pending_count}</b> pending · <b>{c.downloaded_count}</b>{" "}
                downloaded
              </div>
            </div>
            {/* The star and the ⋮ menu share one plate so they read as a single
                control cluster on the row's right edge — where the banner scrim
                is at its most transparent, a plate gives them a surface to sit
                on instead of floating on the artwork. */}
            <div className="chan-rowctl">
              {/* The star both shows and toggles subscription — gold/filled when
                  subscribed, faint/outline when not. */}
              <button
                type="button"
                className={`chan-sub-star${c.subscribed ? "" : " off"}`}
                onClick={() => handleToggleSubscribe(c)}
                aria-pressed={c.subscribed}
                title={
                  c.subscribed
                    ? "Subscribed — click to unsubscribe"
                    : "Not subscribed — click to subscribe"
                }
              >
                <Icon
                  name={c.subscribed ? "starFilled" : "star"}
                  size="18px"
                  label={c.subscribed ? "Unsubscribe" : "Subscribe"}
                />
              </button>
              <RowMenu
                label={`Actions for ${c.name}`}
                actions={[
                  ...(onOpenChannel
                    ? [
                        {
                          label: "Open channel",
                          icon: "externalLink" as const,
                          onClick: () => onOpenChannel(c.id),
                        },
                      ]
                    : []),
                  // Subscribed channels only: the endpoint answers 400
                  // "channel is not subscribed" otherwise, which is why the
                  // channel page hides its own Check now the same way.
                  ...(c.subscribed
                    ? [
                        {
                          label: "Check now",
                          icon: "clock" as const,
                          onClick: () => handleScan(c),
                        },
                      ]
                    : []),
                  {
                    label: "Delete channel",
                    icon: "trash",
                    danger: true,
                    onClick: () => setPendingDelete(c),
                  },
                ]}
              />
            </div>
          </div>
        ))}
      </div>
      {/* Only speak when the list is genuinely empty: no channels at all, or a
          search that hid them. When the only channels are dormant they show in
          the review band above, so "No channels yet." would contradict it —
          stay silent then, as the pre-search code did. */}
      {visibleChannels.length === 0 &&
      !error &&
      (channels.length === 0 || (q !== "" && hasNonDormant)) ? (
        <p style={{ color: "var(--color-faint)" }}>
          {/* A filtered view can be empty while channels sit one chip away, so
              every branch but "all" points at All rather than implying there is
              nothing here. */}
          {q !== "" && hasNonDormant
            ? "No channels match your search."
            : filter === "all"
              ? "No channels yet."
              : filter === "subscribed"
                ? "No subscribed channels — see All."
                : filter === "tracked"
                  ? "Every channel here is subscribed — see All."
                  : "No channels match this filter."}
        </p>
      ) : null}
      <AutoUnsubscribedSection
        channels={tombstones}
        onResubscribe={handleResubscribe}
      />
      {/* Delete is confirmed in a modal, not window.confirm. Same wording as
          the channel page's own delete (channel/SettingsTab): it is the same
          irreversible action, so it must not warn less here. */}
      <ConfirmDialog
        open={pendingDelete !== null}
        title="Delete channel?"
        confirmLabel="Delete channel"
        busy={deleteBusy}
        onConfirm={confirmDelete}
        onCancel={() => setPendingDelete(null)}
      >
        {pendingDelete ? (
          <ChannelDeleteWarning
            name={pendingDelete.name}
            count={pendingDelete.downloaded_count}
          />
        ) : null}
      </ConfirmDialog>
    </>
  );
}
