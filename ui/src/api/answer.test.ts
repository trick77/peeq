import { describe, it, expect, vi, beforeEach } from "vitest";
import { streamAnswer, type AnswerEvent } from "./answer";
import { AuthExpiredError } from "./http";

beforeEach(() => {
  vi.restoreAllMocks();
});

function makeStreamResponse(chunks: string[], status = 200): Response {
  const encoder = new TextEncoder();
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
      controller.close();
    },
  });
  return new Response(stream, { status });
}

function frames(...f: string[]) {
  return f.map((x) => x + "\n\n");
}

async function collect(chunks: string[]): Promise<AnswerEvent[]> {
  const got: AnswerEvent[] = [];
  await streamAnswer("q", (e) => got.push(e));
  return got;
}

describe("streamAnswer", () => {
  it("narrows sources, tokens and done in order", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse(
        frames(
          `event: sources\ndata: {"sources":[{"n":1,"video_id":"v1","title":"T","start_seconds":5,"kind":"transcript"}]}`,
          `event: token\ndata: {"text":"Yes — "}`,
          `event: token\ndata: {"text":"twice."}`,
          `event: done\ndata: {"reason":"stop"}`,
        ),
      ),
    );
    const got = await collect([]);
    expect(got.map((e) => e.type)).toEqual([
      "sources",
      "token",
      "token",
      "done",
    ]);
    expect(got[0]).toMatchObject({ sources: [{ n: 1, video_id: "v1" }] });
    expect(got[1]).toMatchObject({ text: "Yes — " });
  });

  // progress arrives before sources and carries the understood query — the
  // reader's only view of what was actually searched for.
  it("narrows the progress frame and its topic", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse(
        frames(
          `event: progress\ndata: {"phase":"retrieving","topic":"bike geometry","intent":"inventory"}`,
          `event: done\ndata: {"reason":"stop"}`,
        ),
      ),
    );
    const got = await collect([]);
    expect(got.map((e) => e.type)).toEqual(["progress", "done"]);
    expect(got[0]).toMatchObject({
      type: "progress",
      phase: "retrieving",
      topic: "bike geometry",
      intent: "inventory",
    });
  });

  // A question with no framing to strip sends an empty topic, and a backend
  // built before this frame existed sends no fields at all. Both mean "the raw
  // question was searched", never an error.
  it("defaults a progress frame that carries nothing", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse(frames(`event: progress\ndata: {}`)),
    );
    const got = await collect([]);
    expect(got[0]).toMatchObject({
      type: "progress",
      topic: "",
      intent: "content",
    });
  });

  // Leading and trailing spaces have to survive, or the answer loses its word
  // boundaries. They ride inside the JSON string precisely for this reason.
  it("preserves whitespace inside a token", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse(frames(`event: token\ndata: {"text":"  spaced  "}`)),
    );
    const got = await collect([]);
    expect(got[0]).toMatchObject({ type: "token", text: "  spaced  " });
  });

  it("surfaces an error frame without rejecting", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse(
        frames(
          `event: sources\ndata: {"sources":[]}`,
          `event: error\ndata: {"error":"answer unavailable"}`,
          `event: done\ndata: {"reason":"stop"}`,
        ),
      ),
    );
    const got = await collect([]);
    expect(got.map((e) => e.type)).toEqual(["sources", "error", "done"]);
    expect(got[1]).toMatchObject({ message: "answer unavailable" });
  });

  it("ignores frames it does not recognise", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse(
        frames(
          `event: mystery\ndata: {"x":1}`,
          `event: done\ndata: {"reason":"stop"}`,
        ),
      ),
    );
    const got = await collect([]);
    expect(got.map((e) => e.type)).toEqual(["done"]);
  });

  it("defaults missing sources to an empty list", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse(frames(`event: sources\ndata: {}`)),
    );
    const got = await collect([]);
    expect(got[0]).toMatchObject({ type: "sources", sources: [] });
  });

  // coverage is what the panel's "Also in your library" list is derived from, so
  // a frame that omits it must still parse to an empty list rather than undefined
  // — the panel filters it, and filtering undefined throws.
  it("defaults missing coverage to an empty list", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse(frames(`event: sources\ndata: {}`)),
    );
    const got = await collect([]);
    expect(got[0]).toMatchObject({ type: "sources", coverage: [] });
  });

  it("carries the coverage videos through", async () => {
    const body = JSON.stringify({
      sources: [],
      videos: [],
      coverage: [{ id: "v9", title: "Uncited" }],
    });
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse(frames(`event: sources\ndata: ${body}`)),
    );
    const got = await collect([]);
    expect(got[0]).toMatchObject({
      type: "sources",
      coverage: [{ id: "v9", title: "Uncited" }],
    });
  });

  it("rejects on a non-2xx status", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse([], 503),
    );
    await expect(streamAnswer("q", () => {})).rejects.toThrow(/503/);
  });

  it("rejects with AuthExpiredError on 401", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      makeStreamResponse([], 401),
    );
    await expect(streamAnswer("q", () => {})).rejects.toBeInstanceOf(
      AuthExpiredError,
    );
  });

  it("encodes the query into the path", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(makeStreamResponse([]));
    await streamAnswer("did someone say x?", () => {});
    expect(f.mock.calls[0][0]).toBe(
      "/api/search/answer?q=did%20someone%20say%20x%3F",
    );
  });
});
