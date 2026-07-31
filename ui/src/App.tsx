import { useCallback, useEffect, useRef, useState } from "react";
import { Rail, type ViewId } from "./shell/Rail";
import {
  getMe,
  listDownloads,
  cookieHealth,
  downloadsStatus,
  getPlaybackState,
  streamDownloads,
  listPending,
  listSummaries,
  cancelDownload,
  resumeYoutube,
} from "./api";
import type { DownloadsStatus } from "./api/downloads";
import { getYtdlpVersion, type YtdlpVersion } from "./api/ytdlp";
import type { ActivityEvent, Job, SummaryJob, User } from "./api/types";
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
import { Share } from "./views/Share";
import { useRoute } from "./route";
import { Button } from "./ui";

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
  // The video opened from the Inbox to read its summary, if that is the page
  // currently showing. An inbox video has no media yet, so /video/<id> renders
  // what peeq read of it rather than a player — deliberately the same URL the
  // Library opens, so that nothing changes when the file later arrives
  // (Inbox.tsx). That sharing is what this exists to disambiguate: the route
  // alone cannot say whether /video/<id> is a video playing or a summary being
  // read, and the two want different things from the rail and from "Now
  // playing".
  //
  // Held as the video's id rather than a bare "came from the Inbox" flag, so it
  // is a claim about one specific page instead of a mode the app is left in.
  // That makes it self-expiring: navigating to another video simply stops
  // matching, and walking back to the summary with the browser's back button
  // starts matching again, with no clearing logic to keep in step.
  const [inboxSummaryId, setInboxSummaryId] = useState<string | null>(null);
  const readingInboxSummary =
    view === "player" && !!route.videoId && route.videoId === inboxSummaryId;
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
  // playing" return to your video after a detour through Channels. But once an
  // inbox summary could occupy /video/<id>, that memory could hold a video with
  // no file, and skipping the re-read on it made a fileless video shadow the
  // real pointer for the rest of the session: read one summary, go anywhere,
  // and "Now playing" reopened the summary rather than the video you were
  // actually watching. The pointer is already guarded — the backend only ever
  // points at a downloaded video (playback.Store) — so the fix is to consult it
  // rather than the memory whenever the memory is not a video being watched.
  const setView = useCallback(
    (v: ViewId) => {
      // The URL already shows a video being watched: this click is a no-op, and
      // must stay one. Re-reading here would let a pointer another device moved
      // navigate you out of what you are watching.
      const watchingAVideo = !!route.videoId && !readingInboxSummary;
      if (v !== "player" || (view === "player" && watchingAVideo)) {
        navigate({ view: v });
        return;
      }
      void getPlaybackState()
        .then((p) => {
          const fresh = p.video_id || null;
          setPersistedVideoId(fresh);
          // The pointer only ever names a downloaded video (playback.Store), so
          // a pointer naming the video remembered as an inbox summary is that
          // video after its file arrived — it is being watched now, not read.
          // Dropping the marker keeps the rail off Inbox on the page this click
          // opens, and keeps the next click on this item short-circuiting the
          // way it does for any other video being watched.
          if (fresh && fresh === inboxSummaryId) setInboxSummaryId(null);
          navigate({ view: "player", videoId: fresh });
        })
        // A failed read must be no worse than not asking, and off the Player
        // that now means falling back to the route's memory before the copy
        // loaded at bootstrap: the id the URL is carrying is the last video
        // this tab opened, and until this callback started re-reading off the
        // Player, a click here returned to it without asking anything. The
        // bootstrap copy can easily be null on a tab that cold-loaded "/" —
        // dropping to "Nothing playing" because one GET failed would lose the
        // video you were watching a moment ago. An inbox summary sitting in
        // that memory is excluded by id, the same thing readingInboxSummary
        // checks and the reason this can't just reuse it (that flag is false
        // off the Player, where this runs).
        .catch(() =>
          navigate({
            view: "player",
            videoId:
              (route.videoId !== inboxSummaryId ? route.videoId : null) ??
              persistedVideoId,
          }),
        );
    },
    [
      navigate,
      view,
      route.videoId,
      readingInboxSummary,
      inboxSummaryId,
      persistedVideoId,
    ],
  );
  const [user, setUser] = useState<User | null>(null);
  const [authChecked, setAuthChecked] = useState(false);
  const [authError, setAuthError] = useState(false);
  const [jobs, setJobs] = useState<Job[]>([]);
  // undefined until the first listPending() lands. The rail greys Inbox out on
  // a real 0, so a 0 default would flash the item dim on every cold load.
  const [pendingCount, setPendingCount] = useState<number | undefined>(
    undefined,
  );
  // The in-flight summary queue (pending/running), for the Queue page and the
  // rail's Queue badge. Kept alongside jobs because summaries are the second
  // half of "work in flight" — the Queue count is downloads + summaries.
  const [summaries, setSummaries] = useState<SummaryJob[]>([]);
  // Whether the two halves of the queue count have ever loaded successfully.
  // `jobs`/`summaries` start as empty arrays — indistinguishable from a genuinely
  // empty queue — and the rail greys Queue out on a real 0, so without these the
  // item would dim on every cold paint until the first fetch lands. A failed
  // fetch deliberately leaves the flag down: unknown is not empty.
  const [jobsLoaded, setJobsLoaded] = useState(false);
  const [summariesLoaded, setSummariesLoaded] = useState(false);
  // Live summary phase per video (summarizing → embedding), accumulated from
  // the same "summary" SSE event the Player consumes, so the Queue lane can
  // show a job advancing without a reload.
  const [summaryPhaseByVideoId, setSummaryPhaseByVideoId] = useState<
    Record<string, string>
  >({});
  // A bounded buffer of the newest background-work events, appended from the
  // "activity" SSE event. The Activity page loads its own history and merges
  // these in by id, so the single session SSE subscription stays in App.
  const [liveActivity, setLiveActivity] = useState<ActivityEvent[]>([]);
  const [cookieStatus, setCookieStatus] = useState<string | undefined>(
    undefined,
  );
  // Fetched once per session alongside the cookie health, for the same reason:
  // the backend only asks upstream every few hours, so polling this would just
  // re-read a cache. undefined until it loads, which the rail reads as "say
  // nothing".
  const [ytdlp, setYtdlp] = useState<YtdlpVersion | undefined>(undefined);
  const [downloadStatus, setDownloadStatus] = useState<DownloadsStatus>({
    paused: false,
    low_disk: false,
    youtube_paused: false,
    youtube_pause_reason: "",
  });
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
  // How many jobs are pending or running. A plain count is what the queue poll
  // and the rail's queue badge want.
  const activeDownloads = jobs.filter(
    (j) => j.state === "pending" || j.state === "running",
  ).length;
  // The IDENTITY of those same jobs, as a stable string, is what Library needs
  // as its refetch trigger. A count is lossy: with a queue of depth one, job A
  // finishing while job B is enqueued in the same poll window leaves the count
  // at 1, and a Library watching only the number would never learn A's video
  // had arrived — a channel sweep holding the queue at a steady depth would
  // keep the grid stale for the whole batch. Comparing ids catches the swap.
  const queueSignal = jobs
    .filter((j) => j.state === "pending" || j.state === "running")
    .map((j) => j.job_id)
    .sort((a, b) => a - b)
    .join(",");
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
  const [progressByJobId, setProgressByJobId] = useState<
    Record<number, { percent: number; speed: string; eta: string }>
  >({});
  const jobsRef = useRef<Job[]>([]);
  useEffect(() => {
    jobsRef.current = jobs;
  }, [jobs]);

  useEffect(() => {
    let active = true;
    getMe()
      .then((u) => {
        if (active) setUser(u);
      })
      .catch(() => {
        // getMe() rejecting (backend down, network error, or a non-401
        // failure the client surfaced as a thrown error) must never become
        // an unhandled rejection — treat it the same as "not signed in".
        if (active) setAuthError(true);
      })
      .finally(() => {
        if (active) setAuthChecked(true);
      });
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    if (!authChecked || !user) return;
    let active = true;
    listDownloads()
      .then((j) => {
        if (!active) return;
        setJobs(j);
        setJobsLoaded(true);
      })
      .catch(() => {});
    listSummaries()
      .then((s) => {
        if (!active) return;
        setSummaries(s);
        setSummariesLoaded(true);
      })
      .catch(() => {});
    cookieHealth()
      .then((h) => {
        if (active) setCookieStatus(h.status);
      })
      .catch(() => {});
    getYtdlpVersion()
      .then((v) => {
        if (active) setYtdlp(v);
      })
      .catch(() => {});
    // Best-effort, like the two beside it: a rail that can't load the pointer
    // falls back to its old in-memory behaviour rather than showing an error for
    // a convenience. An empty video_id means nothing is playing — which also
    // covers a pointer whose video has since been deleted.
    getPlaybackState()
      .then((p) => {
        if (active) setPersistedVideoId(p.video_id || null);
      })
      .catch(() => {});
    downloadsStatus()
      .then((s) => {
        if (active) setDownloadStatus(s);
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, [authChecked, user]);

  // Refetch the Inbox count on demand. Beyond the navigation-triggered load
  // below, the SSE handler calls this whenever background work reports in: a
  // scan can surface new videos to decide while the user sits on another page,
  // and the rail now greys Inbox out when the count is 0 — a stale 0 would
  // claim there is nothing to decide when there is.
  const refreshPending = useCallback(() => {
    listPending()
      .then((p) => setPendingCount(p.length))
      .catch(() => {});
  }, []);

  // The rail's "Inbox" badge reflects the channel_videos ledger's pending
  // count (Task 14), not the download-jobs queue — loaded once on sign-in and
  // refetched whenever the user navigates into the Inbox view itself, so
  // acting on an item (download/ignore) there updates the badge without a
  // manual refresh.
  useEffect(() => {
    if (!authChecked || !user) return;
    let active = true;
    listPending()
      .then((p) => {
        if (active) setPendingCount(p.length);
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, [authChecked, user, view]);

  // When the worker reports it is paused (a cookie problem stalled the queue),
  // refresh the cookie status so the rail's indicator reflects the current
  // blocked/expired/absent state rather than a stale "active".
  useEffect(() => {
    if (!downloadStatus.paused) return;
    let active = true;
    cookieHealth()
      .then((h) => {
        if (active) setCookieStatus(h.status);
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, [downloadStatus.paused]);

  // Refetch the queue on demand — used when a view queues something itself
  // (the Add view) so the dock reflects it immediately. This is also what
  // bootstraps the poll below: that interval only starts once `jobs` holds
  // an active entry, so something has to seed the first one.
  const refreshQueue = useCallback(() => {
    listDownloads()
      .then((j) => {
        setJobs(j);
        setJobsLoaded(true);
      })
      .catch(() => {});
    downloadsStatus()
      .then((s) => setDownloadStatus(s))
      .catch(() => {});
  }, []);

  // Cancel a download from the Queue page, then refresh so the row leaves the
  // list even if no further progress/terminal SSE arrives for it.
  const onCancelDownload = useCallback(
    (jobId: number) => {
      cancelDownload(jobId)
        .catch(() => {})
        .finally(refreshQueue);
    },
    [refreshQueue],
  );

  // Re-list the in-flight summaries and prune the phase map to match. Pruning
  // is what keeps summaryPhaseByVideoId bounded: a job that has left the queue
  // is no longer in the list, so its phase entry is dropped rather than
  // accumulating for every video summarized this session — and a re-summarized
  // video can't inherit a stale phase label from its previous run.
  const refreshSummaries = useCallback(() => {
    listSummaries()
      .then((list) => {
        setSummaries(list);
        setSummariesLoaded(true);
        setSummaryPhaseByVideoId((prev) => {
          const next: Record<string, string> = {};
          for (const s of list) {
            if (prev[s.video_id] !== undefined)
              next[s.video_id] = prev[s.video_id];
          }
          return next;
        });
      })
      .catch(() => {});
  }, []);

  // Poll the queue every 3s while any job is pending/running. There is no
  // SSE "job finished" event (the worker only ever publishes "progress"),
  // so without this a job that completes right after its last progress
  // tick would leave the dock stuck showing it as still active forever.
  // Cheap and self-limiting: the interval only runs while something is
  // actually in flight.
  //
  // Note the bootstrap dependency: `hasActive` reads the CURRENT jobs, so
  // an empty dock never starts polling on its own. Adding the first video
  // has to seed `jobs` via refreshQueue above — the SSE progress handler
  // can't cover it, since a job that is queued but not yet downloading
  // (worker paused, cookie missing, queue busy) emits no progress at all.
  useEffect(() => {
    if (!authChecked || !user) return;
    // Poll while EITHER lane has work. Downloads have no "finished" SSE, so the
    // poll is what retires a completed job. For summaries the poll tracks a job
    // already in flight through to completion; a summary enqueued while both
    // lanes are idle is picked up instead by the worker's "summarizing" SSE
    // event when it claims the job (within a poll interval), which re-arms this
    // effect — the poll does not itself observe an unclaimed pending summary.
    if (activeDownloads === 0 && summaries.length === 0) return;
    const id = window.setInterval(() => {
      listDownloads()
        .then((j) => setJobs(j))
        .catch(() => {});
      refreshSummaries();
      // Refresh the stalled-queue state alongside the queue so the diagnostic
      // banner clears/appears as the worker pauses or resumes.
      downloadsStatus()
        .then((s) => setDownloadStatus(s))
        .catch(() => {});
    }, 3000);
    return () => window.clearInterval(id);
  }, [authChecked, user, activeDownloads, summaries.length, refreshSummaries]);

  // Live download progress for the rail's dock (Task 14 carry-forward):
  // accumulate per-job percent/speed/eta from the SSE feed directly rather
  // than re-polling listDownloads() on every tick (that fires multiple
  // times/sec while a download is active). The queue itself (new/finished
  // jobs) is still refreshed from the one-shot listDownloads() above; a job
  // transitioning to a terminal state simply stops receiving progress
  // events, which is enough for the dock (it only ever shows the lead
  // active job).
  useEffect(() => {
    if (!authChecked || !user) return;
    const controller = new AbortController();
    streamDownloads((evt) => {
      // The one SSE stream carries download "progress", summary "summary", and
      // background-work "activity" events (see the shared hub in main.go).
      if (evt.event === "activity") {
        const e = evt.data as ActivityEvent;
        // Keep a bounded buffer of the newest events; the Activity page merges
        // them into its log by id, so a live row appears without a reload.
        setLiveActivity((prev) => [...prev, e].slice(-50));
        // A scan that surfaced new videos changes the Inbox count, and
        // retention can remove rows from under it. Activity events are rare by
        // design (the scheduler's silence rule writes nothing for a scan that
        // found nothing), so refreshing on all of them costs next to nothing
        // and keeps the rail's greyed-out state honest.
        refreshPending();
        return;
      }
      if (evt.event === "summary") {
        const s = evt.data as {
          video_id?: string;
          status?: string;
          phase?: string;
        };
        if (s.video_id) {
          setSummaryPhaseByVideoId((prev) => ({
            ...prev,
            [s.video_id as string]: s.phase ?? s.status ?? "",
          }));
        }
        // Any phase transition changes the in-flight set — a job just started
        // (running/summarizing) or just left it (done/error/no_transcript) —
        // so re-list so the Queue lane and its badge match (and the phase map
        // is pruned to the survivors).
        refreshSummaries();
        return;
      }
      if (evt.event !== "progress") return;
      const data = evt.data as {
        job_id: number;
        percent: number;
        speed: string;
        eta: string;
      };
      setProgressByJobId((prev) => ({
        ...prev,
        [data.job_id]: {
          percent: data.percent,
          speed: data.speed,
          eta: data.eta,
        },
      }));
      // A progress event for a job we haven't seen yet means a download
      // was queued after the initial listDownloads() load (e.g. from the
      // Add view) — refresh the queue once so the dock's "N queued" count
      // and lead job pick it up.
      if (!jobsRef.current.some((j) => j.job_id === data.job_id)) {
        listDownloads()
          .then((j) => setJobs(j))
          .catch(() => {});
      }
    }, controller.signal).catch(() => {});
    return () => controller.abort();
  }, [authChecked, user, refreshSummaries, refreshPending]);

  // The public share page renders above everything else — no rail, no top bar,
  // and crucially before the auth gate below, since its whole point is to work
  // for a recipient who is not signed in.
  if (view === "share") {
    return <Share token={route.token} />;
  }

  if (!authChecked) {
    return (
      <div
        style={{ display: "grid", placeItems: "center", minHeight: "100vh" }}
      >
        <b>Peeq</b>
      </div>
    );
  }

  if (!user) {
    return (
      <div
        style={{
          display: "grid",
          placeItems: "center",
          minHeight: "100vh",
          gap: 12,
        }}
      >
        <b>Peeq</b>
        {authError ? (
          <p style={{ color: "var(--color-danger)" }}>
            Couldn't reach the server. Try reloading.
          </p>
        ) : null}
        <a href="/api/auth/login">Sign in</a>
      </div>
    );
  }

  function openVideo(id: string) {
    setPendingSeek(undefined);
    setInboxSummaryId(null);
    navigate({ view: "player", videoId: id });
  }

  // openInboxSummary — the Inbox's onOpen. Same destination as openVideo, and
  // deliberately so: an inbox video's page is the video's page, it just has no
  // file to play yet. The one difference is that this records which video it
  // was, so the shell can tell a summary being read from a video being watched
  // (see inboxSummaryId).
  function openInboxSummary(id: string) {
    setPendingSeek(undefined);
    setInboxSummaryId(id);
    navigate({ view: "player", videoId: id });
  }

  // openVideoAt — Search's onOpen: jumps into the Player at a specific
  // moment (a matched transcript/summary chunk's start_seconds). The seek
  // target stays in App state, not the URL — it is transient sub-page state,
  // out of the deep-link scope.
  function openVideoAt(id: string, startSeconds: number) {
    setPendingSeek(startSeconds);
    setInboxSummaryId(null);
    navigate({ view: "player", videoId: id });
  }

  // openChannel is the channel page's only entry point: there is no rail
  // item for it, so this is called from every place a channel name appears.
  function openChannel(id: string) {
    navigate({ view: "channel", channelId: id });
  }

  return (
    <div className="app-shell">
      <Rail
        // Reading an inbox video's summary keeps the rail on Inbox. The page is
        // reached from there and nowhere else, so lighting "Now playing" for it
        // told you both where you weren't and something untrue — nothing is
        // playing, there is no file yet.
        active={readingInboxSummary ? "inbox" : view}
        onNavigate={setView}
        pendingCount={pendingCount}
        upNextCount={
          jobsLoaded && summariesLoaded
            ? activeDownloads + summaries.length
            : undefined
        }
        // The pill is a "something is happening" light, not a backlog size, so
        // it needs a job actually running in either lane. Both lanes count: a
        // running summary lights it exactly as a running download does.
        upNextLive={
          jobs.some((j) => j.state === "running") ||
          summaries.some((s) => s.state === "running")
        }
        cookieStatus={cookieStatus}
        ytdlp={ytdlp}
      />
      <main className="main">
        <section className="page">
          <DownloadStatusBanner
            status={downloadStatus}
            onFixCookie={() => setView("settings")}
            onResume={async () => {
              await resumeYoutube();
              setDownloadStatus(await downloadsStatus());
            }}
          />
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
            onOpenChannel={openChannel}
            onSeekConsumed={() => setPendingSeek(undefined)}
            setView={setView}
            setPendingCount={setPendingCount}
            onQueued={refreshQueue}
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
            progressByJobId={progressByJobId}
            summaries={summaries}
            summaryPhaseByVideoId={summaryPhaseByVideoId}
            onCancelDownload={onCancelDownload}
            liveActivity={liveActivity}
            // Same precedence the banner uses: the kill-switch outranks a full
            // disk, which outranks the cookie pause. Up next names the cause
            // because each one has a different way out — only the kill-switch
            // has a Resume button.
            stalled={
              downloadStatus.youtube_paused
                ? "youtube"
                : downloadStatus.low_disk
                  ? "disk"
                  : downloadStatus.paused
                    ? "cookie"
                    : undefined
            }
          />
        </section>
      </main>
    </div>
  );
}

// DownloadStatusBanner shows why the download queue is stalled, so a paused
// queue is diagnosable at a glance instead of looking silently broken. Renders
// nothing when the queue is healthy. Low disk takes precedence over the cookie
// pause (a full disk blocks downloads regardless of cookie state). The
// YouTube kill-switch pause (youtube_paused) outranks both and is checked
// first, since it is a deliberate all-activity stop the user asked for.
function DownloadStatusBanner({
  status,
  onFixCookie,
  onResume,
}: {
  status: DownloadsStatus;
  onFixCookie: () => void;
  onResume: () => void;
}) {
  if (status.youtube_paused) {
    const auto = status.youtube_pause_reason !== "";
    return (
      <div className="errline" role="status">
        <span className="msg">
          <b>YouTube activity is paused.</b>{" "}
          {auto
            ? status.youtube_pause_reason
            : "You paused all downloads and channel scans."}
        </span>
        <Button type="button" onClick={onResume}>
          Resume
        </Button>
      </div>
    );
  }
  if (status.low_disk) {
    return (
      <div className="errline" role="status">
        Downloads paused — low disk space. Free up space to resume.
      </div>
    );
  }
  if (status.paused) {
    return (
      <div className="errline" role="status">
        Downloads paused — re-paste your YouTube cookie in{" "}
        <button
          type="button"
          onClick={onFixCookie}
          style={{
            background: "none",
            border: "none",
            padding: 0,
            color: "inherit",
            textDecoration: "underline",
            cursor: "pointer",
            font: "inherit",
          }}
        >
          Settings
        </button>
        .
      </div>
    );
  }
  return null;
}

function ViewSwitch({
  view,
  selectedVideoId,
  selectedChannelId,
  pendingSeek,
  onOpenVideo,
  onOpenInboxSummary,
  onOpenVideoAt,
  onOpenChannel,
  onSeekConsumed,
  setView,
  setPendingCount,
  onQueued,
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
  progressByJobId,
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
  onOpenChannel: (id: string) => void;
  onSeekConsumed: () => void;
  setView: (v: ViewId) => void;
  // undefined = the count is not known (a failed inbox fetch). The rail draws
  // no pill for it, which is deliberately not the same claim as "empty".
  setPendingCount: (n: number | undefined) => void;
  onQueued: () => void;
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
  progressByJobId: Record<
    number,
    { percent: number; speed: string; eta: string }
  >;
  summaries: SummaryJob[];
  summaryPhaseByVideoId: Record<string, string>;
  onCancelDownload: (jobId: number) => void;
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
          onDeleted={() => setView("library")}
          onOpenChannel={onOpenChannel}
          onQueued={onQueued}
          onBackToInbox={() => setView("inbox")}
          inboxOrder={inboxOrder}
          // The summary page's Prev/Next stepper, and the hop a decision makes
          // to the item after it. Both stay inside the Inbox, so they open the
          // next video the same way the grid did — openVideo would clear
          // inboxSummaryId and leave the stepped-to video reading as one being
          // watched: the rail would announce "Now playing" over a file that
          // does not exist yet, and "Now playing" would be a dead click.
          onOpenInboxVideo={onOpenInboxSummary}
        />
      );
    case "search":
      return <Search onOpen={onOpenVideoAt} />;
    case "add":
      // Stay on the Add page after queuing (per the mockup — the preview
      // card confirms the queue, it doesn't jump into Player before the
      // download has even started); onOpenVideo is Library's job.
      return <Add onQueued={onQueued} />;
    case "inbox":
      // onCountChange keeps the rail badge in sync while the user acts on
      // items (Download/Ignore) without leaving this view — the
      // nav-refetch effect above only covers count changes that happen
      // while the user is elsewhere. onQueued seeds the download poll the
      // moment an item is approved (mirroring Add), so a video queued while
      // the worker is paused — which emits no progress SSE — still appears on
      // Queue immediately instead of only after the queue next drains.
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
          progressByJobId={progressByJobId}
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
          onOpenChannel={onOpenChannel}
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
        />
      );
    case "settings":
      return <Settings />;
  }
}
