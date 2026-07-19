import { describe, it, expect, vi, beforeEach } from "vitest";
import { searchVideos, resummarize, subtitlesUrl } from "./search";

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("search api", () => {
  it("returns [] for blank query without fetching", async () => {
    const f = vi.spyOn(globalThis, "fetch");
    expect(await searchVideos("   ")).toEqual([]);
    expect(f).not.toHaveBeenCalled();
  });

  it("fetches and parses results for a non-blank query", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ results: [{ video: { id: "v1" }, matches: [] }] }), { status: 200 }),
    );
    const r = await searchVideos("iphone");
    const [url] = f.mock.calls[0];
    expect(url).toBe("/api/search?q=iphone");
    expect(r[0].video.id).toBe("v1");
  });

  it("returns [] when the backend omits results", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    expect(await searchVideos("nothing")).toEqual([]);
  });

  it("resummarize POSTs to /api/videos/{id}/resummarize", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ status: "queued" }), { status: 202 }),
    );
    await resummarize("v1");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/videos/v1/resummarize");
    expect(init!.method).toBe("POST");
  });

  it("subtitlesUrl builds the subtitles endpoint", () => {
    expect(subtitlesUrl("v1")).toBe("/api/videos/v1/subtitles");
  });
});
