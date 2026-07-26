// Wire enums: the TypeScript half of the vocabularies the backend names in Go.
//
// Every set here mirrors a Go const block that is itself pinned to a SQL CHECK
// constraint by backend/internal/store/enums_test.go. The chain is:
//
//   0001_init.sql CHECK  ->  Go constants  ->  this file
//        (enums_test.go)         (enumsync.test.ts)
//
// so a value can only drift if BOTH guards are removed. Before #196 these were
// bare string literals on both sides with nothing connecting them, which is how
// shell/CookieStatus.tsx came to document an "expired" status the backend has
// never sent while omitting "stale", which it does.
//
// Each set is a `const` array first and a union type derived from it second.
// That ordering matters: the array is what the drift test compares against Go,
// and the union is what the compiler enforces at every use site. Declaring the
// union by hand instead would leave the test with nothing to read.

// Video lifecycle — backend/internal/videos/status.go, videos.status.
export const VIDEO_STATUSES = [
  "new",
  "queued",
  "downloading",
  "downloaded",
  "tombstoned",
  "error",
] as const;
export type VideoStatus = (typeof VIDEO_STATUSES)[number];

// Summarize lifecycle — backend/internal/videos/status.go, videos.summary_status.
export const SUMMARY_STATUSES = [
  "pending",
  "running",
  "done",
  "error",
  "no_transcript",
] as const;
export type SummaryStatus = (typeof SUMMARY_STATUSES)[number];

// Video availability — backend/internal/videos/availability.go.
export const AVAILABILITIES = [
  "available",
  "deleted",
  "private",
  "geo",
  "unknown",
] as const;
export type Availability = (typeof AVAILABILITIES)[number];

// Download queue — backend/internal/jobs/state.go, download_jobs.state.
export const JOB_STATES = [
  "pending",
  "running",
  "done",
  "failed",
  "canceled",
] as const;
export type JobState = (typeof JOB_STATES)[number];

// Summarize queue — backend/internal/summaryjobs/state.go, summary_jobs.state.
// One value shorter than JOB_STATES on purpose: a summarize pass has no cancel.
export const SUMMARY_JOB_STATES = [
  "pending",
  "running",
  "done",
  "failed",
] as const;
export type SummaryJobState = (typeof SUMMARY_JOB_STATES)[number];

// YouTube cookie health — backend/internal/settings/cookiestatus.go.
export const COOKIE_STATUSES = ["absent", "valid", "stale", "blocked"] as const;
export type CookieStatus = (typeof COOKIE_STATUSES)[number];

// Summarize progress phases — backend/internal/summarize/phase.go.
//
// The one set here with no CHECK constraint behind it: nothing persists a
// phase, so this file and phase.go are the whole contract, and enumsync.test.ts
// is the only thing checking it. The empty string is also a valid phase on the
// wire ("no stage in flight", carried by the terminal done/error events) and is
// deliberately not a member — it is an absence, not a stage.
export const SUMMARY_PHASE_NAMES = [
  "summarizing",
  "classifying",
  "embedding",
  "keypoints",
] as const;
export type SummaryPhase = (typeof SUMMARY_PHASE_NAMES)[number];

// Account role — backend/internal/auth/model.go.
export const ROLES = ["admin", "user"] as const;
export type Role = (typeof ROLES)[number];
