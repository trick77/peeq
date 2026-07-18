import { api, ApiError } from "./http";
import { streamSSE, type SSEEvent } from "./stream";
import type { Job } from "./types";

// CookieRequiredError signals the 409 handleDownloadsPost returns when no
// usable cookie is stored — the UI should prompt the user to paste one
// rather than show a generic failure.
export class CookieRequiredError extends Error {
  constructor() {
    super("cookie required");
  }
}

// InvalidUrlError signals the 400s handleDownloadsPost returns for a
// playlist link, a live/premiere video, or an otherwise unparsable url.
// message carries the server's specific reason (surfaced to the user).
export class InvalidUrlError extends Error {}

export async function addDownload(url: string): Promise<Job> {
  try {
    const created = await api.post<{
      job_id: number;
      video_id: string;
      title?: string;
      channel_name?: string;
      state: string;
      priority: number;
    }>("/api/downloads", { url }, "failed to add download");
    return { ...created, attempts: 0 };
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      throw new CookieRequiredError();
    }
    if (err instanceof ApiError && err.status === 400) {
      throw new InvalidUrlError(err.message);
    }
    throw err;
  }
}

export async function listDownloads(): Promise<Job[]> {
  return api.get<Job[]>("/api/downloads", "failed to load downloads");
}

export async function cancelDownload(jobId: number): Promise<void> {
  await api.post(`/api/downloads/${jobId}/cancel`, undefined, "failed to cancel download");
}

// streamDownloads subscribes to the SSE download progress/queue feed. The
// only event name the worker currently publishes is "progress" (job_id,
// percent, speed, eta); onEvent still receives the raw (event, data) pair
// so callers aren't broken if the backend adds queue-state events later.
export function streamDownloads(onEvent: (event: SSEEvent) => void, signal?: AbortSignal): Promise<void> {
  return streamSSE("/api/downloads/stream", onEvent, signal);
}
