import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useTransient } from "./useTransient";

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

describe("useTransient", () => {
  it("shows a value and drops it after the duration", () => {
    const { result } = renderHook(() => useTransient<string>(1000));
    expect(result.current.value).toBeNull();
    act(() => result.current.show("hi"));
    expect(result.current.value).toBe("hi");
    act(() => {
      vi.advanceTimersByTime(999);
    });
    expect(result.current.value).toBe("hi");
    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(result.current.value).toBeNull();
  });

  it("a second show restarts the clock instead of stacking", () => {
    const { result } = renderHook(() => useTransient<string>(1000));
    act(() => result.current.show("first"));
    act(() => {
      vi.advanceTimersByTime(800);
    });
    act(() => result.current.show("second"));
    act(() => {
      vi.advanceTimersByTime(800);
    });
    expect(result.current.value).toBe("second");
    act(() => {
      vi.advanceTimersByTime(200);
    });
    expect(result.current.value).toBeNull();
  });

  it("flags leaving for the fade window before the value goes", () => {
    const { result } = renderHook(() =>
      useTransient<string>(4300, { fadeMs: 300 }),
    );
    act(() => result.current.show("queued"));
    expect(result.current.leaving).toBe(false);
    act(() => {
      vi.advanceTimersByTime(4000);
    });
    expect(result.current.leaving).toBe(true);
    expect(result.current.value).toBe("queued");
    act(() => {
      vi.advanceTimersByTime(300);
    });
    expect(result.current.value).toBeNull();
    expect(result.current.leaving).toBe(false);
  });

  it("clear drops the value and its timers", () => {
    const { result } = renderHook(() => useTransient<string>(1000));
    act(() => result.current.show("hi"));
    act(() => result.current.clear());
    expect(result.current.value).toBeNull();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("leaves no timer behind on unmount", () => {
    const { result, unmount } = renderHook(() => useTransient<string>(1000));
    act(() => result.current.show("hi"));
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("keeps show and clear stable across renders", () => {
    const { result, rerender } = renderHook(() => useTransient<string>(1000));
    const { show, clear } = result.current;
    rerender();
    expect(result.current.show).toBe(show);
    expect(result.current.clear).toBe(clear);
  });
});
