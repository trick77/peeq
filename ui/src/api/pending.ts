import { api } from "./http";
import type { PendingItem } from "./types";

export async function listPending(): Promise<PendingItem[]> {
  return api.get<PendingItem[]>("/api/pending", "failed to load pending videos");
}

export async function downloadPending(id: string): Promise<void> {
  await api.post(`/api/pending/${encodeURIComponent(id)}/download`, undefined, "failed to download video");
}

export async function ignorePending(id: string): Promise<void> {
  await api.post(`/api/pending/${encodeURIComponent(id)}/ignore`, undefined, "failed to ignore video");
}
