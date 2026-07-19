import { useEffect, useRef, useState } from "react";
import { Rail, type ViewId } from "./shell/Rail";
import { TopBar } from "./shell/TopBar";
import { getMe, listDownloads, cookieHealth, downloadsStatus, streamDownloads, listPending } from "./api";
import type { DownloadsStatus } from "./api/downloads";
import type { Job, User } from "./api/types";
import { Library } from "./views/Library";
import { Add } from "./views/Add";
import { Player } from "./views/Player";
import { Settings } from "./views/Settings";
import { Channels } from "./views/Channels";
import { Pending } from "./views/Pending";
import { Search } from "./views/Search";
import { readNowPlaying } from "./nowPlaying";

// Page titles/subtitles per view, per the mockup's `titles` map.
const VIEW_META: Record<ViewId, { title: string; subtitle?: string }> = {
  library: { title: "Library" },
  player: { title: "Now playing" },
  search: { title: "Search" },
  add: { title: "Add a video" },
  pending: { title: "Pending" },
  channels: { title: "Channels" },
  settings: { title: "Settings" },
};

// App — the shell (rail + topbar + routed main) plus the four Task 14
// views. Routing is manual view-state, no router lib — matches loom's
// pattern for a single-page app this size.
export function App() {
  // Reload-restore: if a video was actively playing when the page was last
  // torn down (a reload, not an in-app navigation — Player clears the marker
  // on unmount), reopen the Player on it. It opens paused at the server-side
  // resume position via Player's existing handleLoadedMetadata seek — no
  // autoplay. Read once, synchronously, so the very first render already
  // routes to the Player instead of flashing Library. See nowPlaying.ts.
  const restored = readNowPlaying();
  const [view, setView] = useState<ViewId>(restored?.playing ? "player" : "library");
  const [user, setUser] = useState<User | null>(null);
  const [authChecked, setAuthChecked] = useState(false);
  const [authError, setAuthError] = useState(false);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [pendingCount, setPendingCount] = useState(0);
  const [cookieStatus, setCookieStatus] = useState<string | undefined>(undefined);
  const [downloadStatus, setDownloadStatus] = useState<DownloadsStatus>({ paused: false, low_disk: false });
  const [selectedVideoId, setSelectedVideoId] = useState<string | null>(
    restored?.playing ? restored.videoId : null,
  );
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

  // Poll the queue every 3s while any job is pending/running. There is no
  // SSE "job finished" event (the worker only ever publishes "progress"),
  // so without this a job that completes right after its last progress
  // tick would leave the dock stuck showing it as still active forever.
  // Cheap and self-limiting: the interval only runs while something is
  // actually in flight.
  useEffect(() => {
    if (!authChecked || !user) return;
    const hasActive = jobs.some((j) => j.state === "pending" || j.state === "running");
    if (!hasActive) return;
    const id = window.setInterval(() => {
      listDownloads()
        .then((j) => setJobs(j))
        .catch(() => {});
      // Refresh the stalled-queue state alongside the queue so the diagnostic
      // banner clears/appears as the worker pauses or resumes.
      downloadsStatus()
        .then((s) => setDownloadStatus(s))
        .catch(() => {});
    }, 3000);
    return () => window.clearInterval(id);
  }, [authChecked, user, jobs]);

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
      if (evt.event !== "progress") return;
      const data = evt.data as { job_id: number; percent: number; speed: string; eta: string };
      setProgressByJobId((prev) => ({
        ...prev,
        [data.job_id]: { percent: data.percent, speed: data.speed, eta: data.eta },
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
  }, [authChecked, user]);

  if (!authChecked) {
    return (
      <div style={{ display: "grid", placeItems: "center", minHeight: "100vh" }}>
        <b>peeq</b>
      </div>
    );
  }

  if (!user) {
    return (
      <div style={{ display: "grid", placeItems: "center", minHeight: "100vh", gap: 12 }}>
        <b>peeq</b>
        {authError ? (
          <p style={{ color: "var(--color-danger)" }}>Couldn't reach the server. Try reloading.</p>
        ) : null}
        <a href="/api/auth/login">Sign in</a>
      </div>
    );
  }

  const meta = VIEW_META[view];

  function openVideo(id: string) {
    setSelectedVideoId(id);
    setPendingSeek(undefined);
    setView("player");
  }

  // openVideoAt — Search's onOpen: jumps into the Player at a specific
  // moment (a matched transcript/summary chunk's start_seconds).
  function openVideoAt(id: string, startSeconds: number) {
    setSelectedVideoId(id);
    setPendingSeek(startSeconds);
    setView("player");
  }

  return (
    <div className="app-shell">
      <Rail
        active={view}
        onNavigate={setView}
        pendingCount={pendingCount}
        jobs={jobs}
        progressByJobId={progressByJobId}
        cookieStatus={cookieStatus}
      />
      <main className="main">
        <TopBar title={meta.title} subtitle={meta.subtitle} showSearch={view === "library"} />
        <section className="page">
          <DownloadStatusBanner status={downloadStatus} onFixCookie={() => setView("settings")} />
          <ViewSwitch
            view={view}
            selectedVideoId={selectedVideoId}
            pendingSeek={pendingSeek}
            onOpenVideo={openVideo}
            onOpenVideoAt={openVideoAt}
            onSeekConsumed={() => setPendingSeek(undefined)}
            setView={setView}
            setPendingCount={setPendingCount}
          />
        </section>
      </main>
    </div>
  );
}

// DownloadStatusBanner shows why the download queue is stalled, so a paused
// queue is diagnosable at a glance instead of looking silently broken. Renders
// nothing when the queue is healthy. Low disk takes precedence over the cookie
// pause (a full disk blocks downloads regardless of cookie state).
function DownloadStatusBanner({
  status,
  onFixCookie,
}: {
  status: DownloadsStatus;
  onFixCookie: () => void;
}) {
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
          style={{ background: "none", border: "none", padding: 0, color: "inherit", textDecoration: "underline", cursor: "pointer", font: "inherit" }}
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
  pendingSeek,
  onOpenVideo,
  onOpenVideoAt,
  onSeekConsumed,
  setView,
  setPendingCount,
}: {
  view: ViewId;
  selectedVideoId: string | null;
  pendingSeek: number | undefined;
  onOpenVideo: (id: string) => void;
  onOpenVideoAt: (id: string, startSeconds: number) => void;
  onSeekConsumed: () => void;
  setView: (v: ViewId) => void;
  setPendingCount: (n: number) => void;
}) {
  switch (view) {
    case "library":
      return <Library onOpenVideo={onOpenVideo} />;
    case "player":
      return (
        <Player
          videoId={selectedVideoId}
          seekTo={pendingSeek}
          onSeekConsumed={onSeekConsumed}
          onDeleted={() => setView("library")}
        />
      );
    case "search":
      return <Search onOpen={onOpenVideoAt} />;
    case "add":
      // Stay on the Add page after queuing (per the mockup — the preview
      // card confirms the queue, it doesn't jump into Player before the
      // download has even started); onOpenVideo is Library's job.
      return <Add onQueued={() => {}} />;
    case "pending":
      // onCountChange keeps the rail badge in sync while the user acts on
      // items (Download now/Ignore) without leaving this view — the
      // nav-refetch effect above only covers count changes that happen
      // while the user is elsewhere.
      return <Pending onCountChange={setPendingCount} />;
    case "channels":
      return <Channels />;
    case "settings":
      return <Settings />;
  }
}
