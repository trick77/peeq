// Barrel for the API client. Split into one module per domain (auth,
// videos, downloads, settings) plus shared types (./types), the shared
// fetch helper (./http), and the SSE consumer (./stream) — mirroring
// loom's api/ layout. Re-exports the full public surface so
// `import { ... } from "../api"` call sites stay simple.
export * from "./types";
export { api, AuthExpiredError, ApiError } from "./http";
export * from "./stream";
export * from "./auth";
export * from "./videos";
export * from "./downloads";
export * from "./settings";
export * from "./ytdlp";
export * from "./channels";
export * from "./pending";
export * from "./summaries";
export * from "./search";
