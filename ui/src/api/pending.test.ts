import { describe, it, expect, vi, beforeEach } from "vitest";
import { listPending, downloadPending, ignorePending } from "./pending";

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("pending api", () => {
  it("listPending GETs /api/pending", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(new Response("[]", { status: 200 }));
    await listPending();
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/pending");
    expect(init).toBeUndefined();
  });

  it("downloadPending POSTs to /api/pending/{id}/download", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ status: "queued" }), { status: 200 }),
      );
    await downloadPending("v1");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/pending/v1/download");
    expect(init!.method).toBe("POST");
  });

  it("ignorePending POSTs to /api/pending/{id}/ignore", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ status: "ignored" }), { status: 200 }),
      );
    await ignorePending("v1");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/pending/v1/ignore");
    expect(init!.method).toBe("POST");
  });
});
