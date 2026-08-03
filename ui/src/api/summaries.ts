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

// listFailedSummaries returns the jobs that ran out of attempts. They are not in
// listSummaries — that lane is work still to happen — and nothing else surfaces
// them: the boot sweep skips failed rows on purpose, and a job that died after
// the summary step left the video reading "done", so the Library and the Player
// both show it as finished while its highlights or its search index are missing.
export async function listFailedSummaries(): Promise<SummaryJob[]> {
  return api.get<SummaryJob[]>(
    "/api/summaries/failed",
    "failed to load failed summaries",
  );
}

// retryFailedSummaries puts every failed job back in the queue with a fresh
// attempt budget, and returns how many moved. The budget reset is the point:
// these usually failed together, against an LLM endpoint that was down or
// crawling, so what they need is another go rather than a diagnosis.
export async function retryFailedSummaries(): Promise<{ requeued: number }> {
  return api.post<{ requeued: number }>(
    "/api/summaries/retry-failed",
    undefined,
    "failed to retry summaries",
  );
}
