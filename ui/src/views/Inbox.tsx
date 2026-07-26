import { useEffect, useMemo, useState } from "react";
import { SearchField } from "../components/SearchField";
import { Icon } from "../icons";
import { listPending, downloadPending, ignorePending } from "../api/pending";
import type { PendingItem, VideoSort } from "../api/types";
import { formatAgo, formatDuration } from "../format";
import { Button, controlClass } from "../ui";
import { INBOX_SORT_OPTIONS } from "./Library";

// Inbox — new uploads awaiting the user's keep/ignore call: an inbox of things
// your channels posted, a count of what is unread, cleared by acting on each.
// This page was "Pending", then briefly "Decide"; the API is still /api/pending
// throughout — only the UI vocabulary has moved, so the channel_videos.state
// enum and its handlers stay untouched.
//
// Cards are the library card (`.card.video-card`): same grid, same thumbnail,
// same channel-eyebrow-above-clamped-title order, same `.card-foot` action
// row. Two honest differences remain — the thumbnail is the remote
// `thumbnail_url` (no local media exists yet, an item here has never been
// downloaded) and the actions are Download now / Ignore rather than
// favorite/watched.

// sortKey is the date an item orders by: its publish date when known, else
// the day the scan discovered it. This mirrors the Library's air_* clauses'
// COALESCE(published_at, date(created_at)) ORDER BY, so a dateless row (one
// the scanner hasn't healed yet) still lands somewhere sensible instead of
// sinking to the bottom. discovered_at is a datetime; slicing to 10 chars
// keeps the comparison on the same YYYY-MM-DD granularity as published_at.
function sortKey(i: PendingItem): string {
  return i.published_at || i.discovered_at.slice(0, 10);
}

// compareBy returns the comparator for one INBOX_SORT_OPTIONS id, matching the
// backend's sortClauses (videos/store.go) so the two lists order alike.
// video_id is the final tiebreak everywhere, which is what keeps the grid
// from reshuffling between renders when the primary keys tie.
//
// The added-date ids get no arm: INBOX_SORT_OPTIONS never offers them here,
// since an inbox item has never been downloaded and so has no added date. They
// would land in default: and order by publish date, which is the only honest
// answer anyway.
function compareBy(
  sort: VideoSort,
): (a: PendingItem, b: PendingItem) => number {
  const byID = (a: PendingItem, b: PendingItem) =>
    a.video_id.localeCompare(b.video_id);
  switch (sort) {
    case "oldest":
      return (a, b) => sortKey(a).localeCompare(sortKey(b)) || byID(a, b);
    case "longest":
      return (a, b) =>
        (b.duration_seconds || 0) - (a.duration_seconds || 0) || byID(a, b);
    case "title":
      return (a, b) =>
        a.title.localeCompare(b.title, undefined, { sensitivity: "base" }) ||
        byID(a, b);
    default:
      return (a, b) => sortKey(b).localeCompare(sortKey(a)) || byID(a, b);
  }
}

export function Inbox({
  onCountChange,
  onOpenChannel,
  search = "",
  onSearchChange,
  onQueued,
}: {
  onCountChange?: (n: number) => void;
  onOpenChannel?: (id: string) => void;
  /**
   * The search box's text, owned by App so it survives navigating away and
   * back — the same arrangement the Library and the Channels list use.
   */
  search?: string;
  onSearchChange?: (value: string) => void;
  // onQueued — fired after a video is queued for download, so App can seed the
  // queue poll and the item shows on Queue right away (mirrors the Add view).
  onQueued?: () => void;
} = {}) {
  const [items, setItems] = useState<PendingItem[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  // channel filter: "all" or a specific channel_id. For the common case of a
  // channel dumping a week of uploads at once, this narrows the grid (and the
  // Download-all action) to one channel.
  const [channel, setChannel] = useState<string>("all");
  // The Library's orderings minus the added-date pair (see
  // INBOX_SORT_OPTIONS); "newest" means the same thing here as there. Applied
  // client-side (unlike Library's `sort` query param) because /api/pending
  // returns the whole inbox in one unpaged response — there is nothing for a
  // server-side ORDER BY to win here.
  const [sort, setSort] = useState<VideoSort>("newest");
  // Bulk state: bulkBusy while the Download-all loop runs; confirmBulk is the
  // inline two-step guard for large batches (a 40-video download is not a
  // click to fire by accident).
  const [bulkBusy, setBulkBusy] = useState(false);
  const [confirmBulk, setConfirmBulk] = useState(false);

  function load() {
    setError(null);
    listPending()
      .then((list) => {
        setItems(list);
        onCountChange?.(list.length);
      })
      .catch((e: Error) => setError(e.message));
  }

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // The distinct channels present, in first-seen order, so the chips are
  // stable and don't reshuffle as the grid drains.
  const channels = useMemo(() => {
    const seen = new Map<string, string>();
    for (const it of items) {
      if (it.channel_id && !seen.has(it.channel_id)) {
        seen.set(it.channel_id, it.channel_name || it.channel_id);
      }
    }
    return Array.from(seen, ([id, name]) => ({ id, name }));
  }, [items]);

  // The one client-side pipeline: channel chip, then the search box, then the
  // sort. Client-side because /api/pending returns the whole inbox unpaged, so
  // there is nothing off-screen a server query could reach. Note that Download
  // all acts on `visible`, so a search narrows the bulk action too — which is
  // the point: it is how you download just the three videos you searched for.
  const visible = useMemo(() => {
    const q = search.trim().toLowerCase();
    const list = items.filter(
      (i) =>
        (channel === "all" || i.channel_id === channel) &&
        (q === "" ||
          i.title.toLowerCase().includes(q) ||
          (i.channel_name ?? "").toLowerCase().includes(q)),
    );
    return [...list].sort(compareBy(sort));
  }, [items, channel, sort, search]);

  // If the active channel filter empties out (its last item was downloaded or
  // ignored), fall back to "all" so the user isn't left staring at a blank
  // grid with a filter still applied.
  useEffect(() => {
    if (channel !== "all" && !items.some((i) => i.channel_id === channel)) {
      setChannel("all");
    }
  }, [items, channel]);

  function remove(videoID: string) {
    setItems((prev) => {
      const next = prev.filter((i) => i.video_id !== videoID);
      onCountChange?.(next.length);
      return next;
    });
  }

  async function handleDownload(item: PendingItem) {
    setBusyId(item.video_id);
    try {
      await downloadPending(item.video_id);
      remove(item.video_id);
      onQueued?.();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusyId(null);
    }
  }

  async function handleIgnore(item: PendingItem) {
    setBusyId(item.video_id);
    try {
      await ignorePending(item.video_id);
      remove(item.video_id);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusyId(null);
    }
  }

  // Download all — queues every currently-visible item. There is no bulk
  // endpoint; it loops the existing single-item /api/pending/{id}/download so
  // the backend contract stays exactly one video per call. Sequential (not
  // Promise.all) so a mid-batch failure stops cleanly with the rest still on
  // the page, rather than firing 40 requests at once. Confirms first for a
  // large batch via the inline two-step below.
  async function handleDownloadAll() {
    const batch = visible;
    if (batch.length > 10 && !confirmBulk) {
      setConfirmBulk(true);
      return;
    }
    setConfirmBulk(false);
    setBulkBusy(true);
    setError(null);
    let queuedAny = false;
    try {
      for (const item of batch) {
        await downloadPending(item.video_id);
        remove(item.video_id);
        queuedAny = true;
      }
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBulkBusy(false);
      // Seed the queue once for the whole batch (not per item) if anything was
      // actually queued — even when the batch stopped early on a failure.
      if (queuedAny) onQueued?.();
    }
  }

  const bulkLabel = confirmBulk
    ? `Download ${visible.length} — confirm`
    : "Download all";

  return (
    <>
      {error ? <div className="errline">{error}</div> : null}

      {items.length > 0 ? (
        <>
          {/* Same toolbar as the Library and the Channels list: search leads,
              sort sits at the far right, chips go beneath. The row used to be
              chips-then-sort in one ad-hoc flex line; giving the search box its
              canonical slot meant giving the chips theirs. Download all rides
              in the toolbar because it acts on exactly what search and sort
              have selected. */}
          <div className="listbar">
            <SearchField
              value={search}
              onChange={(v) => onSearchChange?.(v)}
              placeholder="Search the inbox"
              label="Search the inbox"
            />
            <select
              className={`${controlClass} push-end`}
              style={{ maxWidth: 190 }}
              value={sort}
              onChange={(e) => setSort(e.target.value as VideoSort)}
              aria-label="Sort"
            >
              {INBOX_SORT_OPTIONS.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.label}
                </option>
              ))}
            </select>
            {visible.length > 1 ? (
              <Button
                type="button"
                variant={confirmBulk ? "primary" : "secondary"}
                busy={bulkBusy}
                onClick={handleDownloadAll}
                onBlur={() => setConfirmBulk(false)}
              >
                <Icon name="download" size="16px" />
                {bulkLabel}
              </Button>
            ) : null}
          </div>
          {channels.length > 1 ? (
            <div className="catchips">
              <button
                type="button"
                className={`catchip${channel === "all" ? " on" : ""}`}
                onClick={() => setChannel("all")}
              >
                All channels <span className="n">{items.length}</span>
              </button>
              {channels.map((c) => (
                <button
                  key={c.id}
                  type="button"
                  className={`catchip${channel === c.id ? " on" : ""}`}
                  onClick={() => setChannel(c.id)}
                >
                  {c.name}{" "}
                  <span className="n">
                    {items.filter((i) => i.channel_id === c.id).length}
                  </span>
                </button>
              ))}
            </div>
          ) : null}
        </>
      ) : null}

      {items.length > 0 && visible.length === 0 ? (
        <p className="un-empty">
          Nothing in the inbox matches “{search.trim()}”.
        </p>
      ) : null}

      <div className="grid">
        {visible.map((item) => (
          <article key={item.video_id} className="card video-card">
            <div className="thumb">
              <img
                className="fill"
                src={item.thumbnail_url}
                alt=""
                loading="lazy"
              />
              <span className="dur">
                {formatDuration(item.duration_seconds)}
              </span>
            </div>
            {/* Kicker line above the title, exactly like the library card:
                channel · relative publish date, same markup, same helper. The
                scan's date is APPROXIMATE, so it can sit a day off the exact
                one Library shows post-download — identical wording either way.
                Omitted when unknown; only a real publish date belongs here,
                never discovered_at. */}
            <div className="by">
              {onOpenChannel && item.channel_id ? (
                <button
                  type="button"
                  className="chan-link"
                  onClick={() => onOpenChannel(item.channel_id)}
                >
                  {item.channel_name || item.channel_id}
                </button>
              ) : (
                <span className="chan-name">
                  {item.channel_name || item.channel_id}
                </span>
              )}
              {item.published_at ? (
                <>
                  <span className="dot">·</span>
                  {formatAgo(item.published_at)}
                </>
              ) : null}
            </div>
            <h3>{item.title}</h3>
            <div className="card-foot acts-row">
              <Button
                type="button"
                variant="secondary"
                small
                disabled={busyId === item.video_id || bulkBusy}
                onClick={() => handleDownload(item)}
              >
                <Icon name="download" size="15px" />
                Download now
              </Button>
              <Button
                type="button"
                variant="dangerQuiet"
                small
                disabled={busyId === item.video_id || bulkBusy}
                onClick={() => handleIgnore(item)}
              >
                <Icon name="trash" size="15px" />
                Ignore
              </Button>
            </div>
          </article>
        ))}
      </div>
      {items.length === 0 && !error ? (
        <p style={{ color: "var(--color-faint)" }}>Your inbox is empty.</p>
      ) : null}
    </>
  );
}
