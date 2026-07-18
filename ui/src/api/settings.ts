import { api } from "./http";
import type { CookieHealth, Settings, SettingsPatch } from "./types";

export async function getSettings(): Promise<Settings> {
  return api.get<Settings>("/api/settings", "failed to load settings");
}

// updateSettings never accepts (or receives back) the cookie body — see
// putCookie for the only way a cookie is ever written.
export async function updateSettings(patch: SettingsPatch): Promise<Settings> {
  return api.put<Settings>("/api/settings", patch, "failed to update settings");
}

// putCookie is the only endpoint that ever writes the pasted cookie. It
// responds with the same cookie-body-free Settings view as getSettings.
export async function putCookie(text: string): Promise<Settings> {
  return api.put<Settings>("/api/settings/cookie", { cookie: text }, "failed to save cookie");
}

export async function cookieHealth(): Promise<CookieHealth> {
  return api.get<CookieHealth>("/api/cookie/health", "failed to load cookie health");
}
