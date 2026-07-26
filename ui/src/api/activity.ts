import { api } from "./http";
import type { ActivityEvent, UpcomingItem } from "./types";

// ActivityPage is one keyset page of the background-work log, newest first.
// has_more says an older page exists; retained_max is the fixed row ceiling.
// Nothing renders retained_max today — History used to end its chip row with
// "keeps the last N entries", and that note is gone — but the server still sends
// it, and the ceiling is the kind of fact a future edge label would want.
export type ActivityPage = {
  events: ActivityEvent[];
  has_more: boolean;
  retained_max: number;
};

// listActivity fetches the past half of the agenda. before is the id to page
// back from (omit for the newest page); limit defaults server-side to 40; q
// narrows to rows whose subject, summary or detail contains it. The search is a
// server parameter rather than a client filter because the client only ever
// holds the pages it has scrolled to.
export async function listActivity(
  before?: number,
  limit?: number,
  q?: string,
): Promise<ActivityPage> {
  const qs = new URLSearchParams();
  if (before) qs.set("before", String(before));
  if (limit) qs.set("limit", String(limit));
  if (q) qs.set("q", q);
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
