import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  getShareStatus,
  createShare,
  stopShare,
  getSharedVideo,
  shareStreamUrl,
  shareThumbnailUrl,
  shareSubtitlesUrl,
} from "./share";

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("share api", () => {
  it("getShareStatus GETs the owner share endpoint", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ shared: false }), { status: 200 }),
      );
    const res = await getShareStatus("v 1");
    expect(f.mock.calls[0][0]).toBe("/api/videos/v%201/share");
    expect(res).toEqual({ shared: false });
  });

  it("createShare POSTs the chosen ttl", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ shared: true, token: "t" }), {
        status: 200,
      }),
    );
    const res = await createShare("v1", "7d");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/videos/v1/share");
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({ ttl: "7d" });
    expect(res.token).toBe("t");
  });

  it("stopShare DELETEs the owner share endpoint", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ shared: false }), { status: 200 }),
      );
    await stopShare("v1");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/videos/v1/share");
    expect(init?.method).toBe("DELETE");
  });

  it("getSharedVideo GETs the public token endpoint", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ title: "Hi" }), { status: 200 }),
      );
    const res = await getSharedVideo("tok/1");
    expect(f.mock.calls[0][0]).toBe("/api/s/tok%2F1");
    expect(res.title).toBe("Hi");
  });

  it("getSharedVideo rejects on a dead (404) link", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response("{}", { status: 404 }),
    );
    await expect(getSharedVideo("dead")).rejects.toThrow();
  });

  it("builds the public media URLs, encoding the token", () => {
    expect(shareStreamUrl("a b")).toBe("/api/s/a%20b/stream");
    expect(shareThumbnailUrl("a b")).toBe("/api/s/a%20b/thumbnail");
    expect(shareSubtitlesUrl("a b")).toBe("/api/s/a%20b/subtitles");
  });
});
