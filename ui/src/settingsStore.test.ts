import { describe, it, expect, vi, beforeEach } from "vitest";
import { renderHook, act, waitFor } from "@testing-library/react";
import {
  loadSettings,
  refreshSettings,
  publishSettings,
  patchSettings,
  invalidateSettings,
  useSettings,
  resetSettingsStoreForTests,
} from "./settingsStore";
import { getSettings } from "./api";
import type { Settings } from "./api/types";

vi.mock("./api", () => ({ getSettings: vi.fn() }));

const base = {
  retention_days: 14,
  subtitles_default: false,
  direct_stream_enabled: false,
  format_preset: "best",
} as Settings;

beforeEach(() => {
  resetSettingsStoreForTests();
  vi.mocked(getSettings).mockReset();
  vi.mocked(getSettings).mockResolvedValue(base);
});

describe("settingsStore", () => {
  it("fetches once for however many callers ask while the request is out", async () => {
    const [a, b] = await Promise.all([loadSettings(), loadSettings()]);
    expect(a).toBe(b);
    expect(getSettings).toHaveBeenCalledTimes(1);
    await loadSettings();
    expect(getSettings).toHaveBeenCalledTimes(1);
  });

  it("a failed load is reported, and is not sticky", async () => {
    vi.mocked(getSettings).mockRejectedValueOnce(new Error("down"));
    await expect(loadSettings()).rejects.toThrow("down");
    const { result } = renderHook(() => useSettings());
    await waitFor(() => expect(result.current.settings).toEqual(base));
    expect(result.current.failed).toBe(false);
    expect(getSettings).toHaveBeenCalledTimes(2);
  });

  it("useSettings loads on mount and follows a published change", async () => {
    const { result } = renderHook(() => useSettings());
    expect(result.current.settings).toBeNull();
    await waitFor(() => expect(result.current.settings).toEqual(base));
    act(() => publishSettings({ ...base, retention_days: 30 }));
    expect(result.current.settings?.retention_days).toBe(30);
    expect(getSettings).toHaveBeenCalledTimes(1);
  });

  it("useSettings reports a failed load so readers can fall back", async () => {
    vi.mocked(getSettings).mockRejectedValue(new Error("down"));
    const { result } = renderHook(() => useSettings());
    await waitFor(() => expect(result.current.failed).toBe(true));
    expect(result.current.settings).toBeNull();
  });

  it("patchSettings applies an optimistic change on top of the cached value", async () => {
    await loadSettings();
    const { result } = renderHook(() => useSettings());
    act(() => patchSettings({ subtitles_default: true }));
    expect(result.current.settings?.subtitles_default).toBe(true);
    expect(result.current.settings?.retention_days).toBe(14);
  });

  it("patchSettings before anything is cached is a no-op", () => {
    patchSettings({ subtitles_default: true });
    const { result } = renderHook(() => useSettings());
    expect(result.current.settings).toBeNull();
  });

  it("refreshSettings always asks the server and keeps the old value if that fails", async () => {
    await loadSettings();
    vi.mocked(getSettings).mockResolvedValueOnce({
      ...base,
      retention_days: 7,
    });
    await expect(refreshSettings()).resolves.toMatchObject({
      retention_days: 7,
    });
    expect(getSettings).toHaveBeenCalledTimes(2);

    vi.mocked(getSettings).mockRejectedValueOnce(new Error("down"));
    await expect(refreshSettings()).rejects.toThrow("down");
    const { result } = renderHook(() => useSettings());
    expect(result.current.settings?.retention_days).toBe(7);
    expect(result.current.failed).toBe(false);
  });

  it("invalidateSettings forgets the value, and the next reader fetches again", async () => {
    await loadSettings();
    const { result } = renderHook(() => useSettings());
    expect(result.current.settings).toEqual(base);
    vi.mocked(getSettings).mockResolvedValue({ ...base, retention_days: 3 });
    act(() => invalidateSettings());
    expect(result.current.settings).toBeNull();
    await expect(loadSettings()).resolves.toMatchObject({ retention_days: 3 });
    await waitFor(() =>
      expect(result.current.settings?.retention_days).toBe(3),
    );
  });

  it("an answer to a request made before invalidate is not adopted", async () => {
    let resolveOld: (s: Settings) => void = () => {};
    vi.mocked(getSettings).mockReturnValueOnce(
      new Promise<Settings>((r) => {
        resolveOld = r;
      }),
    );
    const old = loadSettings();
    invalidateSettings();
    resolveOld({ ...base, retention_days: 99 });
    await old;
    const { result } = renderHook(() => useSettings());
    await waitFor(() => expect(result.current.settings).toEqual(base));
    expect(result.current.settings?.retention_days).toBe(14);
  });

  it("a fetch that lands after a publish does not overwrite the newer value", async () => {
    let resolveOld: (s: Settings) => void = () => {};
    vi.mocked(getSettings).mockReturnValueOnce(
      new Promise<Settings>((r) => {
        resolveOld = r;
      }),
    );
    const { result } = renderHook(() => useSettings());
    act(() => publishSettings({ ...base, subtitles_default: true }));
    resolveOld({ ...base, subtitles_default: false });
    await Promise.resolve();
    expect(result.current.settings?.subtitles_default).toBe(true);
  });

  it("a fetch that fails after a publish keeps the published value", async () => {
    let rejectOld: (e: Error) => void = () => {};
    vi.mocked(getSettings).mockReturnValueOnce(
      new Promise<Settings>((_r, rej) => {
        rejectOld = rej;
      }),
    );
    const pending = loadSettings().catch(() => {});
    const { result } = renderHook(() => useSettings());
    act(() => publishSettings({ ...base, retention_days: 30 }));
    rejectOld(new Error("timeout"));
    await pending;
    expect(result.current.settings?.retention_days).toBe(30);
    expect(result.current.failed).toBe(false);
  });

  it("a mounted reader reloads after an invalidate", async () => {
    const { result } = renderHook(() => useSettings());
    await waitFor(() => expect(result.current.settings).toEqual(base));
    vi.mocked(getSettings).mockResolvedValue({ ...base, retention_days: 5 });
    act(() => invalidateSettings());
    await waitFor(() =>
      expect(result.current.settings?.retention_days).toBe(5),
    );
    expect(getSettings).toHaveBeenCalledTimes(2);
  });
});
