import { api } from "./http";

// getYtdlpVersion/updateYtdlp back the Settings page's "yt-dlp version +
// Update" control (Task 14). These hit the live binary (GET .../version
// shells out to `yt-dlp --version`; POST .../update runs the self-update)
// rather than the cached Settings.ytdlp_version column, so the Update
// button's result is always fresh.
export async function getYtdlpVersion(): Promise<string> {
  const res = await api.get<{ version: string }>("/api/ytdlp/version", "failed to read yt-dlp version");
  return res.version;
}

export async function updateYtdlp(): Promise<string> {
  const res = await api.post<{ version: string }>("/api/ytdlp/update", undefined, "failed to update yt-dlp");
  return res.version;
}
