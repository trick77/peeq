import { useEffect, useRef, useState } from "react";
import { Rail, type ViewId } from "./shell/Rail";
import { TopBar } from "./shell/TopBar";
import { getMe, listDownloads, cookieHealth, streamDownloads } from "./api";
import type { Job, User } from "./api/types";
import { Library } from "./views/Library";
import { Add } from "./views/Add";
import { Player } from "./views/Player";
import { Settings } from "./views/Settings";

// Page titles/subtitles per view, per the mockup's `titles` map.
const VIEW_META: Record<ViewId, { title: string; subtitle?: string }> = {
  library: { title: "Library" },
  player: { title: "Now playing" },
  add: { title: "Add a video" },
  pending: { title: "New & pending" },
  settings: { title: "Settings" },
};

// App — the shell (rail + topbar + routed main) plus the four Task 14
// views. Routing is manual view-state, no router lib — matches loom's
// pattern for a single-page app this size.
export function App() {
  const [view, setView] = useState<ViewId>("library");
  const [user, setUser] = useState<User | null>(null);
  const [authChecked, setAuthChecked] = useState(false);
  const [authError, setAuthError] = useState(false);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [cookieStatus, setCookieStatus] = useState<string | undefined>(undefined);
  const [selectedVideoId, setSelectedVideoId] = useState<string | null>(null);
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
    return () => {
      active = false;
    };
  }, [authChecked, user]);

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
        <b>vark</b>
      </div>
    );
  }

  if (!user) {
    return (
      <div style={{ display: "grid", placeItems: "center", minHeight: "100vh", gap: 12 }}>
        <b>vark</b>
        {authError ? (
          <p style={{ color: "var(--color-danger)" }}>Couldn't reach the server. Try reloading.</p>
        ) : null}
        <a href="/api/auth/login">Sign in</a>
      </div>
    );
  }

  const meta = VIEW_META[view];
  const pendingCount = jobs.filter((j) => j.state === "pending" || j.state === "running").length;

  function openVideo(id: string) {
    setSelectedVideoId(id);
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
          <ViewSwitch view={view} selectedVideoId={selectedVideoId} onOpenVideo={openVideo} setView={setView} />
        </section>
      </main>
    </div>
  );
}

function ViewSwitch({
  view,
  selectedVideoId,
  onOpenVideo,
  setView,
}: {
  view: ViewId;
  selectedVideoId: string | null;
  onOpenVideo: (id: string) => void;
  setView: (v: ViewId) => void;
}) {
  switch (view) {
    case "library":
      return <Library onOpenVideo={onOpenVideo} />;
    case "player":
      return <Player videoId={selectedVideoId} onDeleted={() => setView("library")} />;
    case "add":
      // Stay on the Add page after queuing (per the mockup — the preview
      // card confirms the queue, it doesn't jump into Player before the
      // download has even started); onOpenVideo is Library's job.
      return <Add onQueued={() => {}} />;
    case "pending":
      return <p>New &amp; pending — coming in a later phase.</p>;
    case "settings":
      return <Settings />;
  }
}
