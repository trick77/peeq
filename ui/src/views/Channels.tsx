import { useEffect, useRef, useState, type FormEvent } from "react";
import { Icon } from "../icons";
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
  type ChannelFilter,
} from "../api/channels";
import { CookieRequiredError } from "../api/downloads";
import { isChannelURL } from "../youtube";
import type { AutoUnsubscribedChannel, Channel } from "../api/types";
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
// filter chips, and per row a subscribe toggle, autodownload toggle,
// format-override field, and a delete-with-confirm. Mirrors Library's chip
// pattern and Settings' panel language (.sect / .ctrl / .btn) for visual
// consistency — no new look.
export function Channels() {
  const [filter, setFilter] = useState<ChannelFilter>("all");
  const [channels, setChannels] = useState<Channel[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [formatDrafts, setFormatDrafts] = useState<Record<string, string>>({});
  const [addUrl, setAddUrl] = useState("");
  const [addSubscribe, setAddSubscribe] = useState(false);
  const [addBusy, setAddBusy] = useState(false);
  const [addError, setAddError] = useState<string | null>(null);
  const [added, setAdded] = useState<{ name: string; subscribed: boolean } | null>(null);
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
        setFormatDrafts((prev) => {
          const next = { ...prev };
          for (const c of cs) {
            if (next[c.id] === undefined) next[c.id] = c.format_override ?? "";
          }
          return next;
        });
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
      setAddError("Paste a channel link (a /channel/, /@handle, /c/, or /user/ URL).");
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
        setAddError("No YouTube cookie configured yet. Paste one on the Settings page before adding a channel.");
      } else {
        setAddError((err as Error).message ?? "Failed to add channel.");
      }
    } finally {
      setAddBusy(false);
    }
  }

  function applyLocalUpdate(id: string, patch: Partial<Channel>) {
    setChannels((prev) => prev.map((c) => (c.id === id ? { ...c, ...patch } : c)));
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

  async function handleToggleAutodownload(c: Channel) {
    const next = !c.autodownload;
    applyLocalUpdate(c.id, { autodownload: next });
    try {
      await updateChannel(c.id, { autodownload: next });
      // Refetch rather than trust the optimistic flip: under the Auto-add
      // chip a channel switched off no longer belongs in the list.
      load(filterRef.current);
    } catch (err) {
      applyLocalUpdate(c.id, { autodownload: c.autodownload });
      setError((err as Error).message);
    }
  }

  async function handleSaveFormatOverride(c: Channel) {
    const draft = formatDrafts[c.id] ?? "";
    if (draft === (c.format_override ?? "")) return;
    try {
      await updateChannel(c.id, { format_override: draft });
      applyLocalUpdate(c.id, { format_override: draft });
    } catch (err) {
      setError((err as Error).message);
    }
  }

  async function handleDelete(c: Channel) {
    if (!window.confirm("Delete this channel and ALL its downloaded videos?")) return;
    try {
      await deleteChannel(c.id);
      setChannels((prev) => prev.filter((x) => x.id !== c.id));
    } catch (err) {
      setError((err as Error).message);
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
      setDormant((prev) => (prev.some((x) => x.id === c.id) ? prev : [...prev, c]));
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

  return (
    <>
      <form className="sect channel-add" onSubmit={handleAdd}>
        <div className="paste">
          <label className="field">
            <Icon name="link" size="18px" style={{ color: "var(--color-faint)" }} />
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
          <button className="btn primary" type="submit" disabled={addBusy || !addUrl.trim()}>
            <Icon name="plus" size="18px" />
            {addBusy ? (addSubscribe ? "Subscribing…" : "Tracking…") : addSubscribe ? "Subscribe" : "Track"}
          </button>
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
        {channels.filter((c) => !c.dormant).map((c) => (
          <div key={c.id} className="channel-row sect">
            <div className="channel-info">
              <h3 style={{ margin: 0, fontFamily: "var(--font-serif)", fontSize: 17, fontWeight: 500 }}>{c.name}</h3>
              <div className="by" style={{ marginTop: 2 }}>
                {c.handle ? `${c.handle} · ` : ""}
                {c.pending_count} pending · {c.downloaded_count} downloaded
              </div>
            </div>
            <div className="channel-actions">
              <label className="ctrl channel-toggle">
                <input
                  type="checkbox"
                  checked={c.autodownload}
                  onChange={() => handleToggleAutodownload(c)}
                  aria-label="Auto-add"
                />
                Auto-add
              </label>
              <input
                type="text"
                className="channel-format-input"
                value={formatDrafts[c.id] ?? ""}
                onChange={(e) => setFormatDrafts((prev) => ({ ...prev, [c.id]: e.target.value }))}
                onBlur={() => handleSaveFormatOverride(c)}
                placeholder="Format override (optional)"
                aria-label="Format override"
              />
              <button type="button" className={`abtn${c.subscribed ? " gold" : ""}`} onClick={() => handleToggleSubscribe(c)}>
                <Icon name={c.subscribed ? "starFilled" : "star"} size="16px" />
                {c.subscribed ? "Unsubscribe" : "Subscribe"}
              </button>
              <button
                type="button"
                className="abtn danger"
                onClick={() => handleDelete(c)}
                aria-label={`Delete ${c.name}`}
              >
                <Icon name="trash" size="16px" /> Delete
              </button>
            </div>
          </div>
        ))}
      </div>
      {channels.length === 0 && !error ? (
        <p style={{ color: "var(--color-faint)" }}>
          {filter === "all" ? "No channels yet." : "No channels match this filter."}
        </p>
      ) : null}
      <AutoUnsubscribedSection channels={tombstones} onResubscribe={handleResubscribe} />
    </>
  );
}
