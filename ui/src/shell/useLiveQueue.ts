import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  listDownloads,
  cookieHealth,
  downloadsStatus,
  cancelDownload as cancelDownloadApi,
  streamDownloads,
  listPending,
  listSummaries,
} from "../api";
import { getYtdlpVersion, type YtdlpVersion } from "../api/ytdlp";
import { streamWithBackoff } from "../api/reconnect";
import type { SSEEvent } from "../api/stream";
import type { DownloadsStatus, SummaryEventData } from "../api/downloads";
import type { SummaryStatus } from "../api/enums";
import type {
  ActivityEvent,
  DownloadProgress,
  DownloadProgressEvent,
  Job,
  SummaryJob,
} from "../api/types";

export type SummaryEvent = { videoId: string; status: SummaryStatus };
export type StalledReason = "youtube" | "disk" | "cookie";

export type LiveQueue = {
  jobs: Job[];
  jobsLoaded: boolean;
  summaries: SummaryJob[];
  summariesLoaded: boolean;
  summaryPhaseByVideoId: Record<string, string>;
  summaryEvent: SummaryEvent | null;
  liveActivity: ActivityEvent[];
  progressByJobId: Record<number, DownloadProgress>;
  pendingCount: number | undefined;
  setPendingCount: (n: number | undefined) => void;
  cookieStatus: string | undefined;
  ytdlp: YtdlpVersion | undefined;
  downloadStatus: DownloadsStatus;
  activeDownloads: number;
  queueSignal: string;
  stalled: StalledReason | undefined;
  refreshQueue: () => void;
  refreshSummaries: () => void;
  refreshPending: () => void;
  // Resolves once every light has been re-read (or failed to be), so a caller
  // can hold a control until the shell reflects the change.
  refreshStatus: () => Promise<void>;
  cancelDownload: (jobId: number) => Promise<void>;
};

const POLL_MS = 3000;
const ACTIVITY_BUFFER = 50;

// useLiveQueue owns everything the shell knows about work in flight: the two
// queue lanes (downloads and summaries), the per-job progress and per-video
// summary phase that the one SSE subscription feeds, the bounded activity
// buffer, and the status lights (cookie, yt-dlp, download worker) the rail
// and the banner read. `enabled` is "signed in": nothing here runs before the
// session check answers, and everything is torn down when the user leaves.
export function useLiveQueue(enabled: boolean): LiveQueue {
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
  // the "summary" SSE event so the Queue lane can show a job advancing without
  // a reload.
  const [summaryPhaseByVideoId, setSummaryPhaseByVideoId] = useState<
    Record<string, string>
  >({});
  // The last "summary" event off the stream, forwarded to the open video's
  // page. It used to subscribe to the stream itself for this — a second
  // connection re-decoding bytes this one had already decoded, and one that
  // stayed open for as long as the Player did, which since the now-playing
  // dock is the whole session. Held as state rather than pushed through a ref
  // because the page reacts to it in an effect.
  const [summaryEvent, setSummaryEvent] = useState<SummaryEvent | null>(null);
  // A bounded buffer of the newest background-work events, appended from the
  // "activity" SSE event. The Activity page loads its own history and merges
  // these in by id, so the single session SSE subscription stays here.
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
  // Live download progress for the rail's dock: accumulated per job from the
  // SSE feed directly rather than re-polling listDownloads() on every tick
  // (that fires multiple times a second while a download is active). Pruned
  // to the jobs still listed every time the list is adopted, so it stays
  // bounded to the queue rather than growing for every job of the session.
  const [progressByJobId, setProgressByJobId] = useState<
    Record<number, DownloadProgress>
  >({});
  const jobsRef = useRef<Job[]>([]);
  useEffect(() => {
    jobsRef.current = jobs;
  }, [jobs]);

  // Every listDownloads() consumer goes through here, so the prune above has
  // one place to happen.
  const adoptJobs = useCallback((list: Job[]) => {
    setJobs(list);
    setJobsLoaded(true);
    setProgressByJobId((prev) => {
      const next: Record<number, DownloadProgress> = {};
      for (const j of list) {
        if (prev[j.job_id] !== undefined) next[j.job_id] = prev[j.job_id];
      }
      return next;
    });
  }, []);

  // Refetch the queue on demand — used when a view queues something itself
  // (the Add view) so the dock reflects it immediately. This is also what
  // bootstraps the poll below: that interval only starts once `jobs` holds
  // an active entry, so something has to seed the first one.
  const refreshQueue = useCallback(() => {
    listDownloads()
      .then(adoptJobs)
      .catch(() => {});
    downloadsStatus()
      .then((s) => setDownloadStatus(s))
      .catch(() => {});
  }, [adoptJobs]);

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

  // Refetch the Inbox count on demand. The SSE handler calls this whenever
  // background work reports in: a scan can surface new videos to decide while
  // the user sits on another page, and the rail greys Inbox out when the count
  // is 0 — a stale 0 would claim there is nothing to decide when there is.
  const refreshPending = useCallback(() => {
    listPending()
      .then((p) => setPendingCount(p.length))
      .catch(() => {});
  }, []);

  // Best-effort like every read here: a light that cannot be read keeps its
  // last value.
  const refreshCookie = useCallback(
    () =>
      cookieHealth()
        .then((h) => setCookieStatus(h.status))
        .catch(() => {}),
    [],
  );
  const refreshYtdlp = useCallback(
    () =>
      getYtdlpVersion()
        .then((v) => setYtdlp(v))
        .catch(() => {}),
    [],
  );

  // Re-read the status lights a settings change can flip: the cookie (after a
  // paste), the worker's pause state (after resume) and the yt-dlp version
  // (after an update).
  const refreshStatus = useCallback(
    () =>
      Promise.all([
        refreshCookie(),
        refreshYtdlp(),
        downloadsStatus()
          .then((s) => setDownloadStatus(s))
          .catch(() => {}),
      ]).then(() => {}),
    [refreshCookie, refreshYtdlp],
  );

  // Cancel a download from the Queue page, then refresh so the row leaves the
  // list even if no further progress/terminal SSE arrives for it. The failure
  // is the caller's to show; the refresh happens either way.
  const cancelDownload = useCallback(
    (jobId: number) => cancelDownloadApi(jobId).finally(refreshQueue),
    [refreshQueue],
  );

  useEffect(() => {
    if (!enabled) return;
    refreshQueue();
    refreshSummaries();
    void refreshCookie();
    void refreshYtdlp();
  }, [enabled, refreshQueue, refreshSummaries, refreshCookie, refreshYtdlp]);

  // When the worker reports it is paused (a cookie problem stalled the queue),
  // refresh the cookie status so the rail's indicator reflects the current
  // blocked/expired/absent state rather than a stale "active".
  useEffect(() => {
    if (!downloadStatus.paused) return;
    void refreshCookie();
  }, [downloadStatus.paused, refreshCookie]);

  // How many jobs are pending or running. A plain count is what the queue poll
  // and the rail's queue badge want.
  const activeDownloads = useMemo(
    () =>
      jobs.filter((j) => j.state === "pending" || j.state === "running").length,
    [jobs],
  );
  // The IDENTITY of those same jobs, as a stable string, is what Library needs
  // as its refetch trigger. A count is lossy: with a queue of depth one, job A
  // finishing while job B is enqueued in the same poll window leaves the count
  // at 1, and a Library watching only the number would never learn A's video
  // had arrived — a channel sweep holding the queue at a steady depth would
  // keep the grid stale for the whole batch. Comparing ids catches the swap.
  const queueSignal = useMemo(
    () =>
      jobs
        .filter((j) => j.state === "pending" || j.state === "running")
        .map((j) => j.job_id)
        .sort((a, b) => a - b)
        .join(","),
    [jobs],
  );
  // Why the queue is stalled, if it is. The kill-switch outranks a full disk,
  // which outranks the cookie pause: each has a different way out, and only
  // the kill-switch has a Resume button.
  const stalled: StalledReason | undefined = downloadStatus.youtube_paused
    ? "youtube"
    : downloadStatus.low_disk
      ? "disk"
      : downloadStatus.paused
        ? "cookie"
        : undefined;

  // Poll the queue every 3s while any job is pending/running. There is no
  // SSE "job finished" event (the worker only ever publishes "progress"),
  // so without this a job that completes right after its last progress
  // tick would leave the dock stuck showing it as still active forever.
  // Cheap and self-limiting: the interval only runs while something is
  // actually in flight.
  //
  // Note the bootstrap dependency: `activeDownloads` reads the CURRENT jobs,
  // so an empty dock never starts polling on its own. Adding the first video
  // has to seed `jobs` via refreshQueue above — the SSE progress handler
  // can't cover it, since a job that is queued but not yet downloading
  // (worker paused, cookie missing, queue busy) emits no progress at all.
  useEffect(() => {
    if (!enabled) return;
    // Poll while EITHER lane has work. Downloads have no "finished" SSE, so the
    // poll is what retires a completed job. For summaries the poll tracks a job
    // already in flight through to completion; a summary enqueued while both
    // lanes are idle is picked up instead by the worker's "summarizing" SSE
    // event when it claims the job (within a poll interval), which re-arms this
    // effect — the poll does not itself observe an unclaimed pending summary.
    if (activeDownloads === 0 && summaries.length === 0) return;
    // refreshQueue also re-reads the stalled-queue state, so the diagnostic
    // banner clears/appears as the worker pauses or resumes.
    const id = window.setInterval(() => {
      refreshQueue();
      refreshSummaries();
    }, POLL_MS);
    return () => window.clearInterval(id);
  }, [
    enabled,
    activeDownloads,
    summaries.length,
    refreshQueue,
    refreshSummaries,
  ]);

  // The one SSE stream carries download "progress", summary "summary", and
  // background-work "activity" events (see the shared hub in main.go). Every
  // dependency here is stable for the hook's lifetime, so the handler is too,
  // and the subscription effect below never re-opens the stream on a render.
  const handleEvent = useCallback(
    (evt: SSEEvent) => {
      if (evt.event === "activity") {
        const e = evt.data as ActivityEvent;
        // Keep a bounded buffer of the newest events; the Activity page merges
        // them into its log by id, so a live row appears without a reload.
        setLiveActivity((prev) => [...prev, e].slice(-ACTIVITY_BUFFER));
        // A scan that surfaced new videos changes the Inbox count, and
        // retention can remove rows from under it. Activity events are rare by
        // design (the scheduler's silence rule writes nothing for a scan that
        // found nothing), so refreshing on all of them costs next to nothing
        // and keeps the rail's greyed-out state honest.
        refreshPending();
        return;
      }
      if (evt.event === "summary") {
        const s = evt.data as SummaryEventData;
        if (s.video_id) {
          setSummaryPhaseByVideoId((prev) => ({
            ...prev,
            [s.video_id as string]: s.phase ?? s.status ?? "",
          }));
        }
        // Hand it on to the open video's page.
        //
        // A NEW object every time, deliberately: two consecutive events can
        // carry identical values (status "running" twice as only the phase
        // moves), and the page has to react to the second one as well — what
        // matters is that an event ARRIVED, not that its value changed.
        if (s.video_id && s.status) {
          setSummaryEvent({ videoId: s.video_id, status: s.status });
        }
        // Any phase transition changes the in-flight set — a job just started
        // (running/summarizing) or just left it (done/error/no_transcript) —
        // so re-list so the Queue lane and its badge match (and the phase map
        // is pruned to the survivors).
        refreshSummaries();
        return;
      }
      if (evt.event !== "progress") return;
      const data = evt.data as DownloadProgressEvent;
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
          .then(adoptJobs)
          .catch(() => {});
      }
    },
    [adoptJobs, refreshPending, refreshSummaries],
  );

  // The stream carries deltas, not state: whatever happened while it was
  // down (a restart, a proxy timeout) is re-read once it is back.
  const catchUp = useCallback(() => {
    refreshQueue();
    refreshSummaries();
    refreshPending();
  }, [refreshQueue, refreshSummaries, refreshPending]);

  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    void streamWithBackoff(streamDownloads, handleEvent, controller.signal, {
      onReconnect: catchUp,
    });
    return () => controller.abort();
  }, [enabled, handleEvent, catchUp]);

  return {
    jobs,
    jobsLoaded,
    summaries,
    summariesLoaded,
    summaryPhaseByVideoId,
    summaryEvent,
    liveActivity,
    progressByJobId,
    pendingCount,
    setPendingCount,
    cookieStatus,
    ytdlp,
    downloadStatus,
    activeDownloads,
    queueSignal,
    stalled,
    refreshQueue,
    refreshSummaries,
    refreshPending,
    refreshStatus,
    cancelDownload,
  };
}
