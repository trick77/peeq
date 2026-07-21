import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  listChannels,
  addChannel,
  updateChannel,
  subscribeChannel,
  unsubscribeChannel,
  deleteChannel,
} from "./channels";

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("channels api", () => {
  it("addChannel posts url + subscribe", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(
          JSON.stringify({ id: "UC1", name: "One", subscribed: true }),
          { status: 201 },
        ),
      );
    await addChannel("https://www.youtube.com/@x", true);
    const [url, init] = f.mock.calls[0];
    expect(url).toContain("/api/channels");
    expect(JSON.parse(init!.body as string)).toEqual({
      url: "https://www.youtube.com/@x",
      subscribe: true,
    });
  });

  it("listChannels passes the filter", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response("[]", { status: 200 }));
    await listChannels("subscribed");
    expect(f.mock.calls[0][0]).toContain("filter=subscribed");
  });

  it("updateChannel PUTs the patch body to /api/channels/{id}", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          id: "UC1",
          autodownload: true,
          format_override: "",
        }),
        { status: 200 },
      ),
    );
    await updateChannel("UC1", { autodownload: true });
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/channels/UC1");
    expect(init!.method).toBe("PUT");
    expect(JSON.parse(init!.body as string)).toEqual({ autodownload: true });
  });

  it("subscribeChannel POSTs to /api/channels/{id}/subscribe", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ status: "subscribed" }), { status: 200 }),
      );
    await subscribeChannel("UC1");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/channels/UC1/subscribe");
    expect(init!.method).toBe("POST");
  });

  it("unsubscribeChannel POSTs to /api/channels/{id}/unsubscribe", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ status: "unsubscribed" }), {
        status: 200,
      }),
    );
    await unsubscribeChannel("UC1");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/channels/UC1/unsubscribe");
    expect(init!.method).toBe("POST");
  });

  it("deleteChannel DELETEs /api/channels/{id}", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ status: "deleted" }), { status: 200 }),
      );
    await deleteChannel("UC1");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/channels/UC1");
    expect(init!.method).toBe("DELETE");
  });
});
