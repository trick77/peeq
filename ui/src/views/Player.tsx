import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon, type IconName } from "../icons";
import { Button, Spinner, iconActionClass } from "../ui";
import { AUTO_SKIP, Scrubber, categoryLabel } from "../components/Scrubber";
import { SleepTimer } from "../components/SleepTimer";
import { RowMenu, type RowMenuAction } from "../components/RowMenu";
import { ConfirmDialog } from "../components/ConfirmDialog";
import {
  getVideo,
  setFavorite,
  setWatched,
  setCategory,
  setResume,
  deleteVideo,
  redownload,
  streamUrl,
  thumbnailUrl,
  createPlaybackGrant,
} from "../api/videos";
import { reprocess, subtitlesUrl } from "../api/search";
import { getSettings, updateSettings } from "../api/settings";
import { setPlaybackState } from "../api/playback";
import { getShareStatus, type ShareStatus } from "../api/share";
import { ShareControl } from "../components/ShareControl";
import type { Video } from "../api/types";
import type { SummaryStatus } from "../api/enums";
import { ApiError } from "../api/http";
import { formatDuration, gradientClassFor } from "../format";
// Only the download filename helper is needed here now: parsing, finding and
// copying all moved into components/TranscriptCard with the markup.
import { transcriptFilenameBase } from "../vtt";
import { centerCuesRef } from "../captions";
import { DOT } from "../sep";
import { MediaStats } from "./player/MediaStats";
import { ContentsCard } from "./player/ContentsCard";
import { TranscriptCard } from "../components/TranscriptCard";
import { UnfetchedVideo } from "./player/UnfetchedVideo";
import { SummaryCard, HighlightsCard } from "./player/SidebarPanels";
import { MetaHeader } from "./player/MetaHeader";
import { park, useParkedAt, videoHostNode } from "../videoHost";
import type { NowPlaying } from "../nowPlaying";

// RESUME_THROTTLE_MS bounds how often `timeupdate` (which fires ~4x/sec)
// is allowed to actually POST the resume position — see handleTimeUpdate.
// visibilitychange/pagehide bypass this throttle entirely (flushOnHide
// below), so closing the tab never loses more than this much progress.
const RESUME_THROTTLE_MS = 5000;

// JUMP_SETTLE_SECONDS — how much playback a jumped-to moment has to survive
// before the playhead is worth storing.
//
// Search can land the playhead anywhere, including inside the last 10% of a
// video, and the server auto-marks a video watched the moment a resume ping
// arrives past that line (videos/store_watch.go). So jumping to a match near the
// end and deciding within a second that it was not what you wanted used to mark
// the video watched — a video the reader has not seen, filed away as seen, by a
// click that was meant to be a peek.
//
// Landing on a moment is not watching it. This buys the peek: nothing is written
// until the video has actually played on from where it landed, at which point
// the position is a real one and the 90% rule can do its job normally. It is a
// deliberately small number — the cost of getting it wrong in the other
// direction is a few seconds of resume position, not a wrongly-filed video.
const JUMP_SETTLE_SECONDS = 15;

// SLEEP_MAX_TICK_MS caps how much wall-clock time a single sleep-timer tick
// may charge to the budget. `timeupdate` fires ~4x/sec while playing, so a
// gap this large means playback stopped in between (a pause, a stall, a
// backgrounded tab) — time the viewer spent not watching, which a "stop after
// 30 minutes" promise has no business spending. See tickSleep.
//
// The trade this makes: where a browser throttles `timeupdate` to slower than
// one event per clamp, the budget drains slower than real time and the timer
// runs long rather than short. That is the safe direction to miss in — better
// to stop a few minutes late than to cut off a viewer who is still awake —
// and the clamp is what makes resuming after a pause correct at all.
const SLEEP_MAX_TICK_MS = 2000;

// fmt is the Task 17 alias for formatDuration used throughout the
// intelligence panels below (chapters/highlights/transcript cues) — kept as
// its own name to match the brief's `fmt(ts)` calls.
const fmt = formatDuration;

// Player — the "Now playing" view: an HTML5 <video> stage with a custom
// scrubber (SponsorBlock overlay + auto-skip), resume tracking, the
// favorite/watched/delete/"Watch on YouTube" action row, captions (Task 17),
// and the Summary/Contents/Highlights/Transcript intelligence panels from
// the approved Phase 3 mockup (mixed layout: Summary + Highlights sit beside
// the video in a sticky sidebar; Contents and the collapsible Transcript run
// full-width below it).
export function Player({
  videoId,
  seekTo,
  onSeekConsumed,
  onDeleted,
  onOpenChannel,
  onQueued,
  onMediaKnown,
  summaryOrigin,
  onBackFromSummary,
  inboxOrder,
  onOpenInboxVideo,
  visible = true,
  onNowPlaying,
  onPlaybackStarted,
  onPlaybackEnded,
  summaryEvent,
}: {
  videoId: string | null;
  // seekTo — the Task 18 jump-to-moment target (Search's onOpen, via App's
  // pendingSeek). Applied once in handleLoadedMetadata, in place of the
  // stored resume position, the same way resume itself is applied only
  // once per video load.
  seekTo?: number;
  // onSeekConsumed — fired exactly once, right after seekTo is applied in
  // handleLoadedMetadata. This makes the seek one-shot: without it, a stale
  // seekTo left set in the parent (e.g. App's pendingSeek) would replay and
  // yank the playhead back on any later remount of this component (say,
  // navigating away then back to "Now playing" via the rail), overriding
  // the user's real resume position. The parent should clear its stored
  // seek target from this callback.
  onSeekConsumed?: () => void;
  onDeleted: () => void;
  // onOpenChannel — optional: wired by App (Task 11), rendered as a channel
  // name link in Task 15.
  onOpenChannel?: (id: string) => void;
  // onQueued — tells App a re-download was queued, so the rail's Queue badge
  // and poll reflect it at once. Same reason as Library's: the video has just
  // left the ready-only library and the rail is the only thing that will say so.
  onQueued?: () => void;
  // onMediaKnown — reports whether the video this page opened has a file, once
  // the fetch says so. App records where a video was opened FROM before anyone
  // can know that, because a search result may be either; this is the answer
  // that lets it drop the marker for a video that turns out to be playable.
  onMediaKnown?: (id: string, hasMedia: boolean) => void;
  // summaryOrigin / onBackFromSummary — where this page was reached from, and
  // the way back there. Only ever used by the UnfetchedVideo branch: a video
  // with a file is being watched, and has no reading session to leave. Both are
  // absent for a video opened from the Library or from a cold link, which is
  // what stops that page offering a back link to somewhere the reader never was.
  summaryOrigin?: "inbox" | "search" | null;
  onBackFromSummary?: () => void;
  // inboxOrder / onOpenInboxVideo drive the Prev / Next stepper, and are only
  // ever used by the UnfetchedVideo branch. Passed only for a page reached from
  // the Inbox, and empty until the Inbox has been opened at least once — which
  // is why both are optional: a cold deep-link to a video, or a search result,
  // has no inbox position to step from.
  inboxOrder?: string[];
  onOpenInboxVideo?: (id: string) => void;
  // visible — whether this Player is the page currently on screen.
  //
  // It exists because the Player is no longer unmounted when you navigate
  // away: the <video> lives in its tree, and unmounting would stop playback,
  // which is the whole point of the now-playing dock. App hides it instead and
  // says so here, and the only thing that changes is where the video parks —
  // the stage while visible, the dock while not.
  visible?: boolean;
  // onNowPlaying — publishes what is playing (or null when nothing is) so the
  // dock can label itself. Fired once per video rather than per frame; see
  // NowPlaying. Must be stable, since it is an effect dependency.
  onNowPlaying?: (playing: NowPlaying | null) => void;
  // onPlaybackStarted — this video has actually begun playing.
  //
  // It is what the now-playing dock is gated on, and the reason opening a page
  // is not enough: walking into "Now playing", reading the summary and walking
  // out again should leave nothing behind. Fired on every play, not just the
  // first — App is setting the same id, which React bails out of.
  onPlaybackStarted?: (id: string) => void;
  // onPlaybackEnded — this video ran out on its own.
  //
  // It undoes what onPlaybackStarted claimed: the dock exists to say what you
  // are in the middle of, and a video that has finished is not that. Leaving
  // it docked would park a bar at 100% announcing something that is over, and
  // follow you around with it.
  //
  // Only reaching the end counts. Pausing does not, because a paused video is
  // still one you are part-way through.
  onPlaybackEnded?: (id: string) => void;
  // summaryEvent — the newest "summary" event off the session's one SSE
  // stream, forwarded by App. See the effect below for why this page no longer
  // opens a stream of its own. Events for other videos are handed over too and
  // ignored here; filtering upstream would mean App tracking which video each
  // page has open, which is the page's own business.
  summaryEvent?: { videoId: string; status: SummaryStatus } | null;
}) {
  const [video, setVideo] = useState<Video | null>(null);
  const [error, setError] = useState<string | null>(null);
  // App passes onMediaKnown as an inline arrow, so its identity changes every
  // render. Held in a ref so the effect that reports media presence depends on
  // the ANSWER changing, not on the parent re-rendering.
  const onMediaKnownRef = useRef(onMediaKnown);
  onMediaKnownRef.current = onMediaKnown;
  // toast — the transient notice over the video stage. It began as the
  // SponsorBlock skip message and now carries action failures too, hence the
  // icon and tone: tone drives the styling, so a failure stays red even if it
  // later picks a more specific icon, and an advisory that happens to use the
  // warning glyph doesn't turn red by accident. `error` is not an option for
  // failures — a non-null error replaces the whole player view (see the early
  // return below), and losing the video is worse than the failure itself.
  const [toast, setToast] = useState<{
    text: string;
    icon: IconName;
    tone: "info" | "warn";
  } | null>(null);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [ccOn, setCcOn] = useState(false);
  // subtitlesDefault is the global "show subtitles by default" preference
  // (settings.subtitles_default). null means "not loaded yet" — distinct
  // from false, because the effect below must not apply a default it hasn't
  // actually read, or every video would flash captions-off first.
  const [subtitlesDefault, setSubtitlesDefault] = useState<boolean | null>(
    null,
  );
  // directStream is the global "allow direct playback links" preference
  // (settings.direct_stream_enabled). Like subtitlesDefault, null means "not
  // loaded yet" — and here that distinction is load-bearing: the <video> gets
  // no src until we know which kind of URL to use, so it can never mount with
  // the session URL and then swap to a grant, which would reload the media.
  const [directStream, setDirectStream] = useState<boolean | null>(null);
  // grant is the minted direct-playback URL, stamped with the video it was
  // minted for. The id is not decoration: the <video> unmounts and remounts on
  // every video change, and without it the new video's element would mount
  // carrying the *previous* video's URL for as long as the next mint takes —
  // long enough for the old media (already warm in the cache) to fire
  // loadedmetadata against the new video's state, which sets resumeAppliedRef
  // and costs the new video its resume position. null until minted.
  const [grant, setGrant] = useState<{ id: string; url: string } | null>(null);
  // confirmDelete drives the delete confirmation modal (ConfirmDialog),
  // opened from the ⋮ menu; deleting is its in-flight busy flag.
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [reprocessing, setReprocessing] = useState(false);
  const [redownloading, setRedownloading] = useState(false);
  // Share state: whether this video currently has a live public link, driving
  // the "Shared" chip beside the title. It loads alongside the video and is
  // updated in place by the share popover.
  const [shareStatus, setShareStatus] = useState<ShareStatus>({
    shared: false,
  });
  // The share popover lives in ShareControl but is opened from the ⋮ menu, so
  // the Player owns the flag that connects them.
  const [shareOpen, setShareOpen] = useState(false);
  // subtitlesReadyFor holds the video id whose metadata has loaded, gating
  // the <track> below. On iPadOS 27 (public beta 1) a <track> child present
  // while the video loads makes Safari fail resource selection outright —
  // networkState 3 (NETWORK_NO_SOURCE), readyState 0, video.error stays
  // null — so the player sits on the poster at 0:00 forever with nothing
  // detectable from JS. Mounting the track only after loadedmetadata loads
  // the media fine and keeps captions working. Keyed on the video id rather
  // than a boolean so switching videos starts track-free again without a
  // reset. See tubearchivist/tubearchivist#1196.
  const [subtitlesReadyFor, setSubtitlesReadyFor] = useState<string | null>(
    null,
  );
  const videoRef = useRef<HTMLVideoElement>(null);
  // stageSlotRef is the empty box on the player page the shared <video> parks
  // into. The element is not a child of this component's tree in the DOM sense
  // — it is portalled into videoHost's node, which this effect relocates — so
  // the stage renders a slot rather than the video itself.
  const stageSlotRef = useRef<HTMLDivElement>(null);
  const lastSentRef = useRef(0);
  // positionRef tracks the latest known playhead position independent of
  // the <video> DOM node itself. On unmount, React nulls out videoRef
  // *before* this effect's cleanup runs, so reading videoRef.current there
  // is unreliable — positionRef, updated on every timeupdate, is not.
  const positionRef = useRef(0);
  // positionRef starts at 0, which is indistinguishable from "the user
  // really is at 0:00" — without this guard, flushing on unmount before
  // loadedMetadata/timeupdate has ever set a real position would overwrite
  // a legitimately stored resume_position_seconds with 0. Only flush once
  // a real position has been observed.
  const positionKnownRef = useRef(false);
  // stateVersionRef is the video's state_version as this Player last saw it,
  // echoed on every resume POST so a watched toggle made in another tab or on
  // another device can't be undone by this client writing its stale position
  // back (issue #97). null means "not known yet" — a ping before the video has
  // loaded sends no version and is checked server-side as before, which is
  // correct: there is no stale read to guard against yet.
  //
  // It is refreshed from EVERY response that reports a version, not just
  // getVideo: the resume POST's own >=90% auto-watch bumps it, so a client
  // refreshing only from getVideo would 409 against its own threshold crossing.
  const stateVersionRef = useRef<number | null>(null);
  const resumeAppliedRef = useRef(false);
  // Where a jump put the playhead, or null when this page was not opened at a
  // moment. While it is set, no resume position is written: see
  // JUMP_SETTLE_SECONDS. Cleared once the video has played on from there, and by
  // the video ending — a video watched to its end is watched however little of
  // it was played after the jump.
  const jumpAnchorRef = useRef<number | null>(null);
  // ccAppliedForRef holds the video id the subtitles default was last
  // applied to, so it lands exactly once per video. Without it the toggle
  // and the default-applier fight: toggling also updates subtitlesDefault
  // (it *is* the preference), which re-runs that effect and would otherwise
  // immediately re-apply the default on top of the user's click.
  const ccAppliedForRef = useRef<string | null>(null);
  const toastTimerRef = useRef<number | undefined>(undefined);
  // openVideoIdRef is which video this Player currently has open, readable
  // from an async continuation that resumed after the answer changed — null
  // once the component unmounts. `videoId` itself can't do that job: a handler
  // closes over the value from the render it was created in, so it keeps
  // claiming the old video forever. Anything that touches state, the playhead
  // or the toast *after* an await must compare against this first.
  const openVideoIdRef = useRef<string | null>(null);
  // Sleep timer — "stop playing in N minutes", for watching in bed.
  //
  // It is a budget of milliseconds drained by wall-clock deltas, not a
  // setTimeout: a long timeout is throttled in a background tab and does not
  // survive the machine sleeping, whereas a budget re-read on every tick
  // cannot arrive late by more than one tick. The draining happens inside
  // handleTimeUpdate, which fires only while the video is actually playing —
  // so "30 minutes" means 30 minutes of video, and pausing to answer the door
  // pauses the timer too, with no `paused` check needed anywhere.
  //
  // The ref is the authority and is written synchronously; the state exists
  // only so the pill re-renders, and holds whole seconds so a 4Hz timeupdate
  // re-renders at most once a second.
  const sleepRemainingRef = useRef<number | null>(null);
  const sleepLastTickRef = useRef(0);
  const [sleepRemaining, setSleepRemaining] = useState<number | null>(null);
  // Which preset is armed, so the menu's radio state is honest. The budget
  // alone can't say — a minute in, 30 and 60 look the same shape.
  const [sleepMinutes, setSleepMinutes] = useState<number | null>(null);

  useEffect(() => {
    resumeAppliedRef.current = false;
    positionKnownRef.current = false;
    stateVersionRef.current = null;
    // Belongs to the video that was jumped into, so it goes with that video.
    // Left set, it would suppress the next video's resume writes until that one
    // happened to pass the same mark.
    jumpAnchorRef.current = null;
    openVideoIdRef.current = videoId;
    setVideo(null);
    setError(null);
    setToast(null);
    setCurrentTime(0);
    setDuration(0);
    setCcOn(false);
    setConfirmDelete(false);
    setShareStatus({ shared: false });
    // A sleep timer is a promise about the video in front of you, so opening
    // another one cancels it rather than quietly carrying over.
    sleepRemainingRef.current = null;
    setSleepRemaining(null);
    setSleepMinutes(null);
    if (!videoId) return;
    let active = true;
    getVideo(videoId)
      .then((v) => {
        if (!active) return;
        setVideo(v);
        stateVersionRef.current = v.state_version;
      })
      .catch((e: Error) => {
        if (active) setError(e.message);
      });
    // Share status is a best-effort side load: a failure just leaves the
    // "not shared" default, never blocking the player.
    getShareStatus(videoId)
      .then((s) => {
        if (active) setShareStatus(s);
      })
      .catch(() => {});
    return () => {
      active = false;
      // Cleared on unmount as well as on a video change; the next run of this
      // effect sets the new id back immediately, so only a real teardown
      // leaves it null.
      openVideoIdRef.current = null;
    };
  }, [videoId]);

  // Record this video as "now playing" server-side, so the rail opens it on
  // every device instead of only in the tab that started it. Its own effect
  // rather than a line in the reset effect above: that one is about clearing
  // this component's state, and entangling a network write with it would put a
  // write inside the same early-return dance.
  //
  // One write per video opened — deliberately not folded into the resume ping,
  // where it would be hundreds of identical writes an hour for a value that
  // does not change between them. Best-effort: a failure must never touch
  // playback, and the worst case is a rail that behaves as it did before this
  // existed. Nothing clears the pointer on unmount either — navigating to the
  // Library does not mean you stopped watching.
  //
  // Only for a video with a file, which is why this waits for the fetched video
  // rather than firing on the id alone. The pointer is a single row the read
  // side joins with status='downloaded' (playback.Store), so writing a fileless
  // video into it does not move the rail to that video — it empties the rail.
  // Opening a tombstoned page to read its transcript is not "you stopped
  // watching the thing you were watching".
  useEffect(() => {
    if (!video?.id || !video.has_media) return;
    setPlaybackState(video.id).catch(() => {});
  }, [video?.id, video?.has_media]);

  // showsStage mirrors the render below: a video with a file, on a page that
  // is actually on screen. Everything the early returns catch — no id, a load
  // error, a video peeq has read but not downloaded — draws no stage, and a
  // hidden Player draws one that nobody can see.
  const showsStage =
    visible &&
    !error &&
    !!video &&
    video.status !== "new" &&
    video.has_media &&
    !!videoId;

  // Park the video on the stage while this page is showing it, and release it
  // when it is not — the dock picks it up from there (videoHost prefers the
  // stage whenever both are registered, so the handover needs no coordination
  // between the two components).
  //
  // The cleanup releases rather than parks: on a real unmount there is no
  // stage left, and leaving a stale node registered would have videoHost keep
  // appending the host into a detached box, which is exactly the
  // removed-from-the-document pause this whole mechanism exists to avoid.
  useEffect(() => {
    if (!showsStage) {
      park("stage", null);
      return;
    }
    park("stage", stageSlotRef.current);
    return () => park("stage", null);
  }, [showsStage]);

  // parkedAt drives the native controls: full transport on the stage, none in
  // a 100x56 dock tile where they would cover the picture entirely and offer
  // hit targets smaller than a fingertip. The dock draws its own.
  const parkedAt = useParkedAt();

  // Coming back to the page, catch the scrubber up in one go. handleTimeUpdate
  // stops tracking the playhead in state while hidden (see there), and a video
  // that was PAUSED from the dock fires no timeupdate to correct it — so
  // without this the bar would sit at whatever position you left the page at.
  useEffect(() => {
    if (!visible) return;
    const el = videoRef.current;
    if (el) setCurrentTime(el.currentTime);
  }, [visible]);

  // Tell App what is playing, so the dock can name it. Guarded the same way
  // the now-playing pointer is — a fileless page is not "what you are
  // watching" — and it reports null in that case so a dock left over from a
  // previous video does not outlive it.
  useEffect(() => {
    if (!onNowPlaying) return;
    if (!video || !video.has_media || video.status === "new") {
      onNowPlaying(null);
      return;
    }
    onNowPlaying({
      id: video.id,
      title: video.title,
      channelName: video.channel_name,
      durationSeconds: video.duration_seconds ?? 0,
      segments: video.sponsorblock_segments ?? [],
    });
    // On `video` as a whole rather than its fields: it is replaced wholesale
    // on every change (the SSE refetch, the optimistic toggles), so this fires
    // when any published field moves and stays put when none does.
  }, [onNowPlaying, video]);

  // Tell the shell whether what it navigated to has a file. It records the
  // origin of a page before that is knowable — a search result may be a video
  // to watch or a summary to read — and this is what settles it.
  //
  // It is also what keeps two Players from rendering a <video> into the one
  // shared host: a summary page whose download lands stops being a summary,
  // and the page is handed to the Player that owns playback.
  useEffect(() => {
    if (!video?.id) return;
    onMediaKnownRef.current?.(video.id, !!video.has_media);
  }, [video?.id, video?.has_media]);

  // Flush the resume position immediately on tab-hide/unload, so the
  // RESUME_THROTTLE_MS window never costs more than itself worth of
  // progress even if the tab is closed mid-throttle. The cleanup function
  // also flushes on unmount — e.g. clicking back to Library, the common
  // in-SPA exit, which would otherwise silently discard up to
  // RESUME_THROTTLE_MS worth of progress. Both paths read positionRef
  // (not videoRef) so they always send the latest playhead position, even
  // once React has already detached the <video> node's ref on unmount.
  useEffect(() => {
    function flush() {
      if (!video || !positionKnownRef.current) return;
      // A jump nobody played on from is not progress worth saving — and past the
      // 90% line it is worse than nothing, since the server would file the video
      // as watched (JUMP_SETTLE_SECONDS). This is the path that used to do it:
      // land on a moment near the end, leave at once, and the unmount flush
      // marked it.
      if (jumpAnchorRef.current !== null) return;
      // No 409 branch here, deliberately: this also runs from the unmount
      // cleanup, where there is no component left to toast and no playhead left
      // to rewind. A refused flush simply means the position the server already
      // holds is the right one.
      //
      // An ACCEPTED flush must still adopt the version it hands back, exactly
      // like the throttled ping does: past the 90% threshold this write
      // auto-marks watched server-side and bumps state_version, so dropping the
      // response would leave the ref stale and make the very next ping 409
      // against this Player's own flush — pausing, rewinding to 0:00 and
      // claiming the video was "marked watched on another device". Guarded on
      // openVideoIdRef so a late response can't write this video's version into
      // the ref after the user has moved to another one.
      const id = video.id;
      setResume(id, positionRef.current, stateVersionRef.current ?? undefined)
        .then((res) => {
          if (openVideoIdRef.current !== id) return;
          stateVersionRef.current = res.state_version;
        })
        .catch(() => {});
    }
    document.addEventListener("visibilitychange", flush);
    window.addEventListener("pagehide", flush);
    return () => {
      document.removeEventListener("visibilitychange", flush);
      window.removeEventListener("pagehide", flush);
      flush();
    };
  }, [video]);

  useEffect(() => {
    return () => {
      if (toastTimerRef.current !== undefined) {
        window.clearTimeout(toastTimerRef.current);
      }
    };
  }, []);

  // Live summary status (Task 10): the initial getVideo load above only
  // captures summary_status at mount time — without this, a "Summarizing…"
  // placeholder would sit frozen until the user manually reloaded, even
  // though the backend (Task 8) is already pushing "summary" SSE events
  // {video_id, status, phase} on every phase transition.
  //
  // The event arrives as a PROP rather than from a subscription of this
  // component's own. App is already on that stream and already decodes these
  // very events for the Queue lane, so a second connection here re-decoded
  // bytes that had been decoded a moment earlier — and, now that the
  // now-playing dock keeps this component mounted for the whole session, held
  // that second connection open for the whole session with it.
  //
  // It fires on ARRIVAL, not on change: App hands over a new object per event,
  // so two events carrying the same status both land.
  useEffect(() => {
    if (!videoId || summaryEvent?.videoId !== videoId) return;
    if (summaryEvent.status !== "done") {
      const status = summaryEvent.status;
      setVideo((prev) => (prev ? { ...prev, summary_status: status } : prev));
      return;
    }
    // Refetch to pull the finished summary/chapters/key-points. Guarded
    // against a stale videoId change (v1 -> v2) racing this refetch in,
    // which would otherwise overwrite v2's video with v1's data.
    let cancelled = false;
    getVideo(videoId)
      .then((v) => {
        if (cancelled) return;
        setVideo(v);
        stateVersionRef.current = v.state_version;
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [videoId, summaryEvent]);

  // Load the global subtitles preference once per mount. A failure is not
  // fatal — playback must work even if settings can't be read — so it falls
  // back to "off", the behaviour peeq had before this was a setting.
  useEffect(() => {
    let active = true;
    getSettings()
      .then((s) => {
        if (!active) return;
        setSubtitlesDefault(s.subtitles_default);
        setDirectStream(s.direct_stream_enabled);
      })
      .catch(() => {
        if (!active) return;
        setSubtitlesDefault(false);
        setDirectStream(false);
      });
    return () => {
      active = false;
    };
  }, []);

  // Mint a grant per video while direct playback is on. AirPlay hands the src
  // to the Apple TV, which fetches it with no session cookie, so the src has to
  // already be auth-free by the time the built-in AirPlay button is pressed —
  // that button lives inside Safari's native controls and cannot be
  // intercepted.
  //
  // A failed mint stores the session URL rather than failing playback: the
  // worst case is that AirPlay does not work, which is exactly where the user
  // was before turning the setting on.
  // Nothing to mint a grant for without a file: the endpoint only issues one
  // for a downloaded video, so a fileless page would spend a request to be
  // refused and fall back to a stream URL no <video> is going to ask for.
  useEffect(() => {
    if (!video?.id || !video.has_media || !directStream) return;
    const id = video.id;
    let active = true;
    createPlaybackGrant(id)
      .then((g) => {
        if (active) setGrant({ id, url: g.url });
      })
      .catch(() => {
        if (active) setGrant({ id, url: streamUrl(id) });
      });
    return () => {
      active = false;
    };
  }, [video?.id, video?.has_media, directStream]);

  // playbackSrc is the URL the <video> actually plays, derived during render
  // rather than held in state: the element remounts whenever the open video
  // changes, and a state-held URL would still be the previous video's for the
  // first commit after that remount. undefined means "no src yet" — either the
  // preference has not loaded or the grant for *this* video has not landed —
  // which shows the poster and nothing else. With direct playback off this is
  // the same session-gated URL peeq has always used.
  const playbackSrc =
    !video || directStream === null
      ? undefined
      : directStream
        ? grant?.id === video.id
          ? grant.url
          : undefined
        : streamUrl(video.id);

  // Hide Safari's AirPlay button when direct playback is off, rather than
  // leaving a control that would hand the receiver a URL it cannot fetch and
  // fail with no explanation. Set imperatively because disableRemotePlayback is
  // an IDL property; x-webkit-airplay is the older Safari spelling of the same
  // intent, and setting both covers the versions that only honour one.
  useEffect(() => {
    const el = videoRef.current;
    if (!el || directStream === null) return;
    el.disableRemotePlayback = !directStream;
    el.setAttribute("x-webkit-airplay", directStream ? "allow" : "deny");
  }, [directStream, playbackSrc]);

  // Apply the subtitles preference once per video, whenever both the
  // preference and the <track> are available (either can land first). The
  // ccAppliedForRef guard is what keeps this from stomping on a mid-video
  // toggle — see the ref's declaration.
  useEffect(() => {
    const el = videoRef.current;
    if (!el || !video?.has_subtitles || subtitlesDefault === null) return;
    if (ccAppliedForRef.current === video.id) return;
    const track = el.textTracks[0];
    // No track yet: leave the ref unstamped so a <track> that mounts later
    // still gets the default applied on a subsequent run.
    if (!track) return;
    track.mode = subtitlesDefault ? "showing" : "hidden";
    setCcOn(subtitlesDefault);
    ccAppliedForRef.current = video.id;
    // subtitlesReadyFor is a required dependency, not a tidy-up: the <track>
    // now mounts only once metadata has loaded, and nothing else here changes
    // at that moment. Without it this effect would run early, find no track,
    // return unstamped — and never run again, so a "subtitles on by default"
    // preference would silently never apply.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [video?.id, video?.has_subtitles, subtitlesDefault, subtitlesReadyFor]);

  if (!videoId) {
    return (
      <p style={{ color: "var(--color-faint)" }}>
        Nothing playing. Pick a video from the Library.
      </p>
    );
  }
  if (error) {
    return <div className="errline">{error}</div>;
  }
  if (!video) {
    return (
      <p
        style={{
          display: "flex",
          alignItems: "center",
          gap: 8,
          color: "var(--color-faint)",
        }}
      >
        <Spinner size="15px" />
        Loading
      </p>
    );
  }

  // A video peeq has read but not downloaded takes a different page entirely.
  //
  // The branch sits here, after the loading and error returns, because
  // everything above it is about GETTING the video and applies either way.
  // Everything below is about playing one, and none of it can run: there is no
  // media to seek, no position to resume, no stream to grant.
  //
  // Same route on purpose. /video/<id> is the video's address whether or not
  // peeq holds the file, so downloading it changes the page under a URL that
  // does not move.
  if (video.status === "new") {
    return (
      <UnfetchedVideo
        video={video}
        onBack={onBackFromSummary}
        backLabel={
          summaryOrigin === "search" ? "Back to search" : "Back to inbox"
        }
        onQueued={onQueued}
        onDismissed={onBackFromSummary}
        inboxOrder={inboxOrder}
        onOpenInboxVideo={onOpenInboxVideo}
        onOpenChannel={onOpenChannel}
      />
    );
  }

  const segments = video.sponsorblock_segments ?? [];

  function handleLoadedMetadata() {
    const el = videoRef.current;
    // Open the subtitle gate first, ahead of the resumeAppliedRef guard, so
    // it still opens if this fires more than once on the same media.
    if (video) setSubtitlesReadyFor(video.id);
    if (!el || !video || resumeAppliedRef.current) return;
    resumeAppliedRef.current = true;
    setDuration(el.duration);
    // el.duration is NaN until real media metadata loads (e.g. under
    // jsdom, or before the stream responds) — only use it to *withhold* a
    // too-far resume once it's a known finite number; a not-yet-known
    // duration must never block applying resume.
    const durationKnown = Number.isFinite(el.duration);
    if (seekTo !== undefined && (!durationKnown || seekTo < el.duration)) {
      el.currentTime = seekTo;
      setCurrentTime(seekTo);
      positionRef.current = seekTo;
      // Landing here is not watching from here. Nothing is stored until the
      // video plays on from this point — see JUMP_SETTLE_SECONDS.
      jumpAnchorRef.current = seekTo;
      onSeekConsumed?.();
    } else if (
      video.resume_position_seconds > 0 &&
      (!durationKnown || video.resume_position_seconds < el.duration)
    ) {
      el.currentTime = video.resume_position_seconds;
      setCurrentTime(video.resume_position_seconds);
      positionRef.current = video.resume_position_seconds;
    }
    // Metadata having loaded means el.currentTime now reflects a real
    // position (either the applied resume above, or genuinely 0:00) —
    // safe for the unmount/hide flush to trust from here on.
    positionKnownRef.current = true;
  }

  // showToast puts a message over the stage for a few seconds. A later toast
  // replaces an earlier one, so the timer is always reset rather than stacked
  // — two overlapping notices would otherwise leave the second one dismissed
  // early by the first one's timeout.
  //
  // Callers reached from an async continuation must check openVideoIdRef
  // first: a toast raised after the user has moved on both misattributes the
  // message and schedules a timer the unmount cleanup can no longer clear.
  function showToast(text: string, icon: IconName, tone: "info" | "warn") {
    setToast({ text, icon, tone });
    if (toastTimerRef.current !== undefined) {
      window.clearTimeout(toastTimerRef.current);
    }
    toastTimerRef.current = window.setTimeout(() => setToast(null), 2600);
  }

  // armSleep is the SleepTimer pill's only entry point: minutes to start a
  // fresh budget, null to call it off. Re-picking the running preset restarts
  // it rather than adding to it, which is what "set it for 30" means the
  // second time as much as the first.
  function armSleep(minutes: number | null) {
    if (minutes === null) {
      disarmSleep();
      showToast("Sleep timer off", "clock", "info");
      return;
    }
    sleepRemainingRef.current = minutes * 60_000;
    sleepLastTickRef.current = Date.now();
    setSleepRemaining(minutes * 60);
    setSleepMinutes(minutes);
    showToast(`Sleep timer set for ${minutes} min`, "clock", "info");
  }

  function disarmSleep() {
    sleepRemainingRef.current = null;
    setSleepRemaining(null);
    setSleepMinutes(null);
  }

  // tickSleep drains the sleep budget by however much wall-clock time has
  // passed since the last tick, and pauses when it runs out.
  //
  // The delta is clamped, and handlePlay re-bases the mark, because
  // sleepLastTickRef only means anything across *continuous* playback. Pause
  // for ten minutes with five left on the clock, and an unclamped first tick
  // after resuming would charge the whole idle gap to the budget and stop the
  // video the instant it started again — the timer firing at exactly the
  // moment the user said they wanted to keep watching.
  function tickSleep(el: HTMLVideoElement) {
    const left = sleepRemainingRef.current;
    if (left === null) return;
    const now = Date.now();
    // Clamped at both ends. The ceiling is SLEEP_MAX_TICK_MS (see above); the
    // floor is zero because Date.now() is wall clock and can step backwards —
    // an NTP correction or a manual clock change mid-playback would otherwise
    // make the delta negative and *credit* the budget, leaving the pill
    // showing more time than the preset was ever armed for.
    const spent = Math.min(
      Math.max(now - sleepLastTickRef.current, 0),
      SLEEP_MAX_TICK_MS,
    );
    const next = left - spent;
    sleepLastTickRef.current = now;
    if (next > 0) {
      sleepRemainingRef.current = next;
      const secs = Math.ceil(next / 1000);
      // Same-value bailout: timeupdate is ~4Hz and the readout is whole
      // seconds, so three ticks in four have nothing to repaint.
      setSleepRemaining((cur) => (cur === secs ? cur : secs));
      return;
    }
    disarmSleep();
    el.pause();
    // Zeroing lastSentRef makes the throttled resume POST further down this
    // same handleTimeUpdate fire unthrottled, so the pause point is stored
    // rather than waiting out a window whose next tick is never coming.
    // Deliberately not a second flush helper: the block below carries the
    // openVideoIdRef guard, the state_version adoption and the 409 ->
    // handleStaleState path that issue #97 exists to protect.
    lastSentRef.current = 0;
    showToast("Paused by sleep timer", "clock", "info");
  }

  // The mark is only valid across continuous playback — see tickSleep.
  function handlePlay() {
    sleepLastTickRef.current = Date.now();
    // Playback starting is what makes this video the one being watched — see
    // onPlaybackStarted. Deliberately here and not on the page's load: a page
    // that was opened and left is not something to carry around.
    if (video?.id) onPlaybackStarted?.(video.id);
  }

  // A video that runs out on its own ends the session the timer was counting
  // down: nothing will fire timeupdate again, so leaving it armed would park
  // a live-looking countdown that can never tick.
  function handleEnded() {
    if (sleepRemainingRef.current !== null) disarmSleep();
    // Reaching the end is watching it, however short the run-up. Without this a
    // jump to a moment inside the last JUMP_SETTLE_SECONDS would suppress the
    // one write that legitimately marks the video watched.
    jumpAnchorRef.current = null;
    // A video that has run out is no longer one you are in the middle of, so
    // it stops being what playback carries — see onPlaybackEnded. Pressing
    // play again adopts it back, through handlePlay, like any other video.
    if (video?.id) onPlaybackEnded?.(video.id);
  }

  function handleTimeUpdate() {
    const el = videoRef.current;
    if (!el || !video) return;
    // Checked first, and before the SponsorBlock loop below can break out of
    // the handler: the budget must not skip a tick because a segment did.
    tickSleep(el);
    // Only while this page is on screen. currentTime feeds exactly one thing —
    // the Scrubber — and timeupdate fires ~4x/sec, so setting it while hidden
    // would re-render the whole player page (summary, chapters, transcript)
    // four times a second behind whatever the user is actually looking at.
    // That cost did not exist while navigating away unmounted the Player, and
    // it must not arrive with the dock. positionRef below is NOT gated: the
    // resume flush reads it, and that has to stay right wherever you are.
    if (visible) setCurrentTime(el.currentTime);
    positionRef.current = el.currentTime;
    positionKnownRef.current = true;
    // The jump has been played through: from here the playhead means what it
    // normally means. Any movement away from the landing point counts, in either
    // direction — scrubbing off it is the reader taking charge of the position
    // just as much as letting it run is.
    if (
      jumpAnchorRef.current !== null &&
      Math.abs(el.currentTime - jumpAnchorRef.current) >= JUMP_SETTLE_SECONDS
    ) {
      jumpAnchorRef.current = null;
    }

    // SponsorBlock auto-skip: jump past whichever AUTO_SKIP segment the
    // playhead has just entered. Segments outside that set (intros, outros,
    // recaps, non-music sections) are drawn on the scrubber but play — cutting
    // them without being asked removes video the viewer may well want. A video
    // with no segments makes this a no-op.
    for (const seg of segments) {
      if (!AUTO_SKIP.has(seg.category)) continue;
      if (el.currentTime >= seg.start_time && el.currentTime < seg.end_time) {
        el.currentTime = seg.end_time;
        if (visible) setCurrentTime(seg.end_time);
        positionRef.current = seg.end_time;
        showToast(
          `Skipped ${categoryLabel(seg.category)}${DOT}${formatDuration(seg.end_time - seg.start_time)}`,
          "skipForward",
          "info",
        );
        break;
      }
    }

    const now = Date.now();
    if (
      jumpAnchorRef.current === null &&
      now - lastSentRef.current >= RESUME_THROTTLE_MS
    ) {
      lastSentRef.current = now;
      const id = video.id;
      setResume(id, el.currentTime, stateVersionRef.current ?? undefined)
        .then((res) => {
          if (openVideoIdRef.current !== id) return;
          stateVersionRef.current = res.state_version;
        })
        .catch((e: unknown) => {
          if (e instanceof ApiError && e.status === 409) {
            void handleStaleState(id);
          }
        });
    }
  }

  // handleStaleState answers a 409 from a resume ping: the video's watched state
  // changed somewhere this Player never saw, so its position was refused (see
  // setResume and issue #97). Refetch, adopt the real state, and — only if the
  // video came back watched — do exactly what the local toggle does: stop and
  // rewind, so nothing keeps pushing the old position at a row that has
  // deliberately been zeroed.
  //
  // Every step is guarded on openVideoIdRef, the rule the toggle and category
  // handlers already follow: a continuation that resumes after the user has
  // moved on must not paint this video's state onto whichever one is open now.
  async function handleStaleState(id: string) {
    if (openVideoIdRef.current !== id) return;
    let fresh: Video;
    try {
      fresh = await getVideo(id);
    } catch {
      // Nothing to adopt. The next ping will 409 again and retry this.
      return;
    }
    if (openVideoIdRef.current !== id) return;
    setVideo(fresh);
    stateVersionRef.current = fresh.state_version;
    if (!fresh.watched) {
      // A conflict that wasn't a mark-watched (an un-watch, a re-download's
      // rescue). The refreshed version is enough; there is no reason to yank a
      // playhead the user is still watching.
      return;
    }
    videoRef.current?.pause();
    seek(0);
    positionRef.current = 0;
    positionKnownRef.current = false;
    showToast("Marked watched on another device.", "check", "info");
  }

  // seek — shared by the scrubber and every intelligence-panel click target
  // (chapters, highlights, transcript cues): sets the <video>'s currentTime
  // directly and keeps the state/positionRef bookkeeping in sync.
  function seek(seconds: number) {
    const el = videoRef.current;
    if (!el) return;
    el.currentTime = seconds;
    setCurrentTime(seconds);
    positionRef.current = seconds;
    positionKnownRef.current = true;
  }

  async function handleToggleFavorite() {
    if (!video) return;
    const id = video.id;
    const next = !video.favorite;
    setVideo({ ...video, favorite: next });
    try {
      await setFavorite(id, next);
    } catch {
      // Same-video guard as handlePickCategory below: a write that fails
      // after the user has moved on must not paint this video's favorite
      // state onto the one now open.
      setVideo((v) => (v && v.id === id ? { ...v, favorite: !next } : v));
    }
  }

  // Optimistic, like the favorite and watched toggles: the pill moves at
  // once and rolls back if the write fails, since a category is cheap to
  // re-pick and a spinner on a one-word change reads as friction.
  async function handlePickCategory(next: string) {
    if (!video) return;
    const id = video.id;
    const prev = video.category;
    setVideo({ ...video, category: next });
    try {
      await setCategory(id, next);
    } catch {
      // Roll back only if this is still the same video: a write that fails
      // after the user has moved on must not paint the old video's category
      // onto the new one.
      setVideo((v) => (v && v.id === id ? { ...v, category: prev } : v));
    }
  }

  // handleToggleWatched — either direction is a deliberate "I'm done with this
  // for now", so it stops playback and puts everything back to 0:00: the
  // server zeroes resume_position_seconds (see videos.SetWatched), the local
  // copy follows, and the playhead itself rewinds.
  //
  // Pausing is what makes that stick. A video left playing keeps firing
  // timeupdate, and handleTimeUpdate would POST the new position within
  // RESUME_THROTTLE_MS — writing back the position the server just cleared,
  // and past the 90% mark re-crossing SetResume's auto-watched check, which
  // silently undoes an un-watch.
  //
  // positionRef/positionKnownRef are reset for the same reason, but note they
  // do not stay reset: setting currentTime queues a timeupdate (the spec fires
  // it on a seek even while paused), and handleTimeUpdate flips
  // positionKnownRef back to true. That is harmless *because the position it
  // then reports is 0* — a flush of 0 matches what the server already stored,
  // and 0 can never re-cross the 90% threshold. Anything that changes this
  // function to leave a non-zero playhead behind has to revisit that.
  //
  // The sleep timer goes off with it, for the same reason handleEnded disarms:
  // the timer counts down a sitting, and this button is how you say the sitting
  // is over. Leaving it armed would park a countdown against a paused video at
  // 0:00 — one that cannot tick, since tickSleep only runs on timeupdate — and
  // then fire "Paused by sleep timer" over whatever you started watching next.
  async function handleToggleWatched() {
    if (!video) return;
    const id = video.id;
    const next = !video.watched;
    const previousPosition = video.resume_position_seconds;
    const el = videoRef.current;
    // Captured for the rollback below: a failed request must leave the player
    // exactly as it found it, not paused at 0:00 with the old state restored.
    const wasPlaying = el ? !el.paused : false;
    const previousPlayhead = el ? el.currentTime : 0;
    const previousSleepMs = sleepRemainingRef.current;
    const previousSleepMinutes = sleepMinutes;

    setVideo({ ...video, watched: next, resume_position_seconds: 0 });
    el?.pause();
    seek(0);
    positionRef.current = 0;
    positionKnownRef.current = false;
    if (previousSleepMs !== null) {
      disarmSleep();
      // Said out loud: the pill is one control away from the button just
      // pressed, and a countdown that vanishes on its own reads as a glitch.
      showToast("Sleep timer off", "clock", "info");
    }

    try {
      const res = await setWatched(id, next);
      // Same-video guard as the rollback below. The ref belongs to whichever
      // video is open now, so writing this one's version into it would 409 the
      // next ping for the other video.
      if (openVideoIdRef.current === id) {
        stateVersionRef.current = res.state_version;
      }
    } catch {
      // Nothing below is safe once the user has moved on — the same rule
      // handlePickCategory follows. The rollback would paint this video's
      // watched flag and position onto whichever video is open now, and
      // seek() would drag that video's playhead to this one's timestamp,
      // where the next resume ping would persist it.
      if (openVideoIdRef.current !== id) return;
      setVideo((v) =>
        v && v.id === id
          ? { ...v, watched: !next, resume_position_seconds: previousPosition }
          : v,
      );
      // Undo the playhead move too. Without this, a failed toggle leaves the
      // video sitting at 0:00 while the restored state still claims the old
      // position — and the next resume ping would persist that 0.
      seek(previousPlayhead);
      // And re-arm the timer at the budget it had, not at the preset: a
      // half-spent 30 must not come back as a fresh 30. sleepLastTickRef is
      // re-based here rather than left alone because the mark only means
      // anything across continuous playback — the same reason handlePlay
      // re-bases it — and the request took real time to fail.
      if (previousSleepMs !== null) {
        sleepRemainingRef.current = previousSleepMs;
        sleepLastTickRef.current = Date.now();
        setSleepRemaining(Math.ceil(previousSleepMs / 1000));
        setSleepMinutes(previousSleepMinutes);
      }
      // play() returns a promise that can reject (autoplay policy), and
      // returns nothing at all under jsdom — resuming is a nicety on an
      // already-failed path, so neither case may surface.
      if (wasPlaying) void el?.play()?.catch(() => {});
      // Say so. Without this the whole thing reads as a button that did
      // nothing: the label flips back, the video jumps and returns, and the
      // user is left guessing whether they misclicked.
      showToast(
        next ? "Couldn't mark watched." : "Couldn't mark unwatched.",
        "warning",
        "warn",
      );
    }
  }

  // handleDelete runs after the ConfirmDialog is confirmed. Deleting is
  // irreversible, so the modal is the guard a single menu click no longer is.
  async function handleDelete() {
    if (!video) return;
    setDeleting(true);
    try {
      await deleteVideo(video.id);
      onDeleted();
    } catch (e) {
      setError((e as Error).message);
      setDeleting(false);
      setConfirmDelete(false);
    }
  }

  // Subtitles toggle — flips the <track>'s TextTrack.mode between 'showing'
  // and 'hidden' directly (imperative, mirroring the native captions
  // button), keeping ccOn in sync for the button's "on" styling.
  //
  // The flip is also written back as the global preference, which is what
  // makes it stick: the next video opens the way this one was left. The
  // write is fire-and-forget with no rollback — what the user just clicked
  // is already visible on this video, and a failed write only means the
  // choice doesn't carry over.
  function handleToggleCC() {
    const el = videoRef.current;
    const track = el?.textTracks?.[0];
    let next: boolean;
    if (track) {
      track.mode = track.mode === "showing" ? "hidden" : "showing";
      next = track.mode === "showing";
    } else {
      next = !ccOn;
    }
    setCcOn(next);
    setSubtitlesDefault(next);
    updateSettings({ subtitles_default: next }).catch(() => {});
  }

  async function handleReprocess() {
    // Guard on reprocessing as well as video: the menu closes on click, so a
    // user who reopens it and clicks again before the request resolves would
    // otherwise fire a second POST and enqueue a duplicate summary job (the
    // backend Enqueue has no dedup).
    if (!video || reprocessing) return;
    const id = video.id;
    setReprocessing(true);
    try {
      await reprocess(id);
      // The user may have switched videos during the await; only touch this
      // Player's state if it is still the same video (mirrors the summary-SSE
      // and toast guards elsewhere).
      if (openVideoIdRef.current !== id) return;
      // The endpoint just reset summary_status to pending and cleared the
      // stored analysis. Mirror that locally so the summary panel reflects it
      // immediately; the summary SSE drives the rest as the worker runs.
      setVideo((prev) =>
        prev && prev.id === id
          ? {
              ...prev,
              summary_status: "pending",
              summary: "",
              chapters: [],
              key_points: [],
            }
          : prev,
      );
      showToast("Reprocessing", "refresh", "info");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setReprocessing(false);
    }
  }

  async function handleRedownload() {
    // Guard on redownloading too: like Reprocess, the menu closes on click, so
    // a reopened-menu double click would otherwise queue a second re-download.
    if (!video || redownloading) return;
    setRedownloading(true);
    try {
      await redownload(video.id);
      // The video is 'queued' now. Nothing refetches this page's record — the
      // SSE subscription above only carries summary events — so without this
      // the stage would go on saying the file was deleted and go on offering
      // the button, and a second press would hit the endpoint's 409 ("only
      // failed or removed videos can be re-downloaded") and surface it as a
      // page error. Functional form: `video` is a stale closure by now.
      setVideo((prev) =>
        prev && prev.id === video.id ? { ...prev, status: "queued" } : prev,
      );
      onQueued?.();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setRedownloading(false);
    }
  }

  // buildMenuActions assembles the ⋮ menu's entries for the current video. The
  // stateful toggles (Keep forever, Mark watched) and the CC toggle stay out —
  // they live as visible controls in the action row.
  function buildMenuActions(): RowMenuAction[] {
    if (!video) return [];
    const actions: RowMenuAction[] = [];
    // Share only makes sense once there is media to stream — the public page
    // is watch-only. The popover it opens is anchored to the ⋮ itself.
    if (video.has_media) {
      actions.push({
        label: "Share…",
        icon: "share",
        onClick: () => setShareOpen(true),
      });
    }
    // Reprocess re-runs the whole post-import pipeline (summarize → classify →
    // embed, plus a SponsorBlock re-fetch). Offered whenever the endpoint can
    // act, which needs a transcript and nothing else — a tombstoned video keeps
    // its .vtt, so it can still rebuild its analysis without the file back —
    // and hidden while a summary is already pending/running so a second click
    // can't enqueue a duplicate job. Flagged "failed" when the last analysis
    // errored.
    //
    // Hidden during a download too: that is the one case where the transcript
    // named here is about to be replaced by a different file, so the endpoint
    // 409s rather than summarize half of one — and the download runs the whole
    // pipeline on its own once it lands.
    if (
      video.has_subtitles &&
      video.status !== "queued" &&
      video.status !== "downloading" &&
      video.summary_status !== "pending" &&
      video.summary_status !== "running"
    ) {
      actions.push({
        // No busy label: the menu closes on click, so a "Reprocessing…" label
        // could never render. Feedback is a toast + the summary panel flipping
        // to pending (see handleReprocess); the in-flight guard lives there.
        label: "Reprocess video",
        icon: "refresh",
        onClick: handleReprocess,
        // A finished summary that never got indexed is worth the same attention
        // dot: reprocessing is what repairs it, and nothing else on this page
        // would send you here.
        flag:
          video.summary_status === "error" ||
          (video.summary_status === "done" && !video.indexed)
            ? "failed"
            : undefined,
      });
    }
    // Re-download re-fetches the media from YouTube — only meaningful when the
    // current copy is broken or gone.
    if (video.status === "error" || video.status === "tombstoned") {
      actions.push({
        label: "Re-download",
        icon: "refresh",
        onClick: handleRedownload,
      });
    }
    // Download file saves the stored media locally. download=1 makes the stream
    // endpoint attach a proper filename (title + the file's real extension); a
    // bare download attribute can't, since the UI never learns the container,
    // so this stays a plain link to the attachment-disposition URL.
    if (video.has_media) {
      actions.push({
        label: "Download file",
        icon: "download",
        href: `${streamUrl(video.id)}?download=1`,
      });
    }
    actions.push({
      label: "Watch on YouTube",
      icon: "externalLink",
      href: video.url,
      newTab: true,
    });
    actions.push({
      label: "Delete…",
      icon: "trash",
      danger: true,
      onClick: () => setConfirmDelete(true),
    });
    return actions;
  }

  return (
    <div className="playgrid">
      <div className="leftcol">
        <div className="stage stage-wrap">
          {/* Without a poster the stage is a bare black rectangle until the
              first frame decodes — which, for a never-played video, only
              happens once you press play (a resumed video gets a frame for
              free from the seek in handleLoadedMetadata). Show the downloaded
              thumbnail there instead, falling back to the same per-id gradient
              the Library cards use when no local thumbnail exists. */}
          {/* No media, no player. A tombstoned video still has a page worth
              opening — title, summary, chapters, transcript, every fact it ever
              had — but a <video> pointed at a file that was reclaimed offers
              transport controls that can only fail, so the stage falls back to
              the poster and says what happened — with Re-download underneath,
              since that is where the explanation is. It stays in the ⋮ menu
              too, where it also serves a failed download. */}
          {video.has_media ? (
            <>
              {/* The stage is an empty box; the <video> is portalled into
                  videoHost's node and that node is appended in here. Rendering
                  the element directly would tie it to this page's lifetime,
                  which is what used to stop playback on every navigation. */}
              <div className="stage-slot" ref={stageSlotRef} />
              {createPortal(
                <video
                  ref={videoRef}
                  className={
                    video.has_thumbnail ? undefined : gradientClassFor(video.id)
                  }
                  src={playbackSrc}
                  poster={
                    video.has_thumbnail
                      ? thumbnailUrl(video.id, video.thumbnail_version)
                      : undefined
                  }
                  // Native controls belong to the stage. In the dock the tile
                  // is 100x56 and the bar draws its own transport.
                  controls={parkedAt !== "dock"}
                  onLoadedMetadata={handleLoadedMetadata}
                  onTimeUpdate={handleTimeUpdate}
                  onPlay={handlePlay}
                  onEnded={handleEnded}
                >
                  {video.has_subtitles && subtitlesReadyFor === video.id && (
                    <track
                      ref={centerCuesRef}
                      kind="subtitles"
                      srcLang={video.audio_language || "en"}
                      src={subtitlesUrl(video.id)}
                      default={false}
                    />
                  )}
                </video>,
                videoHostNode(),
              )}
            </>
          ) : (
            <div
              className={`stage-gone${
                video.has_thumbnail ? "" : ` ${gradientClassFor(video.id)}`
              }`}
              style={
                video.has_thumbnail
                  ? {
                      backgroundImage: `url(${thumbnailUrl(video.id, video.thumbnail_version)})`,
                    }
                  : undefined
              }
            >
              <p>
                <Icon name="trash" size="15px" />
                {/* "Removed", not "deleted", for the reason the retention
                    setting spells out: what went is the file, and everything
                    else on this page — the summary beside it, the transcript
                    below — is standing proof of that. Same word as the Library
                    card's chip and the search card's pill. */}
                {video.status === "tombstoned"
                  ? "The file was removed to save space."
                  : "No file here yet."}
              </p>
              {/* The sentence above used to end "Re-download it to watch
                  again" — naming an action whose only control was three dots at
                  the other end of the page. Telling someone what to do and
                  hiding the way to do it is worse than not mentioning it, so the
                  button stands where the explanation is. It stays in the ⋮ menu
                  too, alongside the other rarely-used actions, and both go
                  through the same guarded handler. */}
              {video.status === "tombstoned" ? (
                <Button
                  type="button"
                  variant="secondary"
                  busy={redownloading}
                  onClick={handleRedownload}
                >
                  <Icon name="refresh" size="15px" />
                  Re-download
                </Button>
              ) : null}
            </div>
          )}
          <div
            className={`stage-toast${toast ? " show" : ""}${
              toast?.tone === "warn" ? " warn" : ""
            }`}
            role="status"
          >
            <Icon name={toast?.icon ?? "skipForward"} size="15px" />
            {toast?.text}
          </div>
          {/* Nothing to scrub without a file: the bar would render a played
              fill that can never move and a seek that lands nowhere. */}
          {video.has_media && (
            <Scrubber
              currentSeconds={currentTime}
              durationSeconds={duration || video.duration_seconds || 0}
              segments={segments}
              onSeek={seek}
            />
          )}
        </div>
        <div className="playmeta">
          {/* The category pill rides the byline, at the end of the video's
              other two facts — see MetaHeader. It has been in three places now:
              the action row's first segment (wrong, a row of verbs), a line of
              its own beneath the title (right idea, a whole row for one chip),
              and the eyebrow it belongs on. Unlabelled throughout, because the
              coloured dot and the caret say pill-you-can-change without a word
              of chrome. */}
          <MetaHeader
            video={video}
            shareStatus={shareStatus}
            onOpenChannel={onOpenChannel}
            onPickCategory={handlePickCategory}
          />
          {/* The action row splits on one rule: a control keeps its label if
              the label reports the current state (Keep forever / Kept
              forever, Mark watched / Mark unwatched). Controls whose label
              only ever named an action carry that meaning in the icon alone
              — see iconActionClass. */}
          <div className="playacts">
            {/* One shell with internal dividers, not six controls that each
                need their own edge. Every control goes flat inside it — the
                chrome belongs to the container now — and the segments carry
                the grouping the two floating hairlines used to strain at. */}
            <div className="playbar">
              {/* Segment 1 — what this sitting decides about the video and
                  every sitting after it: kept and watched both outlive the
                  page. The category used to ride here too; it moved up to the
                  byline (see MetaHeader) because it is a fact, not a verb. */}
              <span className="playseg">
                <Button
                  type="button"
                  variant={video.favorite ? "gold" : "secondary"}
                  onClick={handleToggleFavorite}
                >
                  <Icon
                    name={video.favorite ? "starFilled" : "star"}
                    size="17px"
                  />
                  <span>
                    {video.favorite ? "Kept forever" : "Keep forever"}
                  </span>
                </Button>
                {/* Tinted when watched, so the state reads at a glance and not
                    only from the label — the favourite beside it has always
                    flipped, and the Library card tints its own watched toggle,
                    so a flat grey button here was the odd one out. Deliberately
                    NOT gold: that is "Kept forever", one button over, and
                    reusing it would make the two states indistinguishable.
                    aria-pressed carries the same state without colour, as the
                    captions toggle below does. */}
                <Button
                  type="button"
                  variant={video.watched ? "tinted" : "secondary"}
                  aria-pressed={video.watched}
                  onClick={handleToggleWatched}
                >
                  <Icon name="check" size="17px" />{" "}
                  {video.watched ? "Mark unwatched" : "Mark watched"}
                </Button>
              </span>
              {/* Segment 2 — the sleep timer, which acts on this sitting and
                  nothing that outlives it. Gated as a whole rather than around
                  the control alone: an empty <span className="playseg"> still
                  draws its divider, a hairline pointing at nothing. Same for
                  segment 3 below, so a tombstoned video renders two segments. */}
              {video.has_media && (
                <span className="playseg">
                  {/* The sleep timer is a pill rather than one of the row's
                      labelled Buttons because it obeys the row's own rule: a
                      control keeps a label when the label reports the state,
                      and this one's label IS the state — "Sleep" when off, the
                      live countdown when armed.

                      has_media alongside the CC toggle for the same reason — a
                      tombstoned video has no <video> element left to pause. */}
                  <SleepTimer
                    remainingSeconds={sleepRemaining}
                    armedMinutes={sleepMinutes}
                    onArm={armSleep}
                  />
                </span>
              )}
              {/* Segment 3 — subtitles, behind its own divider. Sleep and
                  subtitles share a scope, not a verb: sleep schedules something
                  that has not happened yet, subtitles toggles something that is
                  happening now. Sitting them flush also reads wrong — a bare
                  icon against a labelled pill looks like one broken control.

                  has_subtitles as well as has_media: a tombstoned video keeps
                  its transcript (read it in the Transcript panel below), but
                  there is no <video> for burned-in captions to appear over, so
                  the toggle would flip nothing. */}
              {video.has_media && video.has_subtitles && (
                <span className="playseg">
                  {/* On is terracotta, off is the same muted grey as the icons
                      beside it — no fill, no dot. aria-pressed + the flipping
                      aria-label carry the state for anyone who can't see colour. */}
                  <button
                    type="button"
                    className={iconActionClass({ on: ccOn })}
                    aria-pressed={ccOn}
                    aria-label={ccOn ? "Subtitles on" : "Subtitles off"}
                    title={ccOn ? "Subtitles on" : "Subtitles off"}
                    onClick={handleToggleCC}
                  >
                    <Icon name="captions" size="19px" />
                  </button>
                </span>
              )}
              {/* Segment 4 — everything else. The ⋮ gets a divider of its own
                  because it is not a sibling of the controls beside it: it is
                  the lid on six more actions (Share, Reprocess, Re-download,
                  Download file, Watch on YouTube, Delete). It carries an
                  attention dot only when the menu actually holds a flagged
                  remedy — a failed summary_status on a video that can't be
                  reprocessed (no subtitle) would otherwise promise a fix the
                  menu doesn't offer.

                  ShareControl wraps the menu rather than sitting beside it: its
                  popover anchors to the ⋮ that opened it, and the trigger has to
                  live inside ShareControl's wrapper or its outside-click handler
                  would close the popover on the same click that opened it. */}
              <span className="playseg">
                {(() => {
                  const menuActions = buildMenuActions();
                  return (
                    <ShareControl
                      videoId={video.id}
                      status={shareStatus}
                      onStatusChange={setShareStatus}
                      open={shareOpen}
                      onOpenChange={setShareOpen}
                    >
                      <RowMenu
                        label="Video actions"
                        attention={menuActions.some((a) => a.flag)}
                        actions={menuActions}
                      />
                    </ShareControl>
                  );
                })()}
              </span>
            </div>
          </div>
          <MediaStats video={video} />
        </div>

        <div className="belowvideo">
          {/* seek only when there is something to seek. A tombstoned video keeps
              its chapters, highlights and transcript, and every row in them
              would otherwise be a button that silently does nothing — the same
              dead control the scrubber and the captions toggle are already
              hidden to avoid. */}
          <ContentsCard
            video={video}
            seek={video.has_media ? seek : undefined}
          />
          {/* Highlights are timestamps into the chapters directly above them,
              so they belong under that list rather than in the rail beside the
              video. Only the Summary stays in the rail — it is prose about the
              video as a whole, and it reads at a narrow measure. */}
          <HighlightsCard
            video={video}
            seek={video.has_media ? seek : undefined}
          />
          {video.has_subtitles && (
            /* seek is withheld for a fileless video, exactly as ContentsCard
               above withholds it: with nothing to seek, a cue is text to read,
               not a control that silently does nothing. Find still highlights
               it either way.

               Keyed on the video, so navigating from one video to another
               remounts the panel: collapsed, with an empty find box. Player is
               not unmounted between videos, and the state that used to be
               reset per-video (transcriptOpen, find) now lives inside the
               component — without this key you would land on video B with the
               panel still expanded and video A's search term still in it. */
            <TranscriptCard
              key={video.id}
              vttUrl={subtitlesUrl(video.id)}
              filenameBase={transcriptFilenameBase(video.title)}
              seek={video.has_media ? seek : undefined}
              /* "full" is the default this prop already carried; the second
                 class is the placement hook the single-column order uses. */
              className="full transcriptpanel"
            />
          )}
        </div>
      </div>

      <aside className="side">
        <SummaryCard video={video} />
      </aside>
      {video ? (
        <ConfirmDialog
          open={confirmDelete}
          title="Delete this video?"
          confirmLabel="Delete"
          busy={deleting}
          onConfirm={handleDelete}
          onCancel={() => setConfirmDelete(false)}
        >
          This removes the stored media and its analysis for “{video.title}”.
          This can’t be undone.
        </ConfirmDialog>
      ) : null}
    </div>
  );
}
