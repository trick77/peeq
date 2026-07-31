import { useEffect, useMemo, useRef, useState, type MouseEvent } from "react";
import { PillStrip } from "../components/PillStrip";
import { SearchField } from "../components/SearchField";
import { ThumbFill } from "../components/ThumbFill";
import { Icon } from "../icons";
import { listPending, downloadPending, ignorePending } from "../api/pending";
import { pendingThumbnailUrl } from "../api/videos";
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
// row. The one honest difference remaining is the actions — Download / Ignore
// rather than favorite/watched. The thumbnail now goes through the same backend
// proxy the Library uses (`/api/pending/{id}/thumbnail`, which fetches and
// caches the remote poster server-side), so an inbox card never loads
// i.ytimg.com in the browser and falls back to the shared gradient placeholder
// instead of a broken-image glyph when a poster is missing.

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

// offer is everything the card decides about its summary: what the marker says,
// and whether a click leads anywhere. One function answers both because they are
// two halves of one promise, and they had drifted apart — a no_transcript card
// drew no marker and still opened on click, which is exactly the invisible click
// target the marker was introduced to remove.
//
// The rule it enforces is one-directional: **the card never opens without a
// marker**. The converse is not true and must not be, which is why `opens` is a
// separate field rather than "mark !== null":
//
//   done                  Read summary     opens   the summary is written
//   no_transcript + vtt    Read transcript  opens   music, but the text is there
//   pending / running      Summarizing…     opens   a videos row exists; the
//                                                   page shows live progress
//   "" + auto_summary      Summarizing…     INERT   the caption fetcher has not
//                                                   reached it, so there is no
//                                                   videos row and the page
//                                                   would 404 — the marker is a
//                                                   promise about the channel,
//                                                   not a fact about the video
//   no_transcript, no vtt  —                inert   nothing behind the card
//   "" + opted out         —                inert   never will be
//   error                  —                inert   the page's only news is
//                                                   that it will be retried
type Offer = {
  mark: "summary" | "transcript" | "reading" | null;
  opens: boolean;
};

function offer(item: PendingItem): Offer {
  switch (item.summary_status) {
    case "done":
      return { mark: "summary", opens: true };
    case "pending":
    case "running":
      return { mark: "reading", opens: true };
    // no_transcript is two different videos wearing one status. YouTube had no
    // captions at all — nothing behind the card, so it must not pretend
    // otherwise — or the captions turned out to be music and produced no
    // summary, in which case the transcript is still there to skim, and
    // skimming it is how you decide on a video peeq could not read for you.
    case "no_transcript":
      return item.has_subtitles
        ? { mark: "transcript", opens: true }
        : { mark: null, opens: false };
    case "":
      return { mark: item.auto_summary ? "reading" : null, opens: false };
    default:
      return { mark: null, opens: false };
  }
}

// hasPage reports whether a click on this card leads anywhere.
function hasPage(item: PendingItem) {
  return offer(item).opens;
}

// summaryMark draws the card's offer.
//
// Everything readable gets a button, named for what is actually behind it —
// "Read summary" or "Read transcript". A click target with no visible edge is
// one nobody finds, and naming the verb is what makes the page discoverable;
// anything quieter is a label with a handler attached, and hover-reveal is not
// an option peeq has (a trackpad-driven iPad reports `hover: hover` and would
// hide the control for good).
//
// "Summarizing…" stays a span. There is nothing to press yet, and a disabled
// button would be a control that never worked. The quiet scrim treatment keeps
// the two from ever looking interchangeable.
function summaryMark(item: PendingItem, onOpen?: (videoID: string) => void) {
  const { mark, opens } = offer(item);
  if (mark === null) return null;
  if (mark === "reading") {
    return <span className="metapill oncover is-reading">Summarizing…</span>;
  }
  const label = mark === "summary" ? "Summary" : "Transcript";
  // Belt and braces: every readable mark opens today, and a button that did
  // not would be the dead control this whole rule exists to prevent.
  if (!opens) {
    return <span className="metapill oncover has-summary">{label}</span>;
  }
  // No onOpen means the host gave the card nowhere to go — App always passes
  // one, but a caller that does not gets the fact without a dead control.
  if (!onOpen) {
    return <span className="metapill oncover has-summary">{label}</span>;
  }
  return (
    <button
      type="button"
      className="metapill oncover has-summary summary-open"
      onClick={() => onOpen(item.video_id)}
    >
      <Icon name="alignLeft" size="12px" />
      Read {label.toLowerCase()}
      <Icon name="chevronRight" size="12px" />
    </button>
  );
}

export function Inbox({
  onCountChange,
  onOpenChannel,
  onOpen,
  onOrderChange,
  search = "",
  onSearchChange,
  onQueued,
}: {
  /**
   * Reports the inbox's size to App, which feeds the rail's badge. `undefined`
   * means the count is not known — the rail draws no pill for it, and that is
   * deliberately not the same claim as "the inbox is empty". A failed fetch
   * sends undefined rather than leaving the last good number in place, because
   * a stale badge asserts a count nobody can vouch for any more.
   */
  onCountChange?: (n: number | undefined) => void;
  onOpenChannel?: (id: string) => void;
  /**
   * Opens a video's page. An inbox video has no media yet, so that page shows
   * what peeq read of it — the summary — rather than a player. It is the same
   * route the Library opens, deliberately: once the video is downloaded the
   * URL does not change, it just gains a video.
   */
  onOpen?: (id: string) => void;
  /**
   * Reports the ids the grid is currently showing, in the order it shows them.
   *
   * This is what lets a video's page offer Prev / Next through the inbox. It
   * has to come from here rather than being refetched there, because the order
   * on screen is the product of three things this component owns and the API
   * knows nothing about: the search box, the channel chip, and the sort select.
   * A page that re-derived it would say "3 of 40" while the grid behind it
   * showed six.
   */
  onOrderChange?: (ids: string[]) => void;
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
  // Whether the first fetch has settled. Without it an empty `items` means two
  // different things — "the inbox is empty" and "the inbox has not arrived" —
  // and every branch below read it as the first. The mount paint therefore
  // announced "Your inbox is empty." with no toolbar, and the response a moment
  // later replaced that with the search box, the chips and a full grid: the
  // page appeared to render wrong and then correct itself. Every other list
  // already carries this flag (History's `loaded`, Up next's `schedLoaded`),
  // which is why the flicker was the Inbox's alone.
  const [loaded, setLoaded] = useState(false);
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
  // Hover lock. Acting on a card removes it, and the pointer has not moved —
  // but Chrome re-runs its hit test after the reflow, so the card that slides
  // into the gone one's slot comes up with :hover painted on whichever control
  // is now under the cursor. The Download you just pressed appears to still be
  // lit, on a video you have not touched. (Verified: focus is NOT the culprit,
  // it drops to <body> when the card unmounts.) So suppress the hover paint
  // from the removal until the pointer genuinely moves again.
  const [hoverLocked, setHoverLocked] = useState(false);

  // `alive` is false once this view has unmounted. Navigating away mid-fetch
  // used to land the response on a component that is gone: React 18 no longer
  // warns about it, so it was silent, but the writes are still pointless and
  // onCountChange would push a count from a page the user has already left.
  // A ref, not a local — `load` is called from the effect below and could be
  // called again later, and every call must see the same flag.
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  function load() {
    setError(null);
    listPending()
      .then((list) => {
        if (!alive.current) return;
        setItems(list);
        onCountChange?.(list.length);
      })
      .catch((e: Error) => {
        if (!alive.current) return;
        setError(e.message);
        // The count is no longer known — say so rather than leaving the rail
        // showing the last number that happened to arrive. undefined draws no
        // pill; a stale 5 claims five items are waiting, which is exactly what
        // the failed request could not confirm.
        onCountChange?.(undefined);
      })
      // Settled, not succeeded: a failed fetch has also finished telling us what
      // it can, and leaving the page on "Loading…" under its own error message
      // would claim the request is still running.
      .finally(() => {
        if (alive.current) setLoaded(true);
      });
  }

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // The distinct channels present, sorted by name. The Library's category row
  // is the master here: its chips sit in a fixed order that does not depend on
  // what the grid happens to hold, so a chip stays where the eye last left it.
  // First-seen order gave the Inbox the opposite — the row reshuffled as items
  // downloaded or were ignored. Channels have no enum to order by, so the name
  // is the fixed order: alphabetical, case- and accent-insensitive, with
  // `numeric` so a "Channel 10" sorts after "Channel 9".
  const channels = useMemo(() => {
    const seen = new Map<string, string>();
    for (const it of items) {
      if (it.channel_id && !seen.has(it.channel_id)) {
        seen.set(it.channel_id, it.channel_name || it.channel_id);
      }
    }
    return Array.from(seen, ([id, name]) => ({ id, name })).sort(
      (a, b) =>
        a.name.localeCompare(b.name, undefined, {
          sensitivity: "base",
          numeric: true,
        }) ||
        // Two channels can share a display name, and without a tiebreak they
        // fall back to Array.sort's stability — first-seen order, the very
        // thing this sort exists to get rid of. The id is the final tiebreak
        // here as it is in compareBy above.
        a.id.localeCompare(b.id),
    );
  }, [items]);

  // The search-scoped list the channel chips count from: items narrowed by the
  // search box but NOT by the channel chip. Each chip's number then answers
  // "how many would I see if I clicked this under the current search" — the
  // same thing the Library's query-scoped counts answer. Without it the chips
  // read the full per-channel totals and sit unchanged beside a grid a search
  // has emptied. The channel filter is layered on top, in `visible`.
  const searchScoped = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (q === "") return items;
    return items.filter(
      (i) =>
        i.title.toLowerCase().includes(q) ||
        (i.channel_name ?? "").toLowerCase().includes(q),
    );
  }, [items, search]);

  // The one client-side pipeline: channel chip, then the search box, then the
  // sort. Client-side because /api/pending returns the whole inbox unpaged, so
  // there is nothing off-screen a server query could reach. Note that Download
  // all acts on `visible`, so a search narrows the bulk action too — which is
  // the point: it is how you download just the three videos you searched for.
  const visible = useMemo(() => {
    // The channel chip layered on top of the same search-scoped list the chip
    // counts read, so the grid and the counts can never disagree.
    const list = searchScoped.filter(
      (i) => channel === "all" || i.channel_id === channel,
    );
    return [...list].sort(compareBy(sort));
  }, [searchScoped, channel, sort]);

  // Publish the visible order upward whenever it changes. Keyed on the ids
  // rather than on `visible` itself: the array identity changes on every
  // filter recompute, and re-reporting an identical list would loop through
  // App's state and back.
  const orderKey = visible.map((i) => i.video_id).join(",");
  useEffect(() => {
    onOrderChange?.(orderKey ? orderKey.split(",") : []);
    // onOrderChange is a setState from App and stable; orderKey is the value
    // that actually decides whether there is anything new to say.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [orderKey]);

  // handleCardClick opens the video's page from anywhere on the card that is
  // not already something else.
  //
  // The guard is copied verbatim from VideoCard, and both halves earn their
  // place. The closest() check lets the actbar's Download and Ignore, the
  // channel link and the title's YouTube link keep their own click — without
  // it, pressing Ignore would also navigate. The selection check stops a
  // drag-to-select of the title from ending in a page change.
  //
  // Until now the Inbox card carried `.card { cursor: pointer }` from the
  // shared grid-tile rule and did nothing at all when clicked. This closes that
  // gap rather than opening one: the card already claimed to be clickable.
  function handleCardClick(e: MouseEvent<HTMLElement>, item: PendingItem) {
    if (!onOpen || !hasPage(item)) return;
    if ((e.target as HTMLElement).closest('button, a, [role="button"]')) return;
    if (window.getSelection()?.toString()) return;
    onOpen(item.video_id);
  }

  // If the active channel filter empties out (its last item was downloaded or
  // ignored), fall back to "all" so the user isn't left staring at a blank
  // grid with a filter still applied.
  useEffect(() => {
    if (channel !== "all" && !items.some((i) => i.channel_id === channel)) {
      setChannel("all");
    }
  }, [items, channel]);

  // Every card removal goes through here — single Download, Ignore and each
  // step of Download all — so the hover lock is armed in one place.
  function remove(videoID: string) {
    setHoverLocked(true);
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

  // One quiet line until the first fetch settles, then the whole settled page
  // at once. The alternative — paint the empty state now and correct it on
  // arrival — is what the flicker was. The toolbar cannot render early the way
  // History's does, because its channel chips are built from the very items
  // being fetched, so there is no honest header to show first.
  //
  // No error line on this branch: `loaded` is set in the same .finally that
  // follows the .catch, so React commits the two together and a failed fetch
  // renders below, never here.
  if (!loaded) return <p className="agenda-empty">Loading…</p>;

  return (
    <>
      {error ? <div className="errline">{error}</div> : null}

      {items.length > 0 ? (
        <>
          {/* Same toolbar as the Library and the Channels list: search leads,
              chips go beneath. The right-hand end is a quiet control group,
              anchored to the edge by ONE push-end: Download all sits to the LEFT
              of the sort control, which stays the rightmost item, and the two
              sit adjacent. Exactly one auto-margin does the anchoring — putting
              it on both would split the free space into two gaps and separate
              the pair. So push-end rides the Download all button when it is
              shown, and falls to the sort control on the frame where Download
              all is absent (a search that matches nothing leaves zero visible),
              keeping the sort control right-anchored either way. Download all
              rides here because it acts on exactly what search and sort have
              selected — it stays available whenever at least one item is on
              screen, since even a single card is quicker to clear from here than
              from its own row. */}
          <div className="listbar">
            <SearchField
              value={search}
              onChange={(v) => onSearchChange?.(v)}
              placeholder="Search the inbox"
              label="Search the inbox"
            />
            {visible.length > 0 ? (
              /* ghost, not secondary: this is the only listbar on the app with
                 a button beside the sort control, and a filled ink-dim button
                 next to a muted-grey dropdown made the pair read as two
                 different tiers of the same row. Ghost is the same
                 --color-muted the sort control uses, so the toolbar's
                 right-hand end reads as one quiet group. The confirm step still
                 goes primary — that one is meant to be loud.

                 Ghost is a REST-only state, though. `busy` disables the button,
                 and .ui-btn:disabled drops it to opacity 0.6 — on a variant with
                 no fill and no border that leaves faint grey text and a spinner
                 floating in the row, at the one moment the control most needs to
                 be visible. It borrows secondary's fill for the duration, which
                 is what it looked like before this went quiet. A batch of ten or
                 fewer skips the confirm step entirely, so without this the only
                 feedback for the common case would be a fade. */
              <Button
                type="button"
                className="push-end"
                variant={
                  confirmBulk ? "primary" : bulkBusy ? "secondary" : "ghost"
                }
                busy={bulkBusy}
                onClick={handleDownloadAll}
                onBlur={() => setConfirmBulk(false)}
              >
                <Icon name="download" size="16px" />
                {bulkLabel}
              </Button>
            ) : null}
            <select
              className={`${controlClass}${visible.length > 0 ? "" : " push-end"}`}
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
          </div>
          {channels.length > 0 ? (
            <PillStrip lead>
              <div className="catchips">
                <button
                  type="button"
                  className={`catchip${channel === "all" ? " on" : ""}`}
                  onClick={() => setChannel("all")}
                >
                  All channels <span className="n">{searchScoped.length}</span>
                </button>
                {/* Counts are search-scoped so a chip reads how many you'd see if
                  you clicked it under the current query, like the Library. A
                  chip whose channel has no match under the search drops off the
                  row — except the selected one, which stays (at count 0) so you
                  can always un-select it, mirroring the Library's category
                  chips. */}
                {channels
                  .filter(
                    (c) =>
                      c.id === channel ||
                      searchScoped.some((i) => i.channel_id === c.id),
                  )
                  .map((c) => (
                    <button
                      key={c.id}
                      type="button"
                      className={`catchip${channel === c.id ? " on" : ""}`}
                      onClick={() => setChannel(c.id)}
                    >
                      {c.name}{" "}
                      <span className="n">
                        {
                          searchScoped.filter((i) => i.channel_id === c.id)
                            .length
                        }
                      </span>
                    </button>
                  ))}
              </div>
            </PillStrip>
          ) : null}
        </>
      ) : null}

      {/* Only when a query was actually typed. The channel chip can also empty
          the grid for a frame — its last item was just downloaded, and the
          auto-reset to "all" lands a tick later — and reporting that as a
          failed search would print “matches ””. */}
      {items.length > 0 && visible.length === 0 && search.trim() !== "" ? (
        <p className="un-empty">
          Nothing in the inbox matches “{search.trim()}”.
        </p>
      ) : null}

      {/* The listener is attached only while locked: the common case is an
          unlocked grid, and a pointermove handler on it would otherwise fire
          on every mouse twitch for nothing. A real move is the only thing
          that clears the lock — a click without one would be the accidental
          second press this exists to prevent. */}
      {/* .gridwrap is the size-query container the grid steps its column count
          off — see the note in Library.tsx for why it is a wrapper and not
          .page. It has to be here too: the @container rules are named, so a
          .grid with no .gridwrap ancestor never matches one and would sit at
          three columns at every width, down to a 375px phone. */}
      <div className="gridwrap">
        <div
          className={`grid inbox-grid${hoverLocked ? " is-hover-locked" : ""}`}
          onPointerMove={hoverLocked ? () => setHoverLocked(false) : undefined}
        >
          {visible.map((item) => (
            <article
              key={item.video_id}
              className={`card video-card${hasPage(item) ? "" : " is-inert"}`}
              onClick={
                hasPage(item) ? (e) => handleCardClick(e, item) : undefined
              }
            >
              {/* Poster and action bar are one object, so they share a wrapper
                rather than sitting as two children of .card — .card lays its
                children out with a 10px gap, and the bar has to touch the
                poster it belongs to. A wrapper says that in the DOM; cancelling
                the gap with a negative margin would only say it in pixels, and
                would come undone the day the gap changes. */}
              <div className="inbox-poster">
                <div className="thumb">
                  <ThumbFill
                    id={item.video_id}
                    // Always attempt the proxy: the backend falls back to the
                    // hqdefault variant YouTube generates for every video, so an
                    // empty recorded thumbnail_url still gets a real poster. A true
                    // 404 still degrades to the shared gradient via onError.
                    hasThumbnail={true}
                    src={pendingThumbnailUrl(item.video_id)}
                  />
                  <span className="dur">
                    {formatDuration(item.duration_seconds)}
                  </span>
                  {/* Top-left, where the eye starts, since this is the offer to
                    read before you decide. The poster carries only the runtime,
                    so the corner is free.

                    A text row would be the wrong home either way: at the
                    narrowest card the grid draws, a fourth item on the eyebrow
                    truncates the channel name, while a corner is the same size
                    on every card. The bar below is no home for it either: three
                    labelled buttons overrun the 300px card the two-column grid
                    draws, and on the cards with nothing to read the third one
                    would vanish and take Download's alignment with it.

                    Its absence is meaningful: no marker means the card does not
                    open, because offer() decides both. */}
                  {summaryMark(item, onOpen)}
                </div>
                {/* The pair rode the poster itself for one release. That put
                  chrome over the artwork — squarely over the title creators
                  bake into the bottom of a thumbnail — and pushed the runtime
                  chip out of the corner it keeps on every other card in the
                  app. Beneath the poster it covers nothing, and because it
                  sits above the text its position never depends on how long a
                  title is: every card in a row has its Download in the same
                  place, whatever the ones beside it are called.

                  Always visible, never revealed on hover — an action you
                  cannot see is one a touch screen cannot reach. */}
                <div className="inbox-actbar">
                  <Button
                    type="button"
                    variant="secondary"
                    small
                    disabled={busyId === item.video_id || bulkBusy}
                    onClick={() => handleDownload(item)}
                  >
                    <Icon name="download" size="15px" />
                    Download
                  </Button>
                  {/* Named, not a bare trash can. Off the poster the bar has
                    room for the word, and a lone glyph asks the reader to guess
                    whether it drops the video from the inbox or deletes
                    something already downloaded. It stays the smaller of the
                    two — the destructive half should not read as the equal of
                    the action you came here to press — but width says that now,
                    not the absence of a label. The visible text is what names
                    it for a screen reader and for the tests, so it carries no
                    aria-label: two sources of truth for one name is one too
                    many. */}
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
              {/* The title still goes to YouTube, and deliberately still does
                now that the card itself opens something. The two are different
                destinations for different questions: the card answers "what is
                this about", from what peeq read of it, and the title answers
                "show me the actual video". Collapsing them would cost the
                second one, and there is nowhere else on the card to put it.

                New tab, same reasoning as the channel header's handle link:
                peeq is the archive, and losing your place in a triage list to a
                YouTube page would be the wrong trade.

                The ledger's url is best-effort (a scan can record an entry
                without one), so it falls back to a watch URL built from the
                video id — a peeq video id IS the YouTube id, so the fallback
                is always correct, never a guess. */}
              <h3>
                <a
                  className="title-btn"
                  href={
                    item.url ||
                    `https://www.youtube.com/watch?v=${item.video_id}`
                  }
                  target="_blank"
                  rel="noopener noreferrer"
                  title={`Open "${item.title}" on YouTube`}
                >
                  {item.title}
                </a>
              </h3>
            </article>
          ))}
        </div>
      </div>
      {items.length === 0 && !error ? (
        <p style={{ color: "var(--color-faint)" }}>Your inbox is empty.</p>
      ) : null}
    </>
  );
}
