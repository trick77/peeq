// reconnect.ts — keeps one SSE subscription alive for the whole session.
//
// streamSSE resolves when the server closes the stream and rejects when the
// connection fails; neither is the end of the session. A backend restart, a
// proxy idle timeout or a dropped Wi-Fi link all end the stream, and before
// this the shell simply stopped hearing progress, summary and activity events
// until the page was reloaded. So the subscription is a loop: connect, drain,
// wait, connect again — with a delay that doubles on consecutive failures
// (never hammering a backend that is down) and resets the moment a frame
// arrives (a healthy stream that drops once reconnects at once).
//
// Two things end the loop: the caller's AbortSignal (the shell unmounting or
// signing out) and an expired session, which the 401 mapping has already
// reported to the shell — reconnecting would only 401 again.
import { AuthExpiredError } from "./http";
import type { SSEEvent } from "./stream";

export type StreamConnect = (
  onEvent: (event: SSEEvent) => void,
  signal: AbortSignal,
  onOpen: () => void,
) => Promise<void>;

export type ReconnectOptions = {
  // First wait after a close; each consecutive close doubles it.
  baseMs?: number;
  // The wait never exceeds this.
  maxMs?: number;
  // Called once a reconnect (any open after the first) has been accepted by
  // the server. The stream carries deltas, not state, so whatever happened
  // while it was down has to be re-read — and only from this point can
  // nothing more be missed, which is why it fires on open rather than before
  // the attempt (a re-read before the subscription exists leaves a gap, and a
  // re-read before a failed attempt is wasted on a backend that is down).
  onReconnect?: () => void;
};

const DEFAULT_BASE_MS = 1000;
const DEFAULT_MAX_MS = 30_000;

export async function streamWithBackoff(
  connect: StreamConnect,
  onEvent: (event: SSEEvent) => void,
  signal: AbortSignal,
  opts: ReconnectOptions = {},
): Promise<void> {
  const base = opts.baseMs ?? DEFAULT_BASE_MS;
  const max = opts.maxMs ?? DEFAULT_MAX_MS;
  let delay = base;
  let opens = 0;
  for (;;) {
    if (signal.aborted) return;
    try {
      await connect(onEvent, signal, () => {
        // An accepted subscription is the health signal, not a data frame:
        // an idle stream sends only keepalive comments, which the reader
        // drops, so resetting on frames alone would leave the delay parked
        // at the cap after any outage.
        delay = base;
        opens += 1;
        if (opens > 1) opts.onReconnect?.();
      });
    } catch (err) {
      if (err instanceof AuthExpiredError) return;
      // Anything else — a failed fetch, a non-2xx, an abort surfacing as an
      // error — is handled the same way as a clean close: by the wait below,
      // or by the abort check at the top of the loop.
    }
    if (signal.aborted) return;
    await sleep(delay, signal);
    delay = Math.min(delay * 2, max);
  }
}

// sleep resolves after ms, or at once when the signal fires — an aborted loop
// must not sit out its last wait before noticing.
function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const onAbort = () => {
      clearTimeout(timer);
      resolve();
    };
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal.addEventListener("abort", onAbort, { once: true });
  });
}
