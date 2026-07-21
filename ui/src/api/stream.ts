// stream.ts — a generic Server-Sent Events consumer, ported from loom's
// api/stream.ts SSE line-parser (event:/data:/blank-line framing) but
// stripped of loom's chat-specific event union: peeq's callers get a raw
// (event, data) pair and decode the JSON payload themselves. This also
// means an unrecognized event name (e.g. a heartbeat comment) is simply
// ignored rather than erroring, per sse.Hub's own heartbeat behavior.
import { AuthExpiredError } from "./http";

export type SSEEvent = { event: string; data: unknown };

// streamSSE opens `path` via fetch + ReadableStream (not EventSource, so we
// can reuse the same-origin cookie session and get an AbortSignal) and
// invokes onEvent for every well-formed `event: ...\ndata: ...\n\n` frame.
// Resolves when the stream ends (server closes or the AbortSignal fires).
export async function streamSSE(
  path: string,
  onEvent: (event: SSEEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const response = await fetch(path, { signal });
  if (response.status === 401) {
    throw new AuthExpiredError();
  }
  if (!response.ok) {
    throw new Error(`stream ${path} failed: ${response.status}`);
  }
  if (!response.body) {
    throw new Error(`stream ${path} has no body`);
  }

  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) {
        break;
      }
      buffer += decoder.decode(value, { stream: true });
      buffer = drainBuffer(buffer, onEvent);
    }
    // Nothing to drain after the loop. drainBuffer only dispatches on a `\n\n`
    // terminator and has already consumed every one of them, and the decoder's
    // final flush can only emit replacement characters for an incomplete
    // multibyte sequence — never a newline. So whatever remains here is a frame
    // the server never terminated, which SSE says to discard rather than
    // dispatch. A trailing drainBuffer call used to sit here looking load-bearing.
  } finally {
    reader.releaseLock();
  }
}

function drainBuffer(buffer: string, onEvent: (event: SSEEvent) => void): string {
  let separator = buffer.indexOf("\n\n");
  while (separator !== -1) {
    const rawEvent = buffer.slice(0, separator);
    buffer = buffer.slice(separator + 2);
    dispatch(rawEvent, onEvent);
    separator = buffer.indexOf("\n\n");
  }
  return buffer;
}

function dispatch(rawEvent: string, onEvent: (event: SSEEvent) => void) {
  let event = "";
  const dataLines: string[] = [];
  for (const line of rawEvent.split("\n")) {
    if (line.startsWith("event:")) {
      event = line.slice("event:".length).trim();
    } else if (line.startsWith("data:")) {
      dataLines.push(line.slice("data:".length).trim());
    }
    // Any other line (e.g. a `:heartbeat` comment) is ignored.
  }
  if (event === "" || dataLines.length === 0) {
    return;
  }
  let data: unknown;
  try {
    data = JSON.parse(dataLines.join("\n"));
  } catch {
    return; // malformed payload — drop the frame rather than throw mid-stream
  }
  onEvent({ event, data });
}
