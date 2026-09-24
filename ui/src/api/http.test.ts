import { describe, it, expect, vi, afterEach } from "vitest";
import {
  api,
  AuthExpiredError,
  ApiError,
  onAuthExpired,
  notifyAuthExpired,
} from "./http";

describe("api http client", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("api.get throws AuthExpiredError on 401", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "unauthorized" }), {
        status: 401,
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.get("/api/videos")).rejects.toBeInstanceOf(
      AuthExpiredError,
    );
  });

  it("api.get throws ApiError with the server message on other non-2xx", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "video not found" }), {
        status: 404,
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.get("/api/videos/x")).rejects.toMatchObject({
      status: 404,
      message: "video not found",
    });
  });

  it("api.get resolves the decoded JSON body on 200", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(
        new Response(JSON.stringify({ id: "abc" }), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.get("/api/videos/abc")).resolves.toEqual({ id: "abc" });
  });

  it("api.post sends a JSON body and the given method", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(
        new Response(JSON.stringify({ ok: true }), { status: 200 }),
      );
    vi.stubGlobal("fetch", fetchMock);

    await api.post("/api/downloads", { url: "https://example.com" });

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/downloads",
      expect.objectContaining({
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ url: "https://example.com" }),
      }),
    );
  });

  it("ApiError instances carry a status", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(JSON.stringify({}), { status: 409 }));
    vi.stubGlobal("fetch", fetchMock);
    try {
      await api.post("/api/downloads", { url: "x" }, "add failed");
      expect.unreachable();
    } catch (err) {
      expect(err).toBeInstanceOf(ApiError);
      expect((err as ApiError).status).toBe(409);
      expect((err as ApiError).message).toBe("add failed");
    }
  });
});

describe("auth expiry listener", () => {
  const offs: Array<() => void> = [];
  const listen = (fn: () => void) => {
    offs.push(onAuthExpired(fn));
    return fn;
  };
  afterEach(() => {
    vi.unstubAllGlobals();
    for (const off of offs.splice(0)) off();
  });

  it("a 401 on api.get notifies the registered listener once and still rejects", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("", { status: 401 })),
    );
    const listener = listen(vi.fn());

    await expect(api.get("/api/videos")).rejects.toBeInstanceOf(
      AuthExpiredError,
    );
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it("a 401 on api.postNoContent notifies too", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("", { status: 401 })),
    );
    const listener = listen(vi.fn());

    await expect(api.postNoContent("/api/x")).rejects.toBeInstanceOf(
      AuthExpiredError,
    );
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it("a non-401 failure does not notify", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("", { status: 500 })),
    );
    const listener = listen(vi.fn());

    await expect(api.get("/api/videos")).rejects.toBeInstanceOf(ApiError);
    expect(listener).not.toHaveBeenCalled();
  });

  it("the returned unsubscribe stops notifications, and listeners do not pre-empt each other", () => {
    const first = vi.fn();
    const second = vi.fn();
    const off = onAuthExpired(first);
    off();
    notifyAuthExpired();
    expect(first).not.toHaveBeenCalled();

    listen(first);
    listen(second);
    notifyAuthExpired();
    expect(first).toHaveBeenCalledTimes(1);
    expect(second).toHaveBeenCalledTimes(1);
  });

  it("notifying with no listener is a no-op", () => {
    expect(() => notifyAuthExpired()).not.toThrow();
  });
});
