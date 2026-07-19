import { useEffect, useState } from "react";
import { Icon } from "../icons";
import {
  listChannels,
  updateChannel,
  subscribeChannel,
  unsubscribeChannel,
  deleteChannel,
  type ChannelFilter,
} from "../api/channels";
import type { Channel } from "../api/types";

const CHIPS: { id: ChannelFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "subscribed", label: "Subscribed" },
  { id: "tracked", label: "Tracked" },
];

// Channels — tracked/subscribed channel management: filter chips, and per
// row a subscribe toggle, autodownload toggle, format-override field, and a
// delete-with-confirm. Mirrors Library's chip pattern and Settings' panel
// language (.sect / .ctrl / .btn) for visual consistency — no new look.
export function Channels() {
  const [filter, setFilter] = useState<ChannelFilter>("all");
  const [channels, setChannels] = useState<Channel[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [formatDrafts, setFormatDrafts] = useState<Record<string, string>>({});

  function load(f: ChannelFilter) {
    setError(null);
    listChannels(f)
      .then((cs) => {
        setChannels(cs);
        setFormatDrafts((prev) => {
          const next = { ...prev };
          for (const c of cs) {
            if (next[c.id] === undefined) next[c.id] = c.format_override ?? "";
          }
          return next;
        });
      })
      .catch((e: Error) => setError(e.message));
  }

  useEffect(() => {
    load(filter);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filter]);

  function applyLocalUpdate(id: string, patch: Partial<Channel>) {
    setChannels((prev) => prev.map((c) => (c.id === id ? { ...c, ...patch } : c)));
  }

  async function handleToggleSubscribe(c: Channel) {
    const next = !c.subscribed;
    applyLocalUpdate(c.id, { subscribed: next });
    try {
      if (next) {
        await subscribeChannel(c.id);
      } else {
        await unsubscribeChannel(c.id);
      }
      load(filter);
    } catch (err) {
      applyLocalUpdate(c.id, { subscribed: c.subscribed });
      setError((err as Error).message);
    }
  }

  async function handleToggleAutodownload(c: Channel) {
    const next = !c.autodownload;
    applyLocalUpdate(c.id, { autodownload: next });
    try {
      await updateChannel(c.id, { autodownload: next });
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
      <div className="channel-list">
        {channels.map((c) => (
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
                  aria-label="Autodownload"
                />
                Autodownload
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
      {channels.length === 0 && !error ? <p style={{ color: "var(--color-faint)" }}>No channels yet.</p> : null}
    </>
  );
}
