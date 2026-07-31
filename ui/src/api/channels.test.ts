import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  listChannels,
  addChannel,
  updateChannel,
  subscribeChannel,
  unsubscribeChannel,
  deleteChannel,
  getChannel,
  scanChannel,
  refreshChannel,
  channelAvatarUrl,
  channelBannerUrl,
  skipScheduledScan,
  skipScheduledMeta,
} from "./channels";
import { CookieRequiredError } from "./downloads";

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

  it("getChannel GETs /api/channels/{id} and returns the detail body", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ id: "UC1", name: "One", added: true }), {
        status: 200,
      }),
    );
    const detail = await getChannel("UC1");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/channels/UC1");
    expect(init?.method ?? "GET").toBe("GET");
    expect(detail).toEqual({ id: "UC1", name: "One", added: true });
  });

  it("scanChannel POSTs to /api/channels/{id}/scan and returns the result", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ status: "scheduled" }), { status: 200 }),
      );
    const res = await scanChannel("UC1");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/channels/UC1/scan");
    expect(init!.method).toBe("POST");
    expect(res).toEqual({ status: "scheduled" });
  });

  it("refreshChannel POSTs to /api/channels/{id}/refresh", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ status: "ok" }), { status: 200 }),
      );
    const res = await refreshChannel("UC1");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/channels/UC1/refresh");
    expect(init!.method).toBe("POST");
    expect(res).toEqual({ status: "ok" });
  });

  it("refreshChannel maps a 409 to CookieRequiredError", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ error: "cookie required" }), {
        status: 409,
      }),
    );
    await expect(refreshChannel("UC1")).rejects.toBeInstanceOf(
      CookieRequiredError,
    );
  });

  it("channelAvatarUrl and channelBannerUrl build encoded per-channel URLs", () => {
    expect(channelAvatarUrl("UC 1")).toBe("/api/channels/UC%201/avatar");
    expect(channelBannerUrl("UC 1")).toBe("/api/channels/UC%201/banner");
  });

  // Versioned artwork is what makes a weekly metadata refresh visible at once:
  // new bytes move the stamp, which moves the URL, which sidesteps the cache.
  it("channelAvatarUrl and channelBannerUrl append the version", () => {
    expect(channelAvatarUrl("UC1", "42")).toBe("/api/channels/UC1/avatar?v=42");
    expect(channelBannerUrl("UC1", "42")).toBe("/api/channels/UC1/banner?v=42");
  });

  // A bare skip sends no body at all. An `at` of undefined serialised as
  // `{"at": ""}` would read to the handler as an explicit instant rather than
  // "you pick", which is the difference between skipping and pinning.
  it("skipScheduledScan posts no body when no instant is given", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          status: "skipped",
          at: "2026-08-01 09:00:00",
          previous_at: "2026-07-26 09:00:00",
        }),
        { status: 200 },
      ),
    );
    const res = await skipScheduledScan("UC1");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/channels/UC1/skip-scan");
    expect(init!.method).toBe("POST");
    expect(init!.body).toBeUndefined();
    expect(res.previous_at).toBe("2026-07-26 09:00:00");
  });

  // Undo is the same endpoint handed the instant the skip reported back.
  it("skipScheduledScan sends the instant back when undoing", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(
          JSON.stringify({ status: "restored", at: "", previous_at: "" }),
          { status: 200 },
        ),
      );
    await skipScheduledScan("UC1", "2026-07-26 09:00:00");
    const [, init] = f.mock.calls[0];
    expect(JSON.parse(init!.body as string)).toEqual({
      at: "2026-07-26 09:00:00",
    });
  });

  it("skipScheduledMeta targets the refresh schedule, not the scan", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(
          JSON.stringify({ status: "skipped", at: "", previous_at: "" }),
          { status: 200 },
        ),
      );
    await skipScheduledMeta("UC 1");
    const [url] = f.mock.calls[0];
    expect(url).toBe("/api/channels/UC%201/skip-meta");
  });
});
