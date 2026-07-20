import { describe, it, expect, vi, beforeEach } from "vitest";
import { streamSSE } from "./stream";
import { AuthExpiredError } from "./http";

beforeEach(() => {
  vi.restoreAllMocks();
});

// makeStreamResponse builds a Response whose body is a ReadableStream that
// enqueues each of `chunks` as a separate encoded write — letting tests
// control exactly how frames are split across `reader.read()` calls.
function makeStreamResponse(chunks: string[], status = 200): Response {
  const encoder = new TextEncoder();
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) {
        controller.enqueue(encoder.encode(chunk));
      }
      controller.close();
    },
  });
  return new Response(stream, { status });
}

describe("streamSSE", () => {
  it("parses a well-formed event:/data: frame and invokes onEvent with decoded JSON", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse(['event: progress\ndata: {"job_id":1,"percent":50}\n\n']),
    );
    const onEvent = vi.fn();
    await streamSSE("/api/downloads/stream", onEvent);
    expect(onEvent).toHaveBeenCalledTimes(1);
    expect(onEvent).toHaveBeenCalledWith({ event: "progress", data: { job_id: 1, percent: 50 } });
  });

  it("reassembles a frame split across chunk boundaries", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse(["event: progr", 'ess\ndata: {"job_id":2}', "\n\n"]),
    );
    const onEvent = vi.fn();
    await streamSSE("/api/downloads/stream", onEvent);
    expect(onEvent).toHaveBeenCalledTimes(1);
    expect(onEvent).toHaveBeenCalledWith({ event: "progress", data: { job_id: 2 } });
  });

  it("ignores a heartbeat frame with no event:/data: lines, but still processes the next real frame", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse([": heartbeat\n\n", 'event: progress\ndata: {"job_id":3}\n\n']),
    );
    const onEvent = vi.fn();
    await streamSSE("/api/downloads/stream", onEvent);
    expect(onEvent).toHaveBeenCalledTimes(1);
    expect(onEvent).toHaveBeenCalledWith({ event: "progress", data: { job_id: 3 } });
  });

  it("drops a frame whose data: payload is not valid JSON", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse(["event: progress\ndata: {not json\n\n", 'event: progress\ndata: {"job_id":4}\n\n']),
    );
    const onEvent = vi.fn();
    await streamSSE("/api/downloads/stream", onEvent);
    expect(onEvent).toHaveBeenCalledTimes(1);
    expect(onEvent).toHaveBeenCalledWith({ event: "progress", data: { job_id: 4 } });
  });

  it("throws AuthExpiredError on a 401", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 401 }));
    await expect(streamSSE("/api/downloads/stream", vi.fn())).rejects.toBeInstanceOf(AuthExpiredError);
  });

  it("throws a plain Error on a non-ok status", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 500 }));
    await expect(streamSSE("/api/downloads/stream", vi.fn())).rejects.toThrow(/500/);
  });

  it("resolves once the stream ends, after dispatching all frames", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse(['event: progress\ndata: {"job_id":1}\n\n', 'event: progress\ndata: {"job_id":2}\n\n']),
    );
    const onEvent = vi.fn();
    await expect(streamSSE("/api/downloads/stream", onEvent)).resolves.toBeUndefined();
    expect(onEvent).toHaveBeenCalledTimes(2);
  });

  it("passes the given AbortSignal through to fetch", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(makeStreamResponse(["event: x\ndata: 1\n\n"]));
    const controller = new AbortController();
    await streamSSE("/api/downloads/stream", vi.fn(), controller.signal);
    expect(f.mock.calls[0][1]).toEqual({ signal: controller.signal });
  });

  it("reassembles a multibyte UTF-8 character split across chunk boundaries", async () => {
    // "日" encodes to the 3 bytes E6 97 A5 in UTF-8. Split the encoded frame
    // so that the cut lands inside that byte sequence (after its first byte),
    // which only decodes correctly if the decoder carries state across
    // `decoder.decode(value, { stream: true })` calls.
    const frame = 'event: progress\ndata: {"name":"日"}\n\n';
    const encoder = new TextEncoder();
    const full = encoder.encode(frame);
    const prefixLen = encoder.encode(frame.slice(0, frame.indexOf("日"))).length;
    const splitAt = prefixLen + 1; // inside the 3-byte sequence for 日
    const chunk1 = full.slice(0, splitAt);
    const chunk2 = full.slice(splitAt);

    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(chunk1);
        controller.enqueue(chunk2);
        controller.close();
      },
    });
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(stream, { status: 200 }));

    const onEvent = vi.fn();
    await streamSSE("/api/downloads/stream", onEvent);
    expect(onEvent).toHaveBeenCalledTimes(1);
    expect(onEvent).toHaveBeenCalledWith({ event: "progress", data: { name: "日" } });
  });

  it("drops a frame that has a data: line but no event: line", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(makeStreamResponse(['data: {"job_id":5}\n\n']));
    const onEvent = vi.fn();
    await streamSSE("/api/downloads/stream", onEvent);
    expect(onEvent).not.toHaveBeenCalled();
  });

  it("throws when the response has no body", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 200 }));
    await expect(streamSSE("/api/downloads/stream", vi.fn())).rejects.toThrow(/has no body/);
  });
});
