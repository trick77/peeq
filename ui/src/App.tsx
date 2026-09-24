import { useCallback, useEffect, useMemo, useState } from "react";
import { Rail, type ViewId } from "./shell/Rail";
import { SignIn } from "./shell/SignIn";
import { useAuthBootstrap } from "./shell/useAuthBootstrap";
import { useLiveQueue } from "./shell/useLiveQueue";
import { DownloadStatusBanner } from "./shell/DownloadStatusBanner";
import { takeAuthFailed } from "./authError";
import { getPlaybackState, resumeYoutube } from "./api";
import type { DownloadsStatus } from "./api/downloads";
import type { SummaryStatus } from "./api/enums";
import type { ActivityEvent, Job, SummaryJob } from "./api/types";
import { Library } from "./views/Library";
import { Add } from "./views/Add";
import { Player } from "./views/Player";
import { Settings } from "./views/Settings";
import { Channels } from "./views/Channels";
import { Channel } from "./views/Channel";
import { Inbox } from "./views/Inbox";
import { UpNext } from "./views/UpNext";
import { History } from "./views/History";
import { Search } from "./views/Search";
import { useSearchState, type SearchState } from "./searchState";
import { Share } from "./views/Share";
import { useRoute } from "./route";
import { TabBar } from "./shell/TabBar";
import { NowDock } from "./shell/NowDock";
import { MOBILE_QUERY, useMediaQuery } from "./shell/useMediaQuery";
import { hostedVideo } from "./videoHost";
import type { NowPlaying } from "./nowPlaying";
import { setPlaybackState } from "./api/playback";

// Where the rail's collapsed/expanded choice is kept. It is a preference about
// this browser's window, not about the account, so it lives in localStorage
// rather than in settings on the server — a phone and a desktop that share a
// login should not argue about how wide a sidebar is.
const RAIL_COLLAPSED_KEY = "peeq.rail.collapsed";

// Both accessors are wrapped: App is also rendered through
// renderToStaticMarkup in App.test.tsx, where there is no window at all, and a
// browser with storage blocked throws on access rather than returning null.
// Neither case is worth a broken app over a sidebar width.
function readRailCollapsed(): boolean {
  try {
    return (
      typeof window !== "undefined" &&
      window.localStorage?.getItem(RAIL_COLLAPSED_KEY) === "1"
    );
  } catch {
    return false;
  }
}

// At module load, before any effect or route read can rewrite the URL — see
// takeAuthFailed for why it is consumed rather than merely read.
const AUTH_FAILED = takeAuthFailed();

function writeRailCollapsed(collapsed: boolean) {
  try {
    window.localStorage?.setItem(RAIL_COLLAPSED_KEY, collapsed ? "1" : "0");
  } catch {
    /* storage blocked — the session still works, it just forgets. */
  }
}

// Per-view page titles used to live here, in a VIEW_META map feeding the top
// bar's <h1>. They were dropped: the title only ever restated the rail item
// you had just clicked, and every page paid ~66px of chrome for it. The rail's
// active marker is now the sole "where am I" signal.

// App — the shell (rail + optional search bar + routed main) plus the four Task 14
// views. Routing is manual view-state, no router lib — matches loom's
// pattern for a single-page app this size.
export function App() {
  // The URL is the source of truth for which page is open: `view` and the two
  // selected ids are derived from the path (route.ts), so a page can be deep-
  // linked, refreshed, and walked with the browser's back/forward buttons. A
  // cold-loaded /video/<id> therefore reopens the Player straight away (paused
  // at the server-side resume position via Player's handleLoadedMetadata seek),
  // which is what retired the old nowPlaying sessionStorage reload-restore.
  //
  // persistedVideoId (below) does not change that. It answers the one question
  // the URL genuinely can't — what "Now playing" means when the address bar
  // carries no video id — and nothing else: a cold load of "/" still lands on
  // the Library, not in a player.
  const { route, navigate } = useRoute();
  const view = route.view;
  const selectedVideoId = route.videoId;
  const selectedChannelId = route.channelId;
  // The server-side "now playing" pointer (GET /api/playback), so the video you
  // are part-way through is the same on every device instead of dying with the
  // tab that opened it. null until the first load lands, and stays null when
  // that load fails — the rail then behaves exactly as it did before this
  // existed.
  const [persistedVideoId, setPersistedVideoId] = useState<string | null>(null);
  // Which video "Now playing" means: the one in the URL, else the persisted
  // pointer. This is the whole read side of the feature.
  const nowPlayingId = selectedVideoId ?? persistedVideoId;
  // The video opened to READ rather than to watch, and where it was opened
  // from. A video with no media yet renders /video/<id> as what peeq read of it
  // rather than as a player — deliberately the same URL the Library opens, so
  // that nothing changes when the file later arrives (Inbox.tsx). That sharing
  // is what this exists to disambiguate: the route alone cannot say whether
  // /video/<id> is a video playing or a summary being read, and the two want
  // different things from the rail and from "Now playing".
  //
  // Held as the video's id rather than a bare flag, so it is a claim about one
  // specific page instead of a mode the app is left in. That makes it
  // self-expiring: navigating to another video simply stops matching, and
  // walking back to the summary with the browser's back button starts matching
  // again, with no clearing logic to keep in step.
  //
  // `from` is the origin, because such a page is now reachable from two places
  // and they owe the reader different things. From the Inbox it is one of a
  // stack to work through: the back link returns to the grid and a stepper walks
  // to the next one. From Search it is a single result: there is no inbox
  // position to step through, and the way back is the results — which are still
  // there, because the search survives the trip (searchState.ts).
  const [summaryOrigin, setSummaryOrigin] = useState<{
    id: string;
    from: "inbox" | "search";
  } | null>(null);
  const readingSummaryFrom =
    view === "player" && !!route.videoId && route.videoId === summaryOrigin?.id
      ? summaryOrigin.from
      : null;
  const readingSummary = readingSummaryFrom !== null;
  // playerPageVideoId — the video the player PAGE is showing right now, or
  // null when the page on screen is not one. A summary being read is not one:
  // it shares the route but has no file to play.
  const playerPageVideoId =
    view === "player" && !readingSummary ? nowPlayingId : null;
  // playbackVideoId — the video PLAYBACK owns, which outlives the page that
  // started it. This split is the whole feature: the <video> element used to
  // die with the player view, so opening the Inbox to deal with one new upload
  // stopped whatever you were in the middle of.
  //
  // Deliberately not seeded from the server-side pointer at boot. Doing that
  // would mount a player and start fetching media bytes on every cold load of
  // the Library, for a video nobody asked to watch yet. The pointer keeps its
  // existing job — telling the rail's "Now playing" where to go.
  //
  // Set when the video actually STARTS PLAYING, never merely when its page is
  // opened. Opening a page is not watching: walk into "Now playing", look at
  // the summary, and walk out, and there is nothing to carry — a dock that
  // followed you out of a video you never pressed play on would be announcing
  // something that is not happening.
  //
  // Pausing does not clear it, because a paused video is still the one you are
  // in the middle of. Only ✕ and opening a different video do.
  const [playbackVideoId, setPlaybackVideoId] = useState<string | null>(null);
  // The page on screen always wins, so the very first render of a player page
  // already knows its video; playbackVideoId only supplies the answer once you
  // have navigated away from it.
  const playingId = playerPageVideoId ?? playbackVideoId;
  const showPlayerPage = !!playerPageVideoId;
  // Opening another video's page replaces what is loaded in the one shared
  // element, so whatever was playing is gone — and the newcomer has not been
  // played yet. Without this the dock would follow you out of the new page
  // still naming the old video, which is no longer anywhere.
  useEffect(() => {
    if (playerPageVideoId && playerPageVideoId !== playbackVideoId) {
      setPlaybackVideoId(null);
    }
    // playbackVideoId is read, not tracked: reacting to it would undo the
    // adoption that onPlaybackStarted just made, one render later.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [playerPageVideoId]);
  const handlePlaybackStarted = useCallback((id: string) => {
    setPlaybackVideoId(id);
  }, []);
  // Guarded on the id so a late "ended" from a video that has already been
  // replaced cannot drop the one that replaced it.
  const handlePlaybackEnded = useCallback((id: string) => {
    setPlaybackVideoId((cur) => (cur === id ? null : cur));
  }, []);
  // What the dock says it is playing, published by the Player once per video.
  // Held here rather than derived because only the Player has fetched the
  // video, and it is the Player that knows whether it has a file at all.
  const [nowPlaying, setNowPlaying] = useState<NowPlaying | null>(null);
  const handleNowPlaying = useCallback((p: NowPlaying | null) => {
    setNowPlaying(p);
  }, []);
  useEffect(() => {
    if (!playingId) setNowPlaying(null);
  }, [playingId]);
  // The origin is recorded before anyone knows whether the video has a file.
  // Player reports the answer, and a video with media is one being watched — so
  // the marker goes, the rail returns to "Now playing", and setView's
  // short-circuit starts treating this page as what it is.
  //
  // It is also what keeps two Players from rendering a <video> into the one
  // shared host: dropping the marker promotes the page to the Player that owns
  // playback, and the other one stops being rendered at all.
  // Stable (functional update, no deps): it is a Player prop, and the Player
  // is memoised so a shell re-render does not reach it.
  const handleMediaKnown = useCallback((id: string, hasMedia: boolean) => {
    setSummaryOrigin((prev) => (hasMedia && prev?.id === id ? null : prev));
  }, []);
  // setView keeps every existing call site (the rail, the banner's "fix
  // cookie", ViewSwitch's back/deleted handlers) unchanged — it just pushes a
  // new URL. navigate is stable, so this is too.
  //
  // "Now playing" is the one destination that carries an id, so it is special-
  // cased to push /video/<id> rather than the bare /video. Without that the
  // address bar would read "/video" while a video plays, and a refresh or a
  // copied link would lose it — which would fight route.ts's URL-as-truth rule
  // rather than respect it.
  //
  // It also RE-READS the pointer instead of trusting the copy loaded at
  // bootstrap. The pointer is server state that other actions clear: marking the
  // pointed-at video watched from a Library or Channel card, or deleting it,
  // clears it server-side, and a stale local copy would have this click reopen a
  // finished video at 0:00 — exactly what the clear rule exists to prevent. Any
  // pointer another device has moved on lands here too. One request per click on
  // one rail item, only when the URL has no video of its own to show; a failed
  // read falls back to the loaded copy, so this is never worse than not asking.
  //
  // "Of its own to show" is narrower than "route.videoId is set", and the
  // difference is a bug this once had. navigate merges onto the current route
  // (route.ts), so videoId survives leaving the Player — that is what lets "Now
  // playing" return to your video after a detour through Channels. But once a
  // summary page could occupy /video/<id>, that memory could hold a video with
  // no file, and skipping the re-read on it made a fileless video shadow the
  // real pointer for the rest of the session: read one summary, go anywhere,
  // and "Now playing" reopened the summary rather than the video you were
  // actually watching. The pointer is already guarded — the backend only ever
  // points at a downloaded video (playback.Store) — so the fix is to consult it
  // rather than the memory whenever the memory is not a video being watched.
  //
  // "A summary being read" means from EITHER origin. Testing only the inbox one
  // would let a summary opened from Search shadow the pointer in exactly the way
  // described above.
  const setView = useCallback(
    (v: ViewId) => {
      // The URL already shows a video being watched: this click is a no-op, and
      // must stay one. Re-reading here would let a pointer another device moved
      // navigate you out of what you are watching.
      const watchingAVideo = !!route.videoId && !readingSummary;
      if (v !== "player" || (view === "player" && watchingAVideo)) {
        navigate({ view: v });
        return;
      }
      void getPlaybackState()
        .then((p) => {
          const fresh = p.video_id || null;
          setPersistedVideoId(fresh);
          // The pointer only ever names a downloaded video (playback.Store), so
          // a pointer naming the video remembered as a summary being read is
          // that video after its file arrived — it is being watched now, not
          // read. Dropping the marker keeps the rail off Inbox (or Search) on
          // the page this click opens, and keeps the next click on this item
          // short-circuiting the way it does for any other video being watched.
          if (fresh && fresh === summaryOrigin?.id) setSummaryOrigin(null);
          navigate({ view: "player", videoId: fresh });
        })
        // A failed read must be no worse than not asking, and off the Player
        // that now means falling back to the route's memory before the copy
        // loaded at bootstrap: the id the URL is carrying is the last video
        // this tab opened, and until this callback started re-reading off the
        // Player, a click here returned to it without asking anything. The
        // bootstrap copy can easily be null on a tab that cold-loaded "/" —
        // dropping to "Nothing playing" because one GET failed would lose the
        // video you were watching a moment ago. A summary page sitting in that
        // memory is excluded by id, the same thing readingSummary checks and the
        // reason this can't just reuse it (that flag is false off the Player,
        // where this runs).
        .catch(() =>
          navigate({
            view: "player",
            videoId:
              (route.videoId !== summaryOrigin?.id ? route.videoId : null) ??
              persistedVideoId,
          }),
        );
    },
    [
      navigate,
      view,
      route.videoId,
      readingSummary,
      summaryOrigin,
      persistedVideoId,
    ],
  );
  const { user, authChecked, authError, checkSlow, signedInHint, expired } =
    useAuthBootstrap();
  const {
    jobs,
    jobsLoaded,
    summaries,
    summariesLoaded,
    summaryPhaseByVideoId,
    summaryEvent,
    liveActivity,
    pendingCount,
    setPendingCount,
    cookieStatus,
    ytdlp,
    downloadStatus,
    activeDownloads,
    queueSignal,
    stalled,
    refreshQueue,
    refreshPending,
    refreshStatus,
    cancelDownload: onCancelDownload,
  } = useLiveQueue(authChecked && !!user);
  // The banner's Resume: flip the kill-switch, then re-read the lights so the
  // banner clears (or says why it did not) without waiting for the poll.
  const resumeAndRefresh = useCallback(async () => {
    await resumeYoutube();
    await refreshStatus();
  }, [refreshStatus]);
  // Search boxes for the two list pages that have one. Each view now renders
  // its own field, in its own toolbar row above the chips; the state stays
  // lifted here so that a query survives leaving the page and coming back —
  // the behaviour it had while the field belonged to the shell's top bar.
  // They are kept apart on purpose: a video-title query must not carry over
  // into a channel-name filter when you switch pages. The channel *detail*
  // page keeps its own separate in-page search.
  const [librarySearch, setLibrarySearch] = useState("");
  const [channelSearch, setChannelSearch] = useState("");
  const [historySearch, setHistorySearch] = useState("");
  const [upNextSearch, setUpNextSearch] = useState("");
  const [inboxSearch, setInboxSearch] = useState("");
  // The global Search view is lifted for the same reason, and then some: it is
  // not one string but a query, its results, and (on Ask) a streamed answer that
  // cost an embedding, a keyword ladder and a model call. Opening two of the
  // videos it found used to buy all of that twice. See searchState.ts.
  const search = useSearchState();
  // The rail's width, remembered across reloads. Two flags rather than one:
  // sidebarCollapsed is what the user chose on a desktop and the only thing
  // written to storage, railCollapsed is what the shell actually renders. On a
  // phone there is no rail to collapse — the tab bar is the navigation — so the
  // derived value is false there while the stored preference stays untouched,
  // and a window dragged back to full width finds it exactly as it was left.
  // Forcing the flag itself on a narrow window would destroy that preference.
  const [sidebarCollapsed, setSidebarCollapsed] = useState(readRailCollapsed);
  const isMobile = useMediaQuery(MOBILE_QUERY);
  const railCollapsed = !isMobile && sidebarCollapsed;
  useEffect(() => {
    writeRailCollapsed(sidebarCollapsed);
  }, [sidebarCollapsed]);
  // The ids the Inbox grid is currently showing, in the order it shows them,
  // so a video's page can step through the inbox without going back to it.
  //
  // It lives here rather than in the Inbox because the page that consumes it is
  // a sibling, and it is reported by the Inbox rather than refetched because the
  // on-screen order is the product of a search box, a channel chip and a sort
  // select that only that component knows about. Empty until the Inbox has been
  // opened at least once — a cold deep-link to a video therefore gets no
  // stepper, which is correct: there is no inbox position to be at.
  const [inboxOrder, setInboxOrder] = useState<string[]>([]);
  // pendingSeek is the jump-to-moment target set by Search's onOpen (Task
  // 18): Player consumes it once on the loadedmetadata handler that already
  // applies the resume position, taking priority over resume. openVideo
  // (Library/Channels' plain "open" path) clears it up front, so navigating
  // to a video that way never applies a stale seek from an earlier search.
  // The Player's onSeekConsumed callback below also clears it the moment the
  // seek is actually applied, so a later remount of the Player (e.g. via the
  // rail's "Now playing" without going through openVideo/openVideoAt again —
  // the Player unmounts whenever the view navigates away) can never replay
  // it and override the user's real resume position.
  const [pendingSeek, setPendingSeek] = useState<number | undefined>(undefined);
  useEffect(() => {
    if (!authChecked || !user) return;
    let active = true;
    // Best-effort: a rail that can't load the pointer falls back to its old
    // in-memory behaviour rather than showing an error for a convenience. An
    // empty video_id means nothing is playing — which also covers a pointer
    // whose video has since been deleted.
    getPlaybackState()
      .then((p) => {
        if (active) setPersistedVideoId(p.video_id || null);
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, [authChecked, user]);

  // The callbacks below are handed to memoised shell pieces (Player, the rail,
  // the dock), so they are stable — and declared here, above the early
  // returns, because hooks cannot follow them.
  // openChannel is the channel page's only entry point: there is no rail
  // item for it, so this is called from every place a channel name appears.
  const openChannel = useCallback(
    (id: string) => navigate({ view: "channel", channelId: id }),
    [navigate],
  );

  // stopPlayback — the dock's ✕. Ends the sitting rather than pausing it, so
  // it also drops the server-side now-playing pointer: leaving it would have
  // the rail keep offering to reopen the thing you just closed, on this device
  // and on every other one.
  //
  // The element is paused before the state change rather than left to the
  // unmount, so the sound stops on the click instead of on the next commit.
  const stopPlayback = useCallback(() => {
    hostedVideo()?.pause();
    setPlaybackVideoId(null);
    setNowPlaying(null);
    setPersistedVideoId(null);
    setPlaybackState(null).catch(() => {});
  }, []);

  // The dock's tile, title and chevron all lead back to the player page. It is
  // the same navigation the rail's "Now playing" performs, minus the pointer
  // re-read: the dock is showing what is playing in this tab right now, so
  // there is nothing to go and ask the server about.
  const openPlayingVideo = useCallback(() => {
    setPendingSeek(undefined);
    setSummaryOrigin(null);
    navigate({ view: "player", videoId: playbackVideoId });
  }, [navigate, playbackVideoId]);

  // Player props that used to be inline arrows, recreated every render.
  const clearPendingSeek = useCallback(() => setPendingSeek(undefined), []);
  // Through navigate, not setView: setView is rebuilt on every route change
  // (it reads the route), and a prop that changes on navigation would defeat
  // the Player's memo exactly when the hidden Player is most expensive. For
  // a plain view the two are the same call.
  const goToLibrary = useCallback(
    () => navigate({ view: "library" }),
    [navigate],
  );
  const handlePlayerDeleted = useCallback(() => {
    // The file is gone, so there is nothing left to play or to dock — and the
    // backend has already dropped the pointer.
    setPlaybackVideoId(null);
    setNowPlaying(null);
    setPersistedVideoId(null);
    goToLibrary();
  }, [goToLibrary]);
  // Where the summary page goes back to, as a stable function per origin.
  const backFromSummary = useMemo(
    () =>
      readingSummaryFrom
        ? () => navigate({ view: readingSummaryFrom })
        : undefined,
    [readingSummaryFrom, navigate],
  );
  const toggleCollapsed = useCallback(() => setSidebarCollapsed((v) => !v), []);

  // The public share page renders above everything else — no rail, no top bar,
  // and crucially before the auth gate below, since its whole point is to work
  // for a recipient who is not signed in.
  if (view === "share") {
    return <Share token={route.token} />;
  }

  if (!authChecked) {
    // A browser that was signed in last time gets nothing at all until the
    // check lands — body already paints --color-bg, so "nothing" is the app's
    // own ground, not a white page. Showing the sign-in card here and pulling
    // it away half a second later is the flash this avoids; the card stays for
    // the visitor it was written for, who has no hint stored.
    //
    // A failed OIDC callback overrides the hint: that user's session just went,
    // and the "Sign-in didn't complete" line below is the whole point of the
    // redirect. Suppressing the screen would swallow it. So does a check that
    // has stopped being quick (see checkSlow) — nothing at all is only right
    // while "nothing" reads as the app arriving.
    if (signedInHint && !AUTH_FAILED && !checkSlow) {
      return null;
    }
    return <SignIn checking />;
  }

  if (!user) {
    return (
      <SignIn unreachable={authError} failed={AUTH_FAILED} expired={expired} />
    );
  }

  function openVideo(id: string) {
    setPendingSeek(undefined);
    setSummaryOrigin(null);
    navigate({ view: "player", videoId: id });
  }

  // openInboxSummary — the Inbox's onOpen. Same destination as openVideo, and
  // deliberately so: an inbox video's page is the video's page, it just has no
  // file to play yet. The one difference is that this records which video it
  // was and where from, so the shell can tell a summary being read from a video
  // being watched (see summaryOrigin).
  function openInboxSummary(id: string) {
    setPendingSeek(undefined);
    setSummaryOrigin({ id, from: "inbox" });
    navigate({ view: "player", videoId: id });
  }

  // openVideoFromSearch — Search's plain open: the card's title, its thumbnail,
  // and a summary match, all of which mean "this video" rather than "this
  // moment in it". No seek at all, which is NOT the same as a seek to 0: Player
  // applies any seekTo that is not undefined, so a zero would rewind a
  // half-watched video and then have the next resume flush store that zero.
  //
  // The origin is recorded OPTIMISTICALLY — at this point nobody knows whether
  // this video has a file. A search result frequently does not (peeq indexes
  // what it read as well as what it downloaded), and that page's back link and
  // stepper are the whole reason the origin exists. Player clears it the moment
  // it learns there IS media: what it opened is then a video being watched, not
  // a summary being read.
  function openVideoFromSearch(id: string) {
    setPendingSeek(undefined);
    setSummaryOrigin({ id, from: "search" });
    navigate({ view: "player", videoId: id });
  }

  // openVideoAt — Search's moment open: jumps into the Player at a matched
  // transcript or chapter chunk's start_seconds. The seek target stays in App
  // state, not the URL — it is transient sub-page state, out of the deep-link
  // scope.
  function openVideoAt(id: string, startSeconds: number) {
    setPendingSeek(startSeconds);
    setSummaryOrigin({ id, from: "search" });
    navigate({ view: "player", videoId: id });
  }

  // The rail and the phone's tab bar are the same navigation in two shapes, so
  // they are handed the same three answers rather than each working them out.
  //
  // Reading a video's summary keeps the nav on the page it was reached from.
  // Lighting "Now playing" for it says both where you aren't and something
  // untrue — nothing is playing, there is no file yet.
  const navActive = readingSummaryFrom ?? view;
  const navUpNextCount =
    jobsLoaded && summariesLoaded
      ? activeDownloads + summaries.length
      : undefined;
  // The pill is a "something is happening" light, not a backlog size, so it
  // needs a job actually running in either lane. Both lanes count: a running
  // summary lights it exactly as a running download does.
  const navUpNextLive =
    jobs.some((j) => j.state === "running") ||
    summaries.some((s) => s.state === "running");

  return (
    <div className={`app-shell${railCollapsed ? " collapsed" : ""}`}>
      {/* One navigation at a time, chosen in JS rather than hidden in CSS.
          Rendering both and hiding one would put nine duplicate destinations in
          the accessibility tree, where display:none is the only thing telling
          them apart — and a screen reader that ignores the viewport would read
          the app's whole nav twice. */}
      {isMobile ? (
        <TabBar
          active={navActive}
          onNavigate={setView}
          pendingCount={pendingCount}
          upNextCount={navUpNextCount}
          upNextLive={navUpNextLive}
        />
      ) : (
        <Rail
          active={navActive}
          onNavigate={setView}
          collapsed={railCollapsed}
          onToggleCollapsed={toggleCollapsed}
          pendingCount={pendingCount}
          upNextCount={navUpNextCount}
          upNextLive={navUpNextLive}
          cookieStatus={cookieStatus}
          ytdlp={ytdlp}
        />
      )}
      <main className="main">
        <section className="page">
          <DownloadStatusBanner
            status={downloadStatus}
            onFixCookie={() => setView("settings")}
            onResume={resumeAndRefresh}
          />
          {/* The player is mounted OUTSIDE the view switch and stays mounted
              once a video is open, because the <video> element lives in its
              tree: unmounting it on navigation is exactly what used to stop
              playback. Hidden rather than removed when the page on screen is
              something else, with the video itself relocated into the dock by
              videoHost.

              `hidden` and not a class: it has to keep the page out of the
              accessibility tree and out of tab order too, and one attribute
              says all three. */}
          {playingId ? (
            <div hidden={!showPlayerPage}>
              <Player
                videoId={playingId}
                visible={showPlayerPage}
                onNowPlaying={handleNowPlaying}
                seekTo={pendingSeek}
                onSeekConsumed={clearPendingSeek}
                onDeleted={handlePlayerDeleted}
                onOpenChannel={openChannel}
                onQueued={refreshQueue}
                onPendingChanged={refreshPending}
                // No summaryOrigin and no back link: this Player is only ever
                // the video being watched. A page being READ is the other
                // branch below, which is exactly why the two are separate —
                // and it is what keeps a second <video> out of the shared host.
                onMediaKnown={handleMediaKnown}
                onPlaybackStarted={handlePlaybackStarted}
                onPlaybackEnded={handlePlaybackEnded}
                summaryEvent={summaryEvent}
              />
            </div>
          ) : null}
          {/* Skipped entirely while the player page is the page: rendering it
              would draw a second Player for the same video. */}
          {showPlayerPage ? null : (
            <ViewSwitch
              inboxOrder={inboxOrder}
              setInboxOrder={setInboxOrder}
              view={view}
              selectedVideoId={nowPlayingId}
              selectedChannelId={selectedChannelId}
              pendingSeek={pendingSeek}
              onOpenVideo={openVideo}
              onOpenInboxSummary={openInboxSummary}
              onOpenVideoAt={openVideoAt}
              onOpenVideoFromSearch={openVideoFromSearch}
              onOpenChannel={openChannel}
              onSeekConsumed={clearPendingSeek}
              search={search}
              summaryOrigin={readingSummaryFrom}
              summaryEvent={summaryEvent}
              // The origin is recorded before anyone knows whether the video has a
              // file. Player reports the answer, and a video with media is one
              // being watched — so the marker goes, the rail returns to "Now
              // playing", and setView's short-circuit starts treating this page as
              // what it is.
              onMediaKnown={handleMediaKnown}
              setView={setView}
              setPendingCount={setPendingCount}
              onQueued={refreshQueue}
              onDeleted={goToLibrary}
              onBackFromSummary={backFromSummary}
              onStatusChanged={refreshStatus}
              onPendingChanged={refreshPending}
              librarySearch={librarySearch}
              channelSearch={channelSearch}
              historySearch={historySearch}
              upNextSearch={upNextSearch}
              inboxSearch={inboxSearch}
              onLibrarySearchChange={setLibrarySearch}
              onChannelSearchChange={setChannelSearch}
              onHistorySearchChange={setHistorySearch}
              onUpNextSearchChange={setUpNextSearch}
              onInboxSearchChange={setInboxSearch}
              queueSignal={queueSignal}
              jobs={jobs}
              summaries={summaries}
              summaryPhaseByVideoId={summaryPhaseByVideoId}
              onCancelDownload={onCancelDownload}
              liveActivity={liveActivity}
              // Same precedence the banner uses: the kill-switch outranks a full
              // disk, which outranks the cookie pause. Up next names the cause
              // because each one has a different way out — only the kill-switch
              // has a Resume button.
              stalled={stalled}
            />
          )}
        </section>
      </main>
      {/* Outside <main>, because it is not part of the page: it is fixed to
          the floor of the shell and outlives every view. */}
      <NowDock
        playing={nowPlaying}
        onOpenPlayer={openPlayingVideo}
        onStop={stopPlayback}
      />
    </div>
  );
}

function ViewSwitch({
  view,
  selectedVideoId,
  selectedChannelId,
  pendingSeek,
  onOpenVideo,
  onOpenInboxSummary,
  onOpenVideoAt,
  onOpenVideoFromSearch,
  onOpenChannel,
  onSeekConsumed,
  search,
  summaryOrigin,
  onMediaKnown,
  summaryEvent,
  setView,
  setPendingCount,
  onQueued,
  onDeleted,
  onBackFromSummary,
  onStatusChanged,
  onPendingChanged,
  librarySearch,
  channelSearch,
  historySearch,
  upNextSearch,
  inboxSearch,
  onLibrarySearchChange,
  onChannelSearchChange,
  onHistorySearchChange,
  onUpNextSearchChange,
  onInboxSearchChange,
  queueSignal,
  jobs,
  summaries,
  summaryPhaseByVideoId,
  onCancelDownload,
  liveActivity,
  stalled,
  inboxOrder,
  setInboxOrder,
}: {
  view: ViewId;
  selectedVideoId: string | null;
  selectedChannelId: string | null;
  pendingSeek: number | undefined;
  onOpenVideo: (id: string) => void;
  onOpenInboxSummary: (id: string) => void;
  onOpenVideoAt: (id: string, startSeconds: number) => void;
  onOpenVideoFromSearch: (id: string) => void;
  onOpenChannel: (id: string) => void;
  onSeekConsumed: () => void;
  // The Search view's whole engine, held above it so a search survives being
  // left for a video and comes back intact (searchState.ts).
  search: SearchState;
  // Where the summary page currently showing was opened from, or null when the
  // page showing is not one. It decides the back link and whether the inbox
  // stepper appears at all.
  summaryOrigin: "inbox" | "search" | null;
  onMediaKnown: (id: string, hasMedia: boolean) => void;
  summaryEvent: { videoId: string; status: SummaryStatus } | null;
  setView: (v: ViewId) => void;
  // undefined = the count is not known (a failed inbox fetch). The rail draws
  // no pill for it, which is deliberately not the same claim as "empty".
  setPendingCount: (n: number | undefined) => void;
  onQueued: () => void;
  onDeleted: () => void;
  onBackFromSummary?: () => void;
  onStatusChanged: () => void;
  onPendingChanged: () => void;
  librarySearch: string;
  channelSearch: string;
  historySearch: string;
  upNextSearch: string;
  inboxSearch: string;
  onLibrarySearchChange: (value: string) => void;
  onChannelSearchChange: (value: string) => void;
  onHistorySearchChange: (value: string) => void;
  onUpNextSearchChange: (value: string) => void;
  onInboxSearchChange: (value: string) => void;
  queueSignal: string;
  jobs: Job[];
  summaries: SummaryJob[];
  summaryPhaseByVideoId: Record<string, string>;
  onCancelDownload: (jobId: number) => Promise<void>;
  liveActivity: ActivityEvent[];
  /** Why YouTube work is stopped, if it is — only Up next's empty state uses it. */
  stalled?: "youtube" | "disk" | "cookie";
  /**
   * The ids the Inbox is currently showing, in on-screen order, and the setter
   * it reports them through. Together they let a video's page step to the next
   * inbox item without going back to the grid — the Inbox owns the order
   * (search, chip and sort all shape it), the Player consumes it, and they are
   * siblings, so it goes up and back down.
   */
  inboxOrder: string[];
  setInboxOrder: (ids: string[]) => void;
}) {
  switch (view) {
    case "library":
      return (
        <Library
          onOpenVideo={onOpenVideo}
          onOpenChannel={onOpenChannel}
          search={librarySearch}
          onSearchChange={onLibrarySearchChange}
          queueSignal={queueSignal}
          onQueued={onQueued}
        />
      );
    case "player":
      return (
        <Player
          videoId={selectedVideoId}
          seekTo={pendingSeek}
          onSeekConsumed={onSeekConsumed}
          onDeleted={onDeleted}
          onOpenChannel={onOpenChannel}
          onQueued={onQueued}
          onPendingChanged={onPendingChanged}
          onMediaKnown={onMediaKnown}
          summaryEvent={summaryEvent}
          // Where the summary page goes back to, and what it calls that place.
          // null when this page was not opened from anywhere that offers a way
          // back — a video reached from the Library or from a cold link then
          // gets no back link at all, rather than the "Back to inbox" it used to
          // claim regardless of how it was reached.
          summaryOrigin={summaryOrigin}
          onBackFromSummary={onBackFromSummary}
          // The Prev/Next stepper walks the Inbox, so it is passed only when
          // that is where this page came from. A search result that happens to
          // also sit in the inbox would otherwise show "3 of 40" and offer to
          // step through a list the reader never opened.
          inboxOrder={summaryOrigin === "inbox" ? inboxOrder : undefined}
          // The stepper, and the hop a decision makes to the item after it. Both
          // stay inside the Inbox, so they open the next video the same way the
          // grid did — openVideo would clear the origin and leave the stepped-to
          // video reading as one being watched: the rail would announce "Now
          // playing" over a file that does not exist yet, and "Now playing"
          // would be a dead click.
          onOpenInboxVideo={onOpenInboxSummary}
        />
      );
    case "search":
      return (
        <Search
          search={search}
          onOpen={onOpenVideoAt}
          onOpenVideo={onOpenVideoFromSearch}
          onOpenChannel={onOpenChannel}
        />
      );
    case "add":
      // Stay on the Add page after queuing (per the mockup — the preview
      // card confirms the queue, it doesn't jump into Player before the
      // download has even started); onOpenVideo is Library's job.
      return <Add onQueued={onQueued} onPendingChanged={onPendingChanged} />;
    case "inbox":
      // onCountChange keeps the rail badge in sync while the user acts on
      // items (Download/Ignore) here. The count is otherwise read once on
      // sign-in and again on every activity event and reconnect; every other
      // page that moves an item out of the inbox (a summary page's decision,
      // a pasted URL the inbox held, a deleted channel) reports it through
      // onPendingChanged. onQueued seeds the download poll the moment an item
      // is approved (mirroring Add), so a video queued while the worker is
      // paused — which emits no progress SSE — still appears on Queue
      // immediately instead of only after the queue next drains.
      return (
        <Inbox
          onCountChange={setPendingCount}
          onOpenChannel={onOpenChannel}
          onOpen={onOpenInboxSummary}
          onOrderChange={setInboxOrder}
          search={inboxSearch}
          onSearchChange={onInboxSearchChange}
          onQueued={onQueued}
        />
      );
    case "upnext":
      return (
        <UpNext
          jobs={jobs}
          summaries={summaries}
          summaryPhaseByVideoId={summaryPhaseByVideoId}
          search={upNextSearch}
          onSearchChange={onUpNextSearchChange}
          onCancel={onCancelDownload}
          onOpenChannel={onOpenChannel}
          onOpenVideo={onOpenVideo}
          stalled={stalled}
        />
      );
    case "history":
      return (
        <History
          live={liveActivity}
          search={historySearch}
          onSearchChange={onHistorySearchChange}
          onOpenChannel={onOpenChannel}
          onOpenVideo={onOpenVideo}
        />
      );
    case "channels":
      return (
        <Channels
          live={liveActivity}
          onOpenChannel={onOpenChannel}
          onPendingChanged={onPendingChanged}
          search={channelSearch}
          onSearchChange={onChannelSearchChange}
        />
      );
    case "channel":
      return (
        <Channel
          channelId={selectedChannelId}
          onOpenVideo={onOpenVideo}
          onBack={() => setView("channels")}
          live={liveActivity}
          onQueued={onQueued}
          onPendingChanged={onPendingChanged}
        />
      );
    case "settings":
      return <Settings onStatusChanged={onStatusChanged} />;
  }
}
