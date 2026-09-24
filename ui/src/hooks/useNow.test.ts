import { describe, it, expect, vi, afterEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useNow } from "./useNow";

afterEach(() => {
  vi.useRealTimers();
});

describe("useNow", () => {
  it("holds its value between ticks and moves on each one", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-07-25T10:00:00Z"));
    const { result, rerender } = renderHook(() => useNow(60_000));
    const first = result.current;
    rerender();
    expect(result.current).toBe(first);
    act(() => {
      vi.advanceTimersByTime(60_000);
    });
    expect(result.current).toBe(first + 60_000);
  });

  it("stops ticking on unmount", () => {
    vi.useFakeTimers();
    const { unmount } = renderHook(() => useNow(1000));
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
