import { api } from "./http";
import type { ActivityEvent, UpcomingItem } from "./types";

// ActivityPage is one keyset page of the background-work log, newest first.
// has_more says an older page exists; retained_max is the fixed row ceiling, so
// the UI can label the oldest edge.
export type ActivityPage = {
  events: ActivityEvent[];
  has_more: boolean;
  retained_max: number;
};

// listActivity fetches the past half of the agenda. before is the id to page
// back from (omit for the newest page); limit defaults server-side to 40.
export async function listActivity(
  before?: number,
  limit?: number,
): Promise<ActivityPage> {
  const qs = new URLSearchParams();
  if (before) qs.set("before", String(before));
  if (limit) qs.set("limit", String(limit));
  const suffix = qs.toString() ? `?${qs}` : "";
  return api.get<ActivityPage>(
    `/api/activity${suffix}`,
    "failed to load activity",
  );
}

export type UpcomingResponse = {
  items: UpcomingItem[];
  truncated: number;
};

// listUpcoming fetches the future half — a live projection over the current
// schedules and queues (nothing stored). truncated is how many beyond the cap
// were dropped, for the top-edge label.
export async function listUpcoming(): Promise<UpcomingResponse> {
  return api.get<UpcomingResponse>(
    "/api/activity/upcoming",
    "failed to load upcoming activity",
  );
}
