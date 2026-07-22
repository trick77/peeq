import { api } from "./http";
import type { PendingItem } from "./types";

export async function listPending(channelId?: string): Promise<PendingItem[]> {
  const qs = channelId ? `?channel=${encodeURIComponent(channelId)}` : "";
  return api.get<PendingItem[]>(
    `/api/pending${qs}`,
    "failed to load pending videos",
  );
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
