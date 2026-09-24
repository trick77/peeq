import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { streamWithBackoff } from "./reconnect";
import { AuthExpiredError } from "./http";
import type { SSEEvent } from "./stream";

type Connect = (
  onEvent: (e: SSEEvent) => void,
  signal: AbortSignal,
  onOpen: () => void,
) => Promise<void>;

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

// A connect that is accepted and then closes at once, like a proxy timing
// out the stream. `opened` false: the server never accepted it (down).
function closesImmediately(
  opened = true,
): Connect & { mock: ReturnType<typeof vi.fn> } {
  const mock = vi.fn(async (..._args: unknown[]) => {});
  const connect = ((onEvent, signal, onOpen) => {
    if (opened) onOpen();
    return mock(onEvent, signal);
  }) as Connect & { mock: typeof mock };
  connect.mock = mock;
  return connect;
}

describe("streamWithBackoff", () => {
  it("reconnects after the stream closes, waiting the base delay first", async () => {
    const connect = closesImmediately();
    const ac = new AbortController();
    void streamWithBackoff(connect, () => {}, ac.signal, { baseMs: 1000 });
    await vi.advanceTimersByTimeAsync(0);
    expect(connect.mock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(999);
    expect(connect.mock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(connect.mock).toHaveBeenCalledTimes(2);
    ac.abort();
  });

  it("doubles the delay while the server keeps refusing, up to the cap", async () => {
    const connect = closesImmediately(false);
    const ac = new AbortController();
    void streamWithBackoff(connect, () => {}, ac.signal, {
      baseMs: 1000,
      maxMs: 4000,
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(connect.mock).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1000); // 1s
    expect(connect.mock).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(2000); // 2s
    expect(connect.mock).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(4000); // 4s
    expect(connect.mock).toHaveBeenCalledTimes(4);
    await vi.advanceTimersByTimeAsync(4000); // capped at 4s
    expect(connect.mock).toHaveBeenCalledTimes(5);
    ac.abort();
  });

  it("an accepted subscription resets the delay to the base, even with no frame", async () => {
    let calls = 0;
    const connect: Connect = async (_onEvent, _signal, onOpen) => {
      calls += 1;
      // The third attempt is accepted (and then closes, idle).
      if (calls === 3) onOpen();
    };
    const ac = new AbortController();
    void streamWithBackoff(connect, () => {}, ac.signal, { baseMs: 1000 });
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(1000); // → 2
    await vi.advanceTimersByTimeAsync(2000); // → 3, which got a frame
    expect(calls).toBe(3);
    await vi.advanceTimersByTimeAsync(1000); // back to base, not 4s
    expect(calls).toBe(4);
    ac.abort();
  });

  it("forwards every frame to onEvent", async () => {
    const connect: Connect = async (onEvent, _signal, onOpen) => {
      onOpen();
      onEvent({ event: "a", data: 1 });
      onEvent({ event: "b", data: 2 });
    };
    const onEvent = vi.fn();
    const ac = new AbortController();
    void streamWithBackoff(connect, onEvent, ac.signal);
    await vi.advanceTimersByTimeAsync(0);
    expect(onEvent).toHaveBeenCalledTimes(2);
    ac.abort();
  });

  it("stops when aborted during the wait, and resolves", async () => {
    const connect = closesImmediately();
    const ac = new AbortController();
    const done = streamWithBackoff(connect, () => {}, ac.signal, {
      baseMs: 1000,
    });
    await vi.advanceTimersByTimeAsync(500);
    ac.abort();
    await expect(done).resolves.toBeUndefined();
    await vi.advanceTimersByTimeAsync(5000);
    expect(connect.mock).toHaveBeenCalledTimes(1);
  });

  it("stops without reconnecting when the session expired", async () => {
    const connect = vi.fn(async () => {
      throw new AuthExpiredError();
    });
    const ac = new AbortController();
    const done = streamWithBackoff(connect, () => {}, ac.signal, {
      baseMs: 1000,
    });
    await expect(done).resolves.toBeUndefined();
    await vi.advanceTimersByTimeAsync(5000);
    expect(connect).toHaveBeenCalledTimes(1);
  });

  it("treats any other connect failure like a close and retries", async () => {
    const connect = vi.fn(async () => {
      throw new Error("stream /api failed: 502");
    });
    const ac = new AbortController();
    void streamWithBackoff(connect, () => {}, ac.signal, { baseMs: 1000 });
    await vi.advanceTimersByTimeAsync(0);
    expect(connect).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1000);
    expect(connect).toHaveBeenCalledTimes(2);
    ac.abort();
  });

  it("calls onReconnect once each reconnect is accepted, never for the first open or a refused attempt", async () => {
    let calls = 0;
    const connect: Connect = async (_onEvent, _signal, onOpen) => {
      calls += 1;
      // Attempt 2 is refused (backend still down); 1 and 3 are accepted.
      if (calls !== 2) onOpen();
    };
    const onReconnect = vi.fn();
    const ac = new AbortController();
    void streamWithBackoff(connect, () => {}, ac.signal, {
      baseMs: 1000,
      onReconnect,
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(calls).toBe(1);
    expect(onReconnect).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1000);
    expect(calls).toBe(2);
    expect(onReconnect).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(2000);
    expect(calls).toBe(3);
    expect(onReconnect).toHaveBeenCalledTimes(1);
    ac.abort();
  });

  it("passes the abort signal through to connect", async () => {
    const connect = closesImmediately();
    const ac = new AbortController();
    void streamWithBackoff(connect, () => {}, ac.signal);
    await vi.advanceTimersByTimeAsync(0);
    expect(connect.mock.mock.calls[0][1]).toBe(ac.signal);
    ac.abort();
  });
});
