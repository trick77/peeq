import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook, act, waitFor } from "@testing-library/react";
import { useAuthBootstrap } from "./useAuthBootstrap";
import { getMe } from "../api";
import { notifyAuthExpired } from "../api/http";
import { writeSignedInHint } from "../signedInHint";

vi.mock("../api", () => ({ getMe: vi.fn() }));
vi.mock("../signedInHint", () => ({
  readSignedInHint: vi.fn(() => false),
  writeSignedInHint: vi.fn(),
}));

const user = { id: "u1", email: "a@b.c" };

describe("useAuthBootstrap", () => {
  beforeEach(() => {
    vi.mocked(getMe).mockReset();
    vi.mocked(writeSignedInHint).mockClear();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("resolves the user and records the hint", async () => {
    vi.mocked(getMe).mockResolvedValue(user);
    const { result } = renderHook(() => useAuthBootstrap());
    expect(result.current.authChecked).toBe(false);
    await waitFor(() => expect(result.current.authChecked).toBe(true));
    expect(result.current.user).toEqual(user);
    expect(result.current.authError).toBe(false);
    expect(result.current.expired).toBe(false);
    expect(writeSignedInHint).toHaveBeenCalledWith(true);
  });

  it("a rejected check reports unreachable and leaves the hint alone", async () => {
    vi.mocked(getMe).mockRejectedValue(new Error("down"));
    const { result } = renderHook(() => useAuthBootstrap());
    await waitFor(() => expect(result.current.authChecked).toBe(true));
    expect(result.current.user).toBeNull();
    expect(result.current.authError).toBe(true);
    expect(writeSignedInHint).not.toHaveBeenCalled();
  });

  it("an expired session mid-use clears the user and the hint, without re-checking", async () => {
    vi.mocked(getMe).mockResolvedValue(user);
    const { result } = renderHook(() => useAuthBootstrap());
    await waitFor(() => expect(result.current.user).toEqual(user));

    act(() => notifyAuthExpired());

    expect(result.current.user).toBeNull();
    expect(result.current.expired).toBe(true);
    expect(result.current.authChecked).toBe(true);
    expect(result.current.authError).toBe(false);
    expect(writeSignedInHint).toHaveBeenLastCalledWith(false);
    expect(getMe).toHaveBeenCalledTimes(1);
  });

  it("stops listening after unmount", async () => {
    vi.mocked(getMe).mockResolvedValue(user);
    const { result, unmount } = renderHook(() => useAuthBootstrap());
    await waitFor(() => expect(result.current.user).toEqual(user));
    unmount();
    notifyAuthExpired();
    // The mount wrote the hint once (signed in); an unmounted hook must not
    // write it again.
    expect(writeSignedInHint).toHaveBeenCalledTimes(1);
  });

  it("flags a slow check after the grace period", async () => {
    vi.useFakeTimers();
    vi.mocked(getMe).mockReturnValue(new Promise(() => {}));
    const { result } = renderHook(() => useAuthBootstrap());
    expect(result.current.checkSlow).toBe(false);
    act(() => {
      vi.advanceTimersByTime(700);
    });
    expect(result.current.checkSlow).toBe(true);
  });
});
