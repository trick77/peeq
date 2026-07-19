import { describe, it, expect, vi, beforeEach } from "vitest";
import { listChannels, addChannel } from "./channels";

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("channels api", () => {
  it("addChannel posts url + subscribe", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ id: "UC1", name: "One", subscribed: true }), { status: 201 }),
    );
    await addChannel("https://www.youtube.com/@x", true);
    const [url, init] = f.mock.calls[0];
    expect(url).toContain("/api/channels");
    expect(JSON.parse(init!.body as string)).toEqual({ url: "https://www.youtube.com/@x", subscribe: true });
  });

  it("listChannels passes the filter", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response("[]", { status: 200 }));
    await listChannels("subscribed");
    expect(f.mock.calls[0][0]).toContain("filter=subscribed");
  });
});
