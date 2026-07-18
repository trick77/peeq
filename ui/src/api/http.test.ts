import { describe, it, expect, vi, afterEach } from "vitest";
import { api, AuthExpiredError, ApiError } from "./http";

describe("api http client", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("api.get throws AuthExpiredError on 401", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "unauthorized" }), { status: 401 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.get("/api/videos")).rejects.toBeInstanceOf(AuthExpiredError);
  });

  it("api.get throws ApiError with the server message on other non-2xx", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ error: "video not found" }), { status: 404 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.get("/api/videos/x")).rejects.toMatchObject({
      status: 404,
      message: "video not found",
    });
  });

  it("api.get resolves the decoded JSON body on 200", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ id: "abc" }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.get("/api/videos/abc")).resolves.toEqual({ id: "abc" });
  });

  it("api.post sends a JSON body and the given method", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
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
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({}), { status: 409 }));
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
