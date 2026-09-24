import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act, waitFor } from "@testing-library/react";
import { useLiveQueue } from "./useLiveQueue";
import {
  listDownloads,
  listSummaries,
  listPending,
  cookieHealth,
  downloadsStatus,
  cancelDownload,
  streamDownloads,
} from "../api";
import { getYtdlpVersion } from "../api/ytdlp";
import type { SSEEvent } from "../api/stream";
import type { Job } from "../api/types";

vi.mock("../api", () => ({
  listDownloads: vi.fn(),
  listSummaries: vi.fn(),
  listPending: vi.fn(),
  cookieHealth: vi.fn(),
  downloadsStatus: vi.fn(),
  cancelDownload: vi.fn(),
  streamDownloads: vi.fn(),
}));
vi.mock("../api/ytdlp", () => ({ getYtdlpVersion: vi.fn() }));

const job = (id: number, state: Job["state"] = "pending"): Job =>
  ({
    job_id: id,
    video_id: `v${id}`,
    title: `Video ${id}`,
    channel_name: "",
    state,
    priority: 0,
  }) as Job;

const healthy = {
  paused: false,
  low_disk: false,
  youtube_paused: false,
  youtube_pause_reason: "",
};

// The stream handler the hook registered, so a test can push frames in.
let pushFrame: ((e: SSEEvent) => void) | null = null;

beforeEach(() => {
  vi.mocked(listDownloads).mockReset().mockResolvedValue([]);
  vi.mocked(listSummaries).mockReset().mockResolvedValue([]);
  vi.mocked(listPending).mockReset().mockResolvedValue([]);
  vi.mocked(cookieHealth).mockReset().mockResolvedValue({ status: "valid" });
  vi.mocked(downloadsStatus).mockReset().mockResolvedValue(healthy);
  vi.mocked(cancelDownload).mockReset().mockResolvedValue(undefined);
  vi.mocked(getYtdlpVersion)
    .mockReset()
    .mockResolvedValue({ installed: "1", latest: "1" } as never);
  pushFrame = null;
  vi.mocked(streamDownloads)
    .mockReset()
    .mockImplementation((onEvent, _signal, onOpen) => {
      pushFrame = onEvent;
      onOpen?.();
      return new Promise(() => {});
    });
});

describe("useLiveQueue", () => {
  it("does nothing while disabled", () => {
    renderHook(() => useLiveQueue(false));
    expect(listDownloads).not.toHaveBeenCalled();
    expect(streamDownloads).not.toHaveBeenCalled();
  });

  it("loads both lanes and the status lights once enabled", async () => {
    vi.mocked(listDownloads).mockResolvedValue([job(1), job(2, "running")]);
    const { result } = renderHook(() => useLiveQueue(true));
    await waitFor(() => expect(result.current.jobsLoaded).toBe(true));
    await waitFor(() => expect(result.current.summariesLoaded).toBe(true));
    expect(result.current.jobs).toHaveLength(2);
    expect(result.current.activeDownloads).toBe(2);
    expect(result.current.queueSignal).toBe("1,2");
    await waitFor(() => expect(result.current.cookieStatus).toBe("valid"));
    expect(streamDownloads).toHaveBeenCalledTimes(1);
  });

  it("prunes progress for jobs that have left the queue", async () => {
    vi.mocked(listDownloads).mockResolvedValue([job(7, "running")]);
    const { result } = renderHook(() => useLiveQueue(true));
    await waitFor(() => expect(result.current.jobsLoaded).toBe(true));
    await waitFor(() => expect(pushFrame).not.toBeNull());

    act(() => {
      pushFrame!({
        event: "progress",
        data: { job_id: 7, percent: 40, speed: "1MB/s", eta: "10s" },
      });
    });
    expect(result.current.progressByJobId[7]?.percent).toBe(40);

    vi.mocked(listDownloads).mockResolvedValue([]);
    act(() => result.current.refreshQueue());
    await waitFor(() => expect(result.current.jobs).toHaveLength(0));
    expect(result.current.progressByJobId[7]).toBeUndefined();
  });

  it("a progress frame for an unknown job re-lists the queue", async () => {
    const { result } = renderHook(() => useLiveQueue(true));
    await waitFor(() => expect(result.current.jobsLoaded).toBe(true));
    await waitFor(() => expect(pushFrame).not.toBeNull());
    expect(listDownloads).toHaveBeenCalledTimes(1);

    vi.mocked(listDownloads).mockResolvedValue([job(9, "running")]);
    act(() => {
      pushFrame!({
        event: "progress",
        data: { job_id: 9, percent: 1, speed: "", eta: "" },
      });
    });
    await waitFor(() => expect(result.current.jobs).toHaveLength(1));
    expect(listDownloads).toHaveBeenCalledTimes(2);
  });

  it("a summary frame records the phase, forwards the event and re-lists summaries", async () => {
    const { result } = renderHook(() => useLiveQueue(true));
    await waitFor(() => expect(pushFrame).not.toBeNull());
    expect(listSummaries).toHaveBeenCalledTimes(1);

    act(() => {
      pushFrame!({
        event: "summary",
        data: { video_id: "v1", status: "running", phase: "embedding" },
      });
    });
    expect(result.current.summaryPhaseByVideoId.v1).toBe("embedding");
    expect(result.current.summaryEvent).toEqual({
      videoId: "v1",
      status: "running",
    });
    await waitFor(() => expect(listSummaries).toHaveBeenCalledTimes(2));
  });

  it("an activity frame is buffered and refreshes the pending count", async () => {
    vi.mocked(listPending).mockResolvedValue([{ video_id: "p1" } as never]);
    const { result } = renderHook(() => useLiveQueue(true));
    await waitFor(() => expect(pushFrame).not.toBeNull());

    act(() => {
      pushFrame!({ event: "activity", data: { id: 1, kind: "scan" } });
    });
    expect(result.current.liveActivity).toHaveLength(1);
    await waitFor(() => expect(result.current.pendingCount).toBe(1));
  });

  it("refreshStatus re-reads the cookie light and the download status", async () => {
    const { result } = renderHook(() => useLiveQueue(true));
    await waitFor(() => expect(result.current.cookieStatus).toBe("valid"));
    vi.mocked(cookieHealth).mockResolvedValue({ status: "stale" });
    vi.mocked(downloadsStatus).mockResolvedValue({
      ...healthy,
      youtube_paused: true,
    });
    act(() => result.current.refreshStatus());
    await waitFor(() => expect(result.current.cookieStatus).toBe("stale"));
    await waitFor(() => expect(result.current.stalled).toBe("youtube"));
  });

  it("cancelDownload rejects when the API does, and still re-lists", async () => {
    const { result } = renderHook(() => useLiveQueue(true));
    await waitFor(() => expect(result.current.jobsLoaded).toBe(true));
    vi.mocked(cancelDownload).mockRejectedValue(new Error("nope"));
    const calls = vi.mocked(listDownloads).mock.calls.length;
    await expect(result.current.cancelDownload(3)).rejects.toThrow("nope");
    await waitFor(() =>
      expect(vi.mocked(listDownloads).mock.calls.length).toBe(calls + 1),
    );
  });

  it("aborts the stream when disabled again", async () => {
    let seen: AbortSignal | undefined;
    vi.mocked(streamDownloads).mockImplementation((_onEvent, signal) => {
      seen = signal;
      return new Promise(() => {});
    });
    const { rerender } = renderHook(({ on }) => useLiveQueue(on), {
      initialProps: { on: true },
    });
    await waitFor(() => expect(seen).toBeDefined());
    rerender({ on: false });
    expect(seen!.aborted).toBe(true);
  });

  it("re-reads both lanes and the inbox count once a dropped stream is back", async () => {
    vi.useFakeTimers();
    try {
      let connects = 0;
      vi.mocked(streamDownloads).mockImplementation(
        (_onEvent, _signal, onOpen) => {
          connects += 1;
          onOpen?.();
          // First connection drops at once; the second stays open.
          return connects === 1
            ? Promise.resolve()
            : new Promise<void>(() => {});
        },
      );
      renderHook(() => useLiveQueue(true));
      await vi.advanceTimersByTimeAsync(0);
      expect(connects).toBe(1);
      expect(listDownloads).toHaveBeenCalledTimes(1);
      expect(listPending).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(1000);
      expect(connects).toBe(2);
      expect(listDownloads).toHaveBeenCalledTimes(2);
      expect(listSummaries).toHaveBeenCalledTimes(2);
      expect(listPending).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });
});
