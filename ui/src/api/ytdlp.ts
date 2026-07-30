import { api } from "./http";

// YtdlpVersion is what the version endpoint reports: the binary installed
// here, the newest release published upstream, and how that comparison went.
//
// update_available is computed server-side, so nothing here has to know how
// yt-dlp versions order. check_error and checked_at describe the UPSTREAM
// lookup only, and must be read together with latest: a failing check keeps
// the last known latest rather than blanking it, so an error beside it means
// "this is what we last knew", not "this is current".
export type YtdlpVersion = {
  version: string;
  latest?: string;
  update_available: boolean;
  checked_at?: string;
  check_error?: string;
};

// getYtdlpVersion/updateYtdlp back the Settings page's "yt-dlp version +
// Update" control (Task 14) and the rail's update indicator. These hit the
// live binary (GET .../version shells out to `yt-dlp --version`; POST
// .../update downloads and installs the latest release) rather than the
// cached Settings.ytdlp_version column, so the Update button's result is
// always fresh.
export async function getYtdlpVersion(): Promise<YtdlpVersion> {
  return api.get<YtdlpVersion>(
    "/api/ytdlp/version",
    "failed to read yt-dlp version",
  );
}

// YtdlpUpdate is what the update endpoint reports. `updated` describes the
// version, not the download: the backend reinstalls the latest release on
// every press, so `updated: false` means the version did not change.
export type YtdlpUpdate = {
  version: string;
  previous_version: string;
  updated: boolean;
};

export async function updateYtdlp(): Promise<YtdlpUpdate> {
  return api.post<YtdlpUpdate>(
    "/api/ytdlp/update",
    undefined,
    "failed to update yt-dlp",
  );
}
