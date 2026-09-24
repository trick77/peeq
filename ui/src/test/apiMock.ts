import { vi } from "vitest";

// apiMock is the ./api barrel as App-level tests see it: everything App loads
// on mount, plus what the views one rail click away resolve at module scope.
// One copy, used by every test file that renders <App />, so a new barrel
// export that a view resolves at import time is added once and not missed by
// the other file.
export function apiMock() {
  return {
    getMe: vi.fn().mockResolvedValue({ id: "u1", email: "a@b.c" }),
    listDownloads: vi.fn().mockResolvedValue([]),
    cookieHealth: vi.fn().mockResolvedValue({ status: "valid" }),
    downloadsStatus: vi
      .fn()
      .mockResolvedValue({ paused: false, low_disk: false }),
    resumeYoutube: vi.fn().mockResolvedValue(undefined),
    listPending: vi.fn().mockResolvedValue([]),
    listSummaries: vi.fn().mockResolvedValue([]),
    cancelDownload: vi.fn().mockResolvedValue(undefined),
    // Never resolves: a closed stream now reconnects and re-lists, which would
    // change the call counts below. A stream that stays open is the normal case.
    streamDownloads: vi.fn().mockReturnValue(new Promise<void>(() => {})),
    listVideos: vi.fn().mockResolvedValue([]),
    getVideoCounts: vi.fn().mockResolvedValue({ filters: {}, categories: {} }),
    // Up next fetches the timed schedule; History fetches the log. Both are one
    // rail click away, so the barrel needs them even in tests that never open
    // those pages.
    listUpcoming: vi.fn().mockResolvedValue({ items: [], truncated: 0 }),
    // Up next fetches the gave-up list itself, for the same reason: it is not
    // in-flight work, so App does not poll it.
    listFailedSummaries: vi.fn().mockResolvedValue([]),
    retryFailedSummaries: vi.fn().mockResolvedValue({ requeued: 0 }),
    // Up next's skip action on a scheduled row. App never calls these itself, but
    // UpNext resolves them at module scope, so the mock has to carry them.
    skipScheduledScan: vi.fn(),
    skipScheduledMeta: vi.fn(),
    listActivity: vi
      .fn()
      .mockResolvedValue({ events: [], has_more: false, retained_max: 2000 }),
    getSettings: vi.fn().mockResolvedValue({}),
    setFavorite: vi.fn(),
    setWatched: vi.fn(),
    getPlaybackState: vi.fn().mockResolvedValue({ video_id: "" }),
  };
}
