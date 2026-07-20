import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  addDownload,
  listDownloads,
  downloadsStatus,
  cancelDownload,
  pauseYoutube,
  resumeYoutube,
  streamDownloads,
  CookieRequiredError,
  InvalidUrlError,
} from "./downloads";
import { ApiError } from "./http";

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("downloads api", () => {
  it("addDownload posts the url and fills in attempts: 0", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({ job_id: 1, video_id: "v1", state: "queued", priority: 0 }),
        { status: 201 },
      ),
    );
    const job = await addDownload("https://youtu.be/v1");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/downloads");
    expect(JSON.parse(init!.body as string)).toEqual({ url: "https://youtu.be/v1" });
    expect(job).toEqual({ job_id: 1, video_id: "v1", state: "queued", priority: 0, attempts: 0 });
  });

  it("addDownload throws CookieRequiredError on a 409", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({}), { status: 409 }));
    await expect(addDownload("https://youtu.be/v1")).rejects.toBeInstanceOf(CookieRequiredError);
  });

  it("addDownload throws InvalidUrlError carrying the server's message on a 400", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      new Response(JSON.stringify({ error: "playlists are not supported" }), { status: 400 }),
    );
    try {
      await addDownload("https://youtu.be/playlist");
      expect.unreachable();
    } catch (err) {
      expect(err).toBeInstanceOf(InvalidUrlError);
      expect((err as Error).message).toBe("playlists are not supported");
    }
  });

  it("addDownload rethrows other ApiErrors unchanged", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({}), { status: 500 }));
    await expect(addDownload("https://youtu.be/v1")).rejects.toBeInstanceOf(ApiError);
  });

  it("listDownloads GETs /api/downloads", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("[]", { status: 200 }));
    await listDownloads();
    expect(f.mock.calls[0][0]).toBe("/api/downloads");
  });

  it("downloadsStatus GETs /api/downloads/status", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({ paused: true, low_disk: false, youtube_paused: false, youtube_pause_reason: "" }),
        { status: 200 },
      ),
    );
    const status = await downloadsStatus();
    expect(f.mock.calls[0][0]).toBe("/api/downloads/status");
    expect(status.paused).toBe(true);
  });

  it("cancelDownload POSTs to /api/downloads/{id}/cancel", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    await cancelDownload(7);
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/downloads/7/cancel");
    expect(init!.method).toBe("POST");
  });

  it("pauseYoutube POSTs to /api/youtube/pause and resolves on an empty 202 body", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 202 }));
    await expect(pauseYoutube()).resolves.toBeUndefined();
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/youtube/pause");
    expect(init!.method).toBe("POST");
  });

  it("resumeYoutube POSTs to /api/youtube/resume", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(null, { status: 202 }));
    await resumeYoutube();
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/youtube/resume");
    expect(init!.method).toBe("POST");
  });

  it("streamDownloads opens the SSE feed at /api/downloads/stream and forwards parsed events", async () => {
    const encoder = new TextEncoder();
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoder.encode('event: progress\ndata: {"job_id":1,"percent":10,"speed":"1MB/s","eta":"1s"}\n\n'));
        controller.close();
      },
    });
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(stream, { status: 200 }));
    const onEvent = vi.fn();
    await streamDownloads(onEvent);
    expect(f.mock.calls[0][0]).toBe("/api/downloads/stream");
    expect(onEvent).toHaveBeenCalledWith({
      event: "progress",
      data: { job_id: 1, percent: 10, speed: "1MB/s", eta: "1s" },
    });
  });
});
