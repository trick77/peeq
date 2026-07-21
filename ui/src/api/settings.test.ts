import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  getSettings,
  updateSettings,
  putCookie,
  cookieHealth,
  getAPITokenStatus,
  createAPIToken,
} from "./settings";
import { AuthExpiredError } from "./http";

beforeEach(() => {
  vi.restoreAllMocks();
});

const settingsBody = {
  cookie_status: "ok",
  format_preset: "best",
  format_custom: "",
  limit_rate: "",
  throttle_base_seconds: 0,
  retention_days: 30,
  min_free_gb: 5,
  min_video_duration_seconds: 0,
  ytdlp_version: "2024.1.1",
  youtube_paused: false,
  youtube_pause_reason: "",
};

describe("settings api", () => {
  it("getSettings GETs /api/settings", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify(settingsBody), { status: 200 }),
      );
    const settings = await getSettings();
    expect(f.mock.calls[0][0]).toBe("/api/settings");
    expect(settings.cookie_status).toBe("ok");
  });

  it("updateSettings PUTs the patch body to /api/settings", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify(settingsBody), { status: 200 }),
      );
    await updateSettings({ retention_days: 60 });
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/settings");
    expect(init!.method).toBe("PUT");
    expect(JSON.parse(init!.body as string)).toEqual({ retention_days: 60 });
  });

  it("putCookie PUTs the cookie text to /api/settings/cookie", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify(settingsBody), { status: 200 }),
      );
    await putCookie("SID=abc123");
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/settings/cookie");
    expect(init!.method).toBe("PUT");
    expect(JSON.parse(init!.body as string)).toEqual({ cookie: "SID=abc123" });
  });

  it("cookieHealth GETs /api/cookie/health", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ status: "valid", present: true }), {
        status: 200,
      }),
    );
    const health = await cookieHealth();
    expect(f.mock.calls[0][0]).toBe("/api/cookie/health");
    expect(health.present).toBe(true);
  });

  it("getAPITokenStatus GETs /api/settings/token", async () => {
    const f = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValue(
        new Response(JSON.stringify({ present: false }), { status: 200 }),
      );
    const status = await getAPITokenStatus();
    expect(f.mock.calls[0][0]).toBe("/api/settings/token");
    expect(status.present).toBe(false);
  });

  it("createAPIToken POSTs to /api/settings/token and returns the plaintext token", async () => {
    const f = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          token: "secret-token",
          created_at: "2026-07-20T00:00:00Z",
        }),
        { status: 200 },
      ),
    );
    const created = await createAPIToken();
    const [url, init] = f.mock.calls[0];
    expect(url).toBe("/api/settings/token");
    expect(init!.method).toBe("POST");
    expect(created.token).toBe("secret-token");
  });

  it("getSettings throws AuthExpiredError on a 401", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({}), { status: 401 }),
    );
    await expect(getSettings()).rejects.toBeInstanceOf(AuthExpiredError);
  });
});
