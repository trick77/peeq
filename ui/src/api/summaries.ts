import { api } from "./http";
import type { SummaryJob } from "./types";

// listSummaries returns the in-flight summary queue — every pending/running
// summary+embedding job — for the Queue page's "being summarized" lane. It is
// the read side of GET /api/summaries; the live phase of each job
// (summarizing → embedding) arrives separately over the "summary" SSE event
// the Player already consumes.
export async function listSummaries(): Promise<SummaryJob[]> {
  return api.get<SummaryJob[]>("/api/summaries", "failed to load summaries");
}
