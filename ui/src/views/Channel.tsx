import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Button } from "../ui";
import { Icon } from "../icons";
import {
  getChannel,
  channelAvatarUrl,
  channelBannerUrl,
  subscribeChannel,
  unsubscribeChannel,
  addChannel,
  refreshChannel,
} from "../api/channels";
import { CookieRequiredError } from "../api/downloads";
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

// formatSubscribers renders a subscriber count the way YouTube itself does —
// "7.2M", "412K" — because that is the number the user recognises from the
// channel page they came from. undefined means YouTube never reported one
// (the channel hides it, or peeq has never read the channel), which is a
// different thing from zero and reads as "—".
export function formatSubscribers(n: number | undefined): string {
  if (!n || n < 0) return "—";
  // One decimal below 100 of a unit ("7.2M"), whole numbers above it
  // ("412K") — more precision than that is noise on a number this large.
  const short = (v: number) =>
    v >= 100 ? String(Math.round(v)) : v.toFixed(1).replace(/\.0$/, "");
  if (n >= 1_000_000) return `${short(n / 1_000_000)}M`;
  if (n >= 1000) {
    const k = short(n / 1000);
    // Rounding can push a count just under a million over the boundary
    // (999,999 → "1000K"), which is not how anyone writes it.
    return k === "1000" ? "1M" : `${k}K`;
  }
  return String(n);
}

// formatStamp renders one of peeq's stored timestamps as a plain local date.
// The stored form has no zone marker but is always UTC, so the "Z" is what
// stops the browser reading it as local time and shifting the date.
export function formatStamp(stored: string | undefined): string {
  if (!stored) return "";
  const d = new Date(stored + "Z");
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleDateString();
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

// ChannelState is the part of the handle line that reports on YouTube rather
// than on peeq: whether the channel is still there, and how current peeq's
// copy of its details is. It has three readings, in priority order:
//
//   gone            — peeq auto-unsubscribed after YouTube kept reporting the
//                     channel deleted. The most definite thing peeq can say,
//                     so it outranks the freshness of the metadata.
//   refresh failed  — resolved_at is set but the attempt did not succeed.
//                     This is the state behind a channel with no avatar, no
//                     banner and no description: peeq tried once, failed, and
//                     will not try again on its own.
//   active          — resolved cleanly, with the date it last read the channel.
//
// A channel with no resolved_at at all has simply never been read, and says
// so plainly rather than claiming anything about YouTube.
export function ChannelState({ detail }: { detail: ChannelDetail }) {
  if (detail.gone) {
    return (
      <>
        <span className="sep">·</span>
        <span className="chan-state dead">
          <span className="led dead" />
          Gone from YouTube
        </span>
      </>
    );
  }
  if (!detail.resolved_at) {
    return (
      <>
        <span className="sep">·</span>
        <span className="chan-state">
          <span className="led unknown" />
          Never read from YouTube
        </span>
      </>
    );
  }
  if (!detail.resolve_ok) {
    return (
      <>
        <span className="sep">·</span>
        <span className="chan-state stale">
          <span className="led unknown" />
          Last refresh failed {formatStamp(detail.resolved_at)}
        </span>
      </>
    );
  }
  return (
    <>
      <span className="sep">·</span>
      <span className="chan-state">
        <span className="led" />
        Active on YouTube
      </span>
      <span className="sep">·</span>
      <span>Refreshed {formatStamp(detail.resolved_at)}</span>
    </>
  );
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
  // Refresh gets its own busy flag rather than sharing `busy`: it runs while
  // the user waits (yt-dlp, then two image fetches) and must not leave the
  // Subscribe button spinning alongside it.
  const [refreshing, setRefreshing] = useState(false);
  // Whether the description is expanded past its 5-line clamp. Reset per
  // channel, so navigating to another one does not inherit "expanded".
  const [descOpen, setDescOpen] = useState(false);
  // descOverflows drives the More button, and is MEASURED rather than guessed
  // from the text length: how many characters fit in five lines depends on
  // the glyphs and the window width, and a length threshold gets the band
  // around the clamp wrong in both directions — hiding More from a
  // description that is in fact cut off, which is the exact thing the button
  // exists to prevent.
  const descRef = useRef<HTMLParagraphElement>(null);
  const [descOverflows, setDescOverflows] = useState(false);

  // channelIdRef mirrors the channel currently on screen. An in-flight
  // refresh cannot read channelId directly: handleRefresh closed over the
  // value it started with, so comparing against that would always say "still
  // here" no matter where the user has navigated since.
  const channelIdRef = useRef(channelId);

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
    channelIdRef.current = channelId;
    setDetail(null);
    setTab("archive");
    setDescOpen(false);
    reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [channelId]);

  // Measure after layout, and only while the clamp is actually on: an
  // expanded paragraph never overflows, so measuring one would say "no
  // overflow" and take the Less button away mid-read. Re-measured on resize
  // because the answer depends on how wide the column is.
  useLayoutEffect(() => {
    if (descOpen) return;
    const el = descRef.current;
    if (!el) {
      setDescOverflows(false);
      return;
    }
    const measure = () =>
      setDescOverflows(el.scrollHeight > el.clientHeight + 1);
    measure();
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
  }, [detail?.description, descOpen]);

  // handleRefresh re-reads the channel from YouTube. It is the only way out
  // of the state where an early failed resolve left the channel with no
  // avatar, banner or description and peeq stopped retrying — so it is worth
  // waiting for, and the error is worth showing rather than swallowing.
  async function handleRefresh() {
    if (!detail) return;
    // A refresh runs for tens of seconds — long enough for the user to move
    // to another channel before it lands. Everything after the await is
    // therefore gated on still being on the channel we asked about, the same
    // guard reload() applies with loadSeq: otherwise THIS channel's failure
    // surfaces under ANOTHER channel's header and reads as that one being
    // broken.
    const requested = detail.id;
    const stillHere = () => requested === channelIdRef.current;
    setRefreshing(true);
    setError(null);
    try {
      await refreshChannel(requested);
      if (!stillHere()) return;
      reload();
    } catch (e) {
      if (!stillHere()) return;
      setError(
        e instanceof CookieRequiredError
          ? "peeq needs a fresh YouTube cookie before it can read this channel."
          : (e as Error).message,
      );
    } finally {
      if (stillHere()) setRefreshing(false);
    }
  }

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

  if (!channelId)
    return <p style={{ color: "var(--color-faint)" }}>No channel selected.</p>;
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
      {/* Leaving the page is navigation, not something you do TO this
          channel, so it sits above the header rather than in the action
          column next to Subscribe and Refresh. */}
      <button type="button" className="chan-back" onClick={onBack}>
        <Icon name="chevronLeft" size="14px" />
        All channels
      </button>
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
            <div
              className={`chan-av ${gradientClassFor(detail.id)}`}
              aria-hidden="true"
            />
          )}
          <div className="chan-id">
            <h2>
              {detail.name}
              {/* YouTube's verified mark, drawn muted: it is a fact about the
                  channel, not a peeq state, and must not read as an accent. */}
              {detail.verified ? (
                <Icon
                  name="verified"
                  size="18px"
                  label="Verified by YouTube"
                  style={{ color: "var(--color-muted)" }}
                />
              ) : null}
            </h2>
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
                  <span className="sep">·</span>
                </>
              ) : null}
              {detail.tracked ? (
                <span>Tracked since {formatStamp(detail.tracked_at)}</span>
              ) : (
                <span style={{ color: "var(--color-faint)" }}>Not tracked</span>
              )}
              <ChannelState detail={detail} />
            </div>
            {detail.description ? (
              <>
                <p
                  ref={descRef}
                  className={`chan-desc${descOpen ? "" : " clamped"}`}
                  data-testid="chan-desc"
                >
                  {detail.description}
                </p>
                {descOverflows || descOpen ? (
                  <button
                    type="button"
                    className="chan-more"
                    aria-expanded={descOpen}
                    onClick={() => setDescOpen((v) => !v)}
                  >
                    {descOpen ? "Less" : "More"}
                  </button>
                ) : null}
              </>
            ) : null}
            <div className="chan-stats">
              {/* Subscribers leads the row because it is the one number here
                  that describes the CHANNEL; everything after it describes
                  peeq's copy of it. */}
              <div className="chan-stat">
                <div className={`k${detail.subscribers ? "" : " unknown"}`}>
                  {formatSubscribers(detail.subscribers)}
                </div>
                <div className="l">subscribers</div>
              </div>
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
            {detail.tracked ? (
              <Button
                type="button"
                variant={detail.subscribed ? "gold" : "secondary"}
                busy={busy}
                onClick={handleToggleSubscribe}
              >
                <Icon
                  name={detail.subscribed ? "starFilled" : "star"}
                  size="16px"
                />
                {detail.subscribed ? "Subscribed" : "Subscribe"}
              </Button>
            ) : (
              <Button
                type="button"
                variant="primary"
                busy={busy}
                onClick={handleTrack}
              >
                Track this channel
              </Button>
            )}
            {/* Refresh turns primary when it is the thing to press: a channel
                peeq has never managed to read is sitting there with no
                artwork and no description, and this is the only way out. */}
            <Button
              type="button"
              variant={detail.resolve_ok ? "secondary" : "primary"}
              busy={refreshing}
              onClick={handleRefresh}
            >
              <Icon name="refresh" size="16px" /> Refresh
            </Button>
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
            {t.count !== undefined ? (
              <span className="chan-cnt">{t.count}</span>
            ) : null}
          </button>
        ))}
      </div>

      <div className="chan-body">
        {tab === "archive" ? (
          <ArchiveTab channelId={detail.id} onOpenVideo={onOpenVideo} />
        ) : null}
        {tab === "new" ? <NewTab detail={detail} onChanged={reload} /> : null}
        {tab === "settings" ? (
          <SettingsTab detail={detail} onChanged={reload} onDeleted={onBack} />
        ) : null}
      </div>
    </div>
  );
}
