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
import { formatAge, gradientClassFor, toDate } from "../format";
import type { ActivityEvent, ChannelDetail } from "../api/types";
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
// The stored form has no zone marker but is always UTC, so without the rewrite
// format.ts's toDate applies the browser would read it as local time and shift
// the date — near midnight, by a whole day. This keeps its own name because it
// is the date-only spelling; formatAbsolute is the one that also shows a clock.
export function formatStamp(stored: string | undefined): string {
  if (!stored) return "";
  const d = toDate(stored);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleDateString();
}

// ScanStamp is the "when did peeq last look for new videos" segment. It is its
// own component because it belongs to BOTH the healthy reading and the failed
// metadata one: a channel whose metadata refresh keeps failing is still scanned
// daily, and the failed reading is exactly where someone is most likely to fear
// peeq has stopped watching the channel. Showing only the metadata date there
// would repeat, in miniature, the bug this whole change is about.
//
// last_scanned_at is absent for an added-but-unsubscribed channel, whose
// subscriptions row is DELETED on unsubscribe, so there is no scan schedule and
// no stamp to show; that segment simply drops out. "Never scanned" is not said
// here — scheduleLine on the New and Settings tabs is where a channel's
// schedule is spelled out in full.
//
// It is deliberately NOT shown for gone (auto-unsubscribed, so scanning really
// has stopped) or for a channel never read at all.
function ScanStamp({ at }: { at?: string }) {
  if (!at) return null;
  return (
    <>
      <span className="sep">·</span>
      <span>Last channel scan {formatStamp(at)}</span>
    </>
  );
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
//                     will not try again on its own. The header's Refresh
//                     button is the way out of it.
//   active          — resolved cleanly, with the date it last read the channel.
//
// A channel with no resolved_at at all has simply never been read, and says
// so plainly rather than claiming anything about YouTube.
//
// The active reading carries TWO dates, and naming them apart is the point.
// They are separate schedules over separate columns, and they are far apart on
// purpose: the metadata refresh (channels.resolved_at) runs weekly, seeded at a
// random minute per subscription so channels do not refresh in a convoy, while
// the channel scan that finds new videos (subscriptions.last_scanned_at) runs
// daily. A single stamp labelled "Refreshed" read as the daily one and made a
// perfectly healthy 5-day-old metadata refresh look like a broken scan.
//
// The wording is the backend's own — "channel scan" and "metadata refresh" are
// the phrases handleActivityUpcoming attaches to these two units of work, so a
// row queued in Up next and the stamp here name the same thing. "Last " is the
// one addition: Up next lists the next occurrence, this reports the previous.
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
          Metadata refresh failed {formatStamp(detail.resolved_at)}
        </span>
        <ScanStamp at={detail.last_scanned_at} />
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
      <span>Last metadata refresh {formatStamp(detail.resolved_at)}</span>
      <ScanStamp at={detail.last_scanned_at} />
    </>
  );
}

export function Channel({
  channelId,
  onOpenVideo,
  onBack,
  live = [],
}: {
  channelId: string | null;
  onOpenVideo: (id: string) => void;
  onBack: () => void;
  /**
   * Newest activity events pushed over SSE, the same stream the Activity page
   * reads. The channel page needs them because "Scan now" is asynchronous: the
   * scan happens up to a minute later on the scan loop, and without a signal the
   * button would sit at "Queued" until the user reloaded by hand. A scan event
   * for this channel is exactly that signal.
   */
  live?: ActivityEvent[];
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

  // loadSeq drops out-of-order responses, the same guard Channels.tsx uses:
  // navigating between two channels quickly must not leave the slower
  // response painted over the newer one.
  const loadSeq = useRef(0);

  // channelIdRef mirrors the channel currently on screen. An in-flight refresh
  // cannot read channelId directly: handleRefresh closed over the value it
  // started with, so comparing against that would always say "still here" no
  // matter where the user has navigated since.
  const channelIdRef = useRef(channelId);

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
    handledScanID.current = 0;
    setDetail(null);
    setTab("archive");
    setDescOpen(false);
    reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [channelId]);

  // handledScanID is the newest scan event already acted on. App keeps `live` as
  // a rolling buffer of the last 50 events, so a matching scan event STAYS in it:
  // without this high-water mark the effect below would refetch again on every
  // later unrelated event (a download, a summary) for as long as that scan sat in
  // the buffer, turning one scan into dozens of channel requests.
  const handledScanID = useRef(0);

  // Refetch when a scan for THIS channel lands, so last_scanned_at and
  // next_scan_at move on their own and the Scan now button leaves its "Queued"
  // state without a reload. Filtered by subject id — another channel's scan says
  // nothing about this page.
  useEffect(() => {
    if (!channelId) return;
    let newest = 0;
    for (const e of live) {
      if (e.kind === "scan" && e.subject_id === channelId && e.id > newest) {
        newest = e.id;
      }
    }
    if (newest === 0 || newest <= handledScanID.current) return;
    handledScanID.current = newest;
    reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live, channelId]);

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

  async function handleAdd() {
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

  // handleRefresh re-reads the channel from YouTube. It is the only way out of
  // the state where an early failed resolve left the channel with no avatar,
  // banner or description and peeq stopped retrying — so it is worth waiting
  // for, and the error is worth showing rather than swallowing.
  async function handleRefresh() {
    if (!detail) return;
    // A refresh runs for tens of seconds — long enough for the user to move to
    // another channel before it lands. Everything after the await is gated on
    // still being on the channel we asked about, the same guard reload()
    // applies with loadSeq: otherwise THIS channel's failure surfaces under
    // ANOTHER channel's header and reads as that one being broken.
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
          ? "Peeq needs a fresh YouTube cookie before it can read this channel."
          : (e as Error).message,
      );
    } finally {
      if (stillHere()) setRefreshing(false);
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
  const tabs: { id: TabId; label: string; count?: number }[] = detail.added
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
          column next to Subscribe. */}
      <button type="button" className="chan-back" onClick={onBack}>
        <Icon name="chevronLeft" size="14px" />
        All channels
      </button>
      <header className="chan-head">
        {detail.has_banner ? (
          <div
            className="chan-banner"
            style={{
              backgroundImage: `url(${channelBannerUrl(detail.id, detail.banner_version)})`,
            }}
            aria-hidden="true"
          />
        ) : null}
        <div className="chan-head-in">
          {detail.has_avatar ? (
            <img
              className="chan-av"
              src={channelAvatarUrl(detail.id, detail.avatar_version)}
              alt=""
            />
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
                    className="link chan-yt"
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
              {detail.added ? (
                <span>Added {formatStamp(detail.added_at)}</span>
              ) : (
                <span style={{ color: "var(--color-faint)" }}>Not added</span>
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
                    className="link chan-more"
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
            {detail.added ? (
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
                onClick={handleAdd}
              >
                Add this channel
              </Button>
            )}
            {/* Refresh turns primary when it is the thing to press: a channel
                peeq has never managed to read is sitting there with no artwork
                and no description, and this is the only way out. */}
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
