import { useCallback, useEffect, useRef, useState } from "react";
import { Rail, type ViewId } from "./shell/Rail";
import { TopBar } from "./shell/TopBar";
import {
  getMe,
  listDownloads,
  cookieHealth,
  downloadsStatus,
  streamDownloads,
  listPending,
  listSummaries,
  cancelDownload,
  resumeYoutube,
} from "./api";
import type { DownloadsStatus } from "./api/downloads";
import type { ActivityEvent, Job, SummaryJob, User } from "./api/types";
import { Library } from "./views/Library";
import { Add } from "./views/Add";
import { Player } from "./views/Player";
import { Settings } from "./views/Settings";
import { Channels } from "./views/Channels";
import { Channel } from "./views/Channel";
import { Decide } from "./views/Decide";
import { Queue } from "./views/Queue";
import { Activity } from "./views/Activity";
import { Search } from "./views/Search";
import { useRoute } from "./route";
import { Button } from "./ui";

// Page titles/subtitles per view, per the mockup's `titles` map.
const VIEW_META: Record<ViewId, { title: string; subtitle?: string }> = {
  library: { title: "Library" },
  player: { title: "Now playing" },
  search: { title: "Search" },
  add: { title: "Add" },
  decide: { title: "Decide" },
  queue: { title: "Queue" },
  channels: { title: "Channels" },
  channel: { title: "Channel" },
  activity: { title: "Activity" },
  settings: { title: "Settings" },
};

// App — the shell (rail + topbar + routed main) plus the four Task 14
// views. Routing is manual view-state, no router lib — matches loom's
// pattern for a single-page app this size.
export function App() {
  // The URL is the source of truth for which page is open: `view` and the two
  // selected ids are derived from the path (route.ts), so a page can be deep-
  // linked, refreshed, and walked with the browser's back/forward buttons. A
  // cold-loaded /video/<id> therefore reopens the Player straight away (paused
  // at the server-side resume position via Player's handleLoadedMetadata seek),
  // which is what retired the old nowPlaying sessionStorage reload-restore.
  const { route, navigate } = useRoute();
  const view = route.view;
  const selectedVideoId = route.videoId;
  const selectedChannelId = route.channelId;
  // setView keeps every existing call site (the rail, the banner's "fix
  // cookie", ViewSwitch's back/deleted handlers) unchanged — it just pushes a
  // new URL. navigate is stable, so this is too.
  const setView = useCallback((v: ViewId) => navigate({ view: v }), [navigate]);
  const [user, setUser] = useState<User | null>(null);
  const [authChecked, setAuthChecked] = useState(false);
  const [authError, setAuthError] = useState(false);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [pendingCount, setPendingCount] = useState(0);
  // The in-flight summary queue (pending/running), for the Queue page and the
  // rail's Queue badge. Kept alongside jobs because summaries are the second
  // half of "work in flight" — the Queue count is downloads + summaries.
  const [summaries, setSummaries] = useState<SummaryJob[]>([]);
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
  const [downloadStatus, setDownloadStatus] = useState<DownloadsStatus>({
    paused: false,
    low_disk: false,
    youtube_paused: false,
    youtube_pause_reason: "",
  });
  // Search boxes for the two list pages that have one. Both live in the top
  // bar (which neither view renders itself), so their state is lifted here.
  // They are kept apart on purpose: a video-title query must not carry over
  // into a channel-name filter when you switch pages. The channel *detail*
  // page still keeps its own in-page search — the top bar isn't detail-aware.
  const [librarySearch, setLibrarySearch] = useState("");
  const [channelSearch, setChannelSearch] = useState("");
  // How many jobs are pending or running. It drives the queue poll below, and
  // is handed to Library as the signal to refetch — a video enters the library
  // exactly when its download leaves this count.
  const activeDownloads = jobs.filter(
    (j) => j.state === "pending" || j.state === "running",
  ).length;
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
        if (active) setJobs(j);
      })
      .catch(() => {});
    listSummaries()
      .then((s) => {
        if (active) setSummaries(s);
      })
      .catch(() => {});
    cookieHealth()
      .then((h) => {
        if (active) setCookieStatus(h.status);
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

  // The rail's "Pending" badge reflects the channel_videos ledger's
  // pending count (Task 14), not the download-jobs queue — loaded once on
  // sign-in and refetched whenever the user navigates into the Pending view
  // itself, so acting on an item (download/ignore) there updates the badge
  // without a manual refresh.
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
      .then((j) => setJobs(j))
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
  }, [authChecked, user, refreshSummaries]);

  if (!authChecked) {
    return (
      <div
        style={{ display: "grid", placeItems: "center", minHeight: "100vh" }}
      >
        <b>peeq</b>
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
        <b>peeq</b>
        {authError ? (
          <p style={{ color: "var(--color-danger)" }}>
            Couldn't reach the server. Try reloading.
          </p>
        ) : null}
        <a href="/api/auth/login">Sign in</a>
      </div>
    );
  }

  const meta = VIEW_META[view];

  function openVideo(id: string) {
    setPendingSeek(undefined);
    navigate({ view: "player", videoId: id });
  }

  // openVideoAt — Search's onOpen: jumps into the Player at a specific
  // moment (a matched transcript/summary chunk's start_seconds). The seek
  // target stays in App state, not the URL — it is transient sub-page state,
  // out of the deep-link scope.
  function openVideoAt(id: string, startSeconds: number) {
    setPendingSeek(startSeconds);
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
        active={view}
        onNavigate={setView}
        pendingCount={pendingCount}
        queueCount={activeDownloads + summaries.length}
        cookieStatus={cookieStatus}
      />
      <main className="main">
        <TopBar
          title={meta.title}
          subtitle={meta.subtitle}
          showSearch={view === "library" || view === "channels"}
          search={view === "channels" ? channelSearch : librarySearch}
          onSearchChange={
            view === "channels" ? setChannelSearch : setLibrarySearch
          }
          searchPlaceholder={
            view === "channels" ? "Search channels" : "Search titles"
          }
        />
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
            view={view}
            selectedVideoId={selectedVideoId}
            selectedChannelId={selectedChannelId}
            pendingSeek={pendingSeek}
            onOpenVideo={openVideo}
            onOpenVideoAt={openVideoAt}
            onOpenChannel={openChannel}
            onSeekConsumed={() => setPendingSeek(undefined)}
            setView={setView}
            setPendingCount={setPendingCount}
            onQueued={refreshQueue}
            librarySearch={librarySearch}
            channelSearch={channelSearch}
            activeDownloads={activeDownloads}
            jobs={jobs}
            progressByJobId={progressByJobId}
            summaries={summaries}
            summaryPhaseByVideoId={summaryPhaseByVideoId}
            onCancelDownload={onCancelDownload}
            liveActivity={liveActivity}
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
  onOpenVideoAt,
  onOpenChannel,
  onSeekConsumed,
  setView,
  setPendingCount,
  onQueued,
  librarySearch,
  channelSearch,
  activeDownloads,
  jobs,
  progressByJobId,
  summaries,
  summaryPhaseByVideoId,
  onCancelDownload,
  liveActivity,
}: {
  view: ViewId;
  selectedVideoId: string | null;
  selectedChannelId: string | null;
  pendingSeek: number | undefined;
  onOpenVideo: (id: string) => void;
  onOpenVideoAt: (id: string, startSeconds: number) => void;
  onOpenChannel: (id: string) => void;
  onSeekConsumed: () => void;
  setView: (v: ViewId) => void;
  setPendingCount: (n: number) => void;
  onQueued: () => void;
  librarySearch: string;
  channelSearch: string;
  activeDownloads: number;
  jobs: Job[];
  progressByJobId: Record<
    number,
    { percent: number; speed: string; eta: string }
  >;
  summaries: SummaryJob[];
  summaryPhaseByVideoId: Record<string, string>;
  onCancelDownload: (jobId: number) => void;
  liveActivity: ActivityEvent[];
}) {
  switch (view) {
    case "library":
      return (
        <Library
          onOpenVideo={onOpenVideo}
          onOpenChannel={onOpenChannel}
          search={librarySearch}
          activeDownloads={activeDownloads}
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
        />
      );
    case "search":
      return <Search onOpen={onOpenVideoAt} />;
    case "add":
      // Stay on the Add page after queuing (per the mockup — the preview
      // card confirms the queue, it doesn't jump into Player before the
      // download has even started); onOpenVideo is Library's job.
      return <Add onQueued={onQueued} />;
    case "decide":
      // onCountChange keeps the rail badge in sync while the user acts on
      // items (Download now/Ignore) without leaving this view — the
      // nav-refetch effect above only covers count changes that happen
      // while the user is elsewhere. onQueued seeds the download poll the
      // moment an item is approved (mirroring Add), so a video queued while
      // the worker is paused — which emits no progress SSE — still appears on
      // Queue immediately instead of only after the queue next drains.
      return (
        <Decide
          onCountChange={setPendingCount}
          onOpenChannel={onOpenChannel}
          onQueued={onQueued}
        />
      );
    case "queue":
      return (
        <Queue
          jobs={jobs}
          progressByJobId={progressByJobId}
          summaries={summaries}
          summaryPhaseByVideoId={summaryPhaseByVideoId}
          onCancel={onCancelDownload}
        />
      );
    case "activity":
      return (
        <Activity
          live={liveActivity}
          jobs={jobs}
          progressByJobId={progressByJobId}
          summaries={summaries}
          summaryPhaseByVideoId={summaryPhaseByVideoId}
        />
      );
    case "channels":
      return <Channels onOpenChannel={onOpenChannel} search={channelSearch} />;
    case "channel":
      return (
        <Channel
          channelId={selectedChannelId}
          onOpenVideo={onOpenVideo}
          onBack={() => setView("channels")}
        />
      );
    case "settings":
      return <Settings />;
  }
}
