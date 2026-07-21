import { useEffect, useRef, useState } from "react";
import { Button } from "../ui";
import { Icon } from "../icons";
import {
  getChannel,
  channelAvatarUrl,
  channelBannerUrl,
  subscribeChannel,
  unsubscribeChannel,
  addChannel,
} from "../api/channels";
import { gradientClassFor } from "../format";
import type { ChannelDetail } from "../api/types";
import { ArchiveTab } from "./channel/ArchiveTab";
import { NewTab } from "./channel/NewTab";
import { SettingsTab } from "./channel/SettingsTab";

type TabId = "archive" | "new" | "settings";

// formatRuntime renders a total duration as whole hours ("61 h"), falling
// back to minutes below an hour so a small channel does not read "0 h".
export function formatRuntime(seconds: number): string {
  if (seconds < 3600) return `${Math.round(seconds / 60)} min`;
  return `${Math.round(seconds / 3600)} h`;
}

// formatBytes renders a size in the largest unit that keeps it readable.
// Binary (1024-based) units, matching how disk usage is shown elsewhere
// in peeq — a decimal GB here would read the wrong number vs. the OS.
export function formatBytes(bytes: number): string {
  const TB = 1024 ** 4;
  const GB = 1024 ** 3;
  const MB = 1024 ** 2;
  const KB = 1024;
  if (bytes >= TB) return `${(bytes / TB).toFixed(1)} TB`;
  if (bytes >= GB) return `${(bytes / GB).toFixed(1)} GB`;
  if (bytes >= MB) return `${Math.round(bytes / MB)} MB`;
  return `${Math.round(bytes / KB)} kB`;
}

// formatAge renders an ISO timestamp as a coarse "how long ago", matching
// how the rest of peeq talks about time on cards.
export function formatAge(iso: string | undefined): string {
  if (!iso) return "—";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "—";
  const days = Math.floor((Date.now() - then) / 86400000);
  if (days <= 0) return "today";
  if (days === 1) return "1 d ago";
  if (days < 30) return `${days} d ago`;
  if (days < 365) return `${Math.round(days / 30)} mo ago`;
  return `${Math.round(days / 365)} y ago`;
}

export function Channel({
  channelId,
  onOpenVideo,
  onBack,
}: {
  channelId: string | null;
  onOpenVideo: (id: string) => void;
  onBack: () => void;
}) {
  const [detail, setDetail] = useState<ChannelDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [tab, setTab] = useState<TabId>("archive");
  const [busy, setBusy] = useState(false);

  // loadSeq drops out-of-order responses, the same guard Channels.tsx uses:
  // navigating between two channels quickly must not leave the slower
  // response painted over the newer one.
  const loadSeq = useRef(0);

  function reload() {
    if (!channelId) return;
    const seq = ++loadSeq.current;
    setError(null);
    getChannel(channelId)
      .then((d) => {
        if (seq !== loadSeq.current) return;
        setDetail(d);
      })
      .catch((e: Error) => {
        if (seq !== loadSeq.current) return;
        setError(e.message);
      });
  }

  useEffect(() => {
    setDetail(null);
    setTab("archive");
    reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [channelId]);

  async function handleToggleSubscribe() {
    if (!detail) return;
    setBusy(true);
    try {
      if (detail.subscribed) await unsubscribeChannel(detail.id);
      else await subscribeChannel(detail.id);
      reload();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function handleTrack() {
    if (!detail) return;
    setBusy(true);
    try {
      await addChannel(`https://www.youtube.com/channel/${detail.id}`, false);
      reload();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  if (!channelId) return <p style={{ color: "var(--color-faint)" }}>No channel selected.</p>;
  if (error && !detail) return <div className="errline">{error}</div>;
  if (!detail) return null;

  // The Archive tab carries a count badge, same as the New tab: the badge
  // tells you how many items are behind that tab, distinct from the
  // "archived" stat shown in the header, which is part of the channel's
  // summary.
  const tabs: { id: TabId; label: string; count?: number }[] = detail.tracked
    ? [
        { id: "archive", label: "Archive", count: detail.archived_count },
        { id: "new", label: "New", count: detail.pending_count },
        { id: "settings", label: "Settings" },
      ]
    : [{ id: "archive", label: "Archive", count: detail.archived_count }];

  return (
    <div className="chan">
      <header className="chan-head">
        {detail.has_banner ? (
          <div
            className="chan-banner"
            style={{ backgroundImage: `url(${channelBannerUrl(detail.id)})` }}
            aria-hidden="true"
          />
        ) : null}
        <div className="chan-head-in">
          {detail.has_avatar ? (
            <img className="chan-av" src={channelAvatarUrl(detail.id)} alt="" />
          ) : (
            <div className={`chan-av ${gradientClassFor(detail.id)}`} aria-hidden="true" />
          )}
          <div className="chan-id">
            <h2>{detail.name}</h2>
            <div className="chan-handle">
              {/* The handle is the one thing on this page that belongs to
                  YouTube rather than peeq, so it links back there. Opens in a
                  new tab: peeq is the archive, and losing your place in it to
                  a YouTube page would be the wrong trade. */}
              {detail.handle ? (
                <>
                  <a
                    className="chan-yt"
                    href={`https://www.youtube.com/${detail.handle}`}
                    target="_blank"
                    rel="noopener noreferrer"
                    title={`Open ${detail.handle} on YouTube`}
                  >
                    {detail.handle}
                    <Icon name="externalLink" size="12px" />
                  </a>
                  {" · "}
                </>
              ) : null}
              {detail.tracked ? (
                <>tracked since {new Date((detail.tracked_at ?? "") + "Z").toLocaleDateString()}</>
              ) : (
                <span style={{ color: "var(--color-faint)" }}>not tracked</span>
              )}
            </div>
            {detail.description ? <p className="chan-desc">{detail.description}</p> : null}
            <div className="chan-stats">
              <div className="chan-stat">
                <div className="k">{detail.archived_count}</div>
                <div className="l">archived</div>
              </div>
              <div className="chan-stat">
                <div className="k">{formatRuntime(detail.runtime_seconds)}</div>
                <div className="l">runtime</div>
              </div>
              <div className="chan-stat">
                <div className="k">{formatBytes(detail.disk_bytes)}</div>
                <div className="l">on disk</div>
              </div>
              <div className="chan-stat">
                <div className="k">{formatAge(detail.newest_published_at)}</div>
                <div className="l">newest</div>
              </div>
            </div>
          </div>
          <div className="chan-acts">
            <Button type="button" variant="ghost" onClick={onBack}>
              <Icon name="tv" size="16px" /> All channels
            </Button>
            {detail.tracked ? (
              <Button
                type="button"
                variant={detail.subscribed ? "gold" : "secondary"}
                busy={busy}
                onClick={handleToggleSubscribe}
              >
                <Icon name={detail.subscribed ? "starFilled" : "star"} size="16px" />
                {detail.subscribed ? "Subscribed" : "Subscribe"}
              </Button>
            ) : (
              <Button type="button" variant="primary" busy={busy} onClick={handleTrack}>
                Track this channel
              </Button>
            )}
          </div>
        </div>
      </header>

      {error ? <div className="errline">{error}</div> : null}

      <div className="chan-tabs" role="tablist">
        {tabs.map((t) => (
          <button
            key={t.id}
            type="button"
            role="tab"
            aria-selected={tab === t.id}
            className={`chan-tab${tab === t.id ? " on" : ""}`}
            onClick={() => setTab(t.id)}
          >
            {t.label}
            {t.count !== undefined ? <span className="chan-cnt">{t.count}</span> : null}
          </button>
        ))}
      </div>

      <div className="chan-body">
        {tab === "archive" ? <ArchiveTab channelId={detail.id} onOpenVideo={onOpenVideo} /> : null}
        {tab === "new" ? <NewTab detail={detail} onChanged={reload} /> : null}
        {tab === "settings" ? <SettingsTab detail={detail} onChanged={reload} onDeleted={onBack} /> : null}
      </div>
    </div>
  );
}
