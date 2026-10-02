import { api } from "./http";
import type { PendingItem } from "./types";

export async function listPending(channelId?: string): Promise<PendingItem[]> {
  const qs = channelId ? `?channel=${encodeURIComponent(channelId)}` : "";
  return api.get<PendingItem[]>(
    `/api/pending${qs}`,
    "failed to load pending videos",
  );
}

// countPending is the Inbox badge's number. A COUNT on the server: the shell
// refreshes it on every activity event and must not download the list for it.
export async function countPending(): Promise<number> {
  const r = await api.get<{ count: number }>(
    "/api/pending/count",
    "failed to load pending count",
  );
  return r.count;
}

export async function downloadPending(id: string): Promise<void> {
  await api.post(
    `/api/pending/${encodeURIComponent(id)}/download`,
    undefined,
    "failed to download video",
  );
}

export async function ignorePending(id: string): Promise<void> {
  await api.post(
    `/api/pending/${encodeURIComponent(id)}/ignore`,
    undefined,
    "failed to ignore video",
  );
}
