import { useEffect, useRef, useState, type FormEvent } from "react";
import { Icon } from "../icons";
import { Button } from "../ui";
import {
  addChannel,
  listChannels,
  subscribeChannel,
  unsubscribeChannel,
  deleteChannel,
  listAutoUnsubscribedChannels,
  dismissDormantChannel,
  resubscribeChannel,
  channelAvatarUrl,
  channelBannerUrl,
  type ChannelFilter,
} from "../api/channels";
import { CookieRequiredError } from "../api/downloads";
import { isChannelURL } from "../youtube";
import { gradientClassFor } from "../format";
import type { AutoUnsubscribedChannel, Channel } from "../api/types";
import { RowMenu } from "../components/RowMenu";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { ReviewBand } from "./ReviewBand";
import { AutoUnsubscribedSection } from "./AutoUnsubscribedSection";

const CHIPS: { id: ChannelFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "subscribed", label: "Subscribed" },
  { id: "tracked", label: "Tracked" },
  // The filter id stays "autodownload" — it is the API's ?filter= value and
  // the subscriptions column name. Only the label is user-facing.
  { id: "autodownload", label: "Auto-add" },
];

// Channels — tracked/subscribed channel management: an add-channel form,
// filter chips, and a top-bar search box, with each row showing the channel's
// banner-and-avatar art, its counts, and a single ⋮ actions menu (Subscribe,
// Open, Delete). Auto-add and the format override live on the channel's own
// Settings tab, not here. Mirrors Library's chip/search pattern for visual
// consistency.
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
  const [filter, setFilter] = useState<ChannelFilter>("all");
  const [channels, setChannels] = useState<Channel[]>([]);
  const [error, setError] = useState<string | null>(null);
  // pendingDelete holds the channel the ⋮ menu's Delete opened the confirm
  // dialog for; deleteBusy disables the dialog while the request is in flight.
  const [pendingDelete, setPendingDelete] = useState<Channel | null>(null);
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [addUrl, setAddUrl] = useState("");
  const [addSubscribe, setAddSubscribe] = useState(false);
  const [addBusy, setAddBusy] = useState(false);
  const [addError, setAddError] = useState<string | null>(null);
  const [added, setAdded] = useState<{
    name: string;
    subscribed: boolean;
  } | null>(null);
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

  // handleAdd tracks the pasted channel (subscribing too, if the box is
  // ticked). It always shows a confirmation line rather than relying on the
  // new row appearing: under a non-"all" chip the new channel often does not
  // match the active filter, so the list would not visibly change and a
  // successful add would read as a silent failure.
  async function handleAdd(e: FormEvent) {
    e.preventDefault();
    const trimmed = addUrl.trim();
    if (!trimmed || addBusy) return;
    setAddError(null);
    setAdded(null);
    if (!isChannelURL(trimmed)) {
      setAddError(
        "Paste a channel link (a /channel/, /@handle, /c/, or /user/ URL).",
      );
      return;
    }
    setAddBusy(true);
    try {
      const channel = await addChannel(trimmed, addSubscribe);
      setAdded({ name: channel.name, subscribed: channel.subscribed });
      setAddUrl("");
      load(filterRef.current);
    } catch (err) {
      if (err instanceof CookieRequiredError) {
        setAddError(
          "No YouTube cookie configured yet. Paste one on the Settings page before adding a channel.",
        );
      } else {
        setAddError((err as Error).message ?? "Failed to add channel.");
      }
    } finally {
      setAddBusy(false);
    }
  }

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
      <form className="sect channel-add" onSubmit={handleAdd}>
        <div className="paste">
          <label className="field">
            <Icon
              name="link"
              size="18px"
              style={{ color: "var(--color-faint)" }}
            />
            <input
              value={addUrl}
              onChange={(e) => setAddUrl(e.target.value)}
              placeholder="https://www.youtube.com/@handle"
              spellCheck={false}
              aria-label="Channel URL"
            />
          </label>
          {/* The label names the outcome rather than the mechanism, so the
              Subscribe checkbox visibly changes what the button will do. */}
          <Button type="submit" busy={addBusy} disabled={!addUrl.trim()}>
            {!addBusy && <Icon name="plus" size="18px" />}
            {addBusy
              ? addSubscribe
                ? "Subscribing"
                : "Tracking"
              : addSubscribe
                ? "Subscribe"
                : "Track"}
          </Button>
        </div>
        <label className="ctrl channel-toggle" style={{ marginTop: 10 }}>
          <input
            type="checkbox"
            checked={addSubscribe}
            onChange={(e) => setAddSubscribe(e.target.checked)}
            aria-label="Subscribe"
          />
          Subscribe
        </label>
        {addError ? <div className="errline">{addError}</div> : null}
        {added ? (
          <div className="hint" style={{ marginTop: 10 }}>
            <span className="led" />
            {added.subscribed
              ? `Subscribed to ${added.name} — new uploads will be picked up on the next scan.`
              : `Tracked ${added.name} — not subscribed, so new uploads won't be picked up yet.`}
          </div>
        ) : null}
      </form>
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
                <span
                  className={`chan-sub-star${c.subscribed ? "" : " off"}`}
                  title={c.subscribed ? "Subscribed" : "Not subscribed"}
                >
                  <Icon
                    name={c.subscribed ? "starFilled" : "star"}
                    size="15px"
                    label={c.subscribed ? "Subscribed" : "Not subscribed"}
                  />
                </span>
              </div>
              <div className="channel-by">
                {c.handle ? `${c.handle} · ` : ""}
                <b>{c.pending_count}</b> pending · <b>{c.downloaded_count}</b>{" "}
                downloaded
              </div>
            </div>
            <RowMenu
              label={`Actions for ${c.name}`}
              actions={[
                {
                  label: c.subscribed ? "Unsubscribe" : "Subscribe",
                  icon: c.subscribed ? "starFilled" : "star",
                  onClick: () => handleToggleSubscribe(c),
                },
                ...(onOpenChannel
                  ? [
                      {
                        label: "Open channel",
                        icon: "externalLink" as const,
                        onClick: () => onOpenChannel(c.id),
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
        ))}
      </div>
      {visibleChannels.length === 0 && !error ? (
        <p style={{ color: "var(--color-faint)" }}>
          {q !== "" && hasNonDormant
            ? "No channels match your search."
            : filter === "all"
              ? "No channels yet."
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
          <>
            Delete <b>{pendingDelete.name}</b> and its{" "}
            {pendingDelete.downloaded_count} videos? This removes the files from
            disk, including any you kept forever. This cannot be undone.
          </>
        ) : null}
      </ConfirmDialog>
    </>
  );
}
