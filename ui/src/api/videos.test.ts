import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  listVideos,
  getVideo,
  deleteVideo,
  setFavorite,
  setWatched,
  setCategory,
  setResume,
  redownload,
  streamUrl,
  thumbnailUrl,
  pendingThumbnailUrl,
} from "./videos";

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("videos api", () => {
  it("listVideos sends no query string when given no options", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response("[]", { status: 200 }));
    await listVideos();
    const [url] = f.mock.calls[0];
    expect(url).toBe("/api/videos");
  });

  it("listVideos includes an explicit filter and category", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response("[]", { status: 200 }));
    await listVideos({ filter: "watched", category: "music" });
    const [url] = f.mock.calls[0];
    expect(url).toBe("/api/videos?filter=watched&category=music");
  });

  it("listVideos omits category=all even when passed explicitly", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response("[]", { status: 200 }));
    await listVideos({ filter: "all", category: "all" });
    const [url] = f.mock.calls[0];
    expect(url).toBe("/api/videos?filter=all");
  });

  it("listVideos passes the channel page's q, sort, and channel scope", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response("[]", { status: 200 }));
    await listVideos({ q: "deep field", sort: "oldest", channel: "UC1" });
    const [url] = f.mock.calls[0];
    expect(url).toBe("/api/videos?q=deep+field&sort=oldest&channel=UC1");
  });

  it("getVideo GETs /api/videos/{id}, encoding the id", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ id: "v1" }), { status: 200 }),
      );
    const v = await getVideo("v 1");
    expect(f.mock.calls[0][0]).toBe("/api/videos/v%201");
    expect(v.id).toBe("v1");
  });

  it("deleteVideo DELETEs /api/videos/{id}", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    await deleteVideo("v1");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/videos/v1");
    expect(init!.method).toBe("DELETE");
  });

  it("setFavorite POSTs the flag and returns the server's echoed value", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ favorite: true }), { status: 200 }),
      );
    const result = await setFavorite("v1", true);
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/videos/v1/favorite");
    expect(JSON.parse(init!.body as string)).toEqual({ favorite: true });
    expect(result).toBe(true);
  });

  it("setCategory POSTs the id and returns the server's echoed value", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ category: "science" }), { status: 200 }),
      );
    const result = await setCategory("v1", "science");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/videos/v1/category");
    expect(JSON.parse(init!.body as string)).toEqual({ category: "science" });
    expect(result).toBe("science");
  });

  it("setWatched POSTs the flag and returns the server's echoed value", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ watched: false, state_version: 4 }), {
        status: 200,
      }),
    );
    const result = await setWatched("v1", false);
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/videos/v1/watched");
    expect(JSON.parse(init!.body as string)).toEqual({ watched: false });
    expect(result).toEqual({ watched: false, state_version: 4 });
  });

  it("setResume POSTs the position and returns the server's echoed value", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(
          JSON.stringify({ position: 42, state_version: 3, watched: false }),
          { status: 200 },
        ),
      );
    const result = await setResume("v1", 42);
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/videos/v1/resume");
    // No state_version in the body at all when the caller passes none — the
    // backend reads that as "skip the conflict check", which is what keeps a
    // non-Player caller working.
    expect(JSON.parse(init!.body as string)).toEqual({ position: 42 });
    expect(result).toEqual({ position: 42, state_version: 3, watched: false });
  });

  it("setResume echoes a state_version when given one", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(
          JSON.stringify({ position: 42, state_version: 7, watched: false }),
          { status: 200 },
        ),
      );
    await setResume("v1", 42, 7);
    const [, init] = f.mock.calls[0];
    expect(JSON.parse(init!.body as string)).toEqual({
      position: 42,
      state_version: 7,
    });
  });

  it("setResume rejects with a 409 ApiError when the version is stale", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ error: "video state changed" }), {
        status: 409,
      }),
    );
    // The Player branches on exactly this: status 409 means a watched toggle
    // landed elsewhere and its position was refused (issue #97).
    await expect(setResume("v1", 42, 1)).rejects.toMatchObject({ status: 409 });
  });

  it("redownload POSTs to /api/videos/{id}/redownload and resolves on an empty 202 body", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response(null, { status: 202 }));
    await expect(redownload("v1")).resolves.toBeUndefined();
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/videos/v1/redownload");
    expect(init!.method).toBe("POST");
  });

  it("streamUrl builds the stream endpoint with the id encoded", () => {
    expect(streamUrl("v 1")).toBe("/api/videos/v%201/stream");
  });

  it("thumbnailUrl builds the thumbnail endpoint with the id encoded", () => {
    expect(thumbnailUrl("v 1")).toBe("/api/videos/v%201/thumbnail");
  });

  // The version is what lets the backend serve the poster as immutable, so a
  // builder that quietly dropped it would turn every cached image back into a
  // request without anything failing.
  it("thumbnailUrl appends the version when given one", () => {
    expect(thumbnailUrl("v1", "1700000000")).toBe(
      "/api/videos/v1/thumbnail?v=1700000000",
    );
  });

  it("thumbnailUrl stays bare when there is no version", () => {
    expect(thumbnailUrl("v1", undefined)).toBe("/api/videos/v1/thumbnail");
    expect(thumbnailUrl("v1", "")).toBe("/api/videos/v1/thumbnail");
  });

  it("pendingThumbnailUrl versions the inbox endpoint the same way", () => {
    expect(pendingThumbnailUrl("p 1", "42")).toBe(
      "/api/pending/p%201/thumbnail?v=42",
    );
    expect(pendingThumbnailUrl("p1")).toBe("/api/pending/p1/thumbnail");
  });
});
