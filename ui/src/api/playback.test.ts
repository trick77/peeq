import { describe, it, expect, vi, afterEach } from "vitest";
import { getPlaybackState, setPlaybackState } from "./playback";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("playback api", () => {
  it("getPlaybackState reads the pointer", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(
          JSON.stringify({ video_id: "v1", updated_at: "2026-07-25 10:00:00" }),
          { status: 200 },
        ),
      );
    const got = await getPlaybackState();
    expect(f.mock.calls[0][0]).toBe("/api/playback");
    expect(got.video_id).toBe("v1");
  });

  it("setPlaybackState PUTs the video id", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ video_id: "v1" }), { status: 200 }),
      );
    await setPlaybackState("v1");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/playback");
    expect(init!.method).toBe("PUT");
    expect(JSON.parse(init!.body as string)).toEqual({ video_id: "v1" });
  });

  it("setPlaybackState(null) clears with an empty id", async () => {
    // An empty video_id is the wire form of "nothing playing" — the backend
    // needs no second verb for it.
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ video_id: "" }), { status: 200 }),
      );
    await setPlaybackState(null);
    const [, init] = f.mock.calls[0];
    expect(JSON.parse(init!.body as string)).toEqual({ video_id: "" });
  });
});
