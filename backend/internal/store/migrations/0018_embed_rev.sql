-- Chapter chunks change WHAT gets embedded, which the existing "have we
-- embedded this?" test cannot express. That test is `embed_model <> ''`, set
-- once and never revisited, so it cannot tell a video indexed under the old
-- recipe from one indexed under the new one.
--
-- embed_rev is the CONTENT recipe version — which kinds of chunk a video's
-- index contains — and is deliberately separate from embed_model/embed_dim,
-- which describe the MODEL. Either can change without the other: a new model
-- with the same chunk set, or a new chunk set with the same model.
--
-- Every existing row defaults to 0 and is therefore stale against the current
-- rag.ChunkRecipeRev, which is exactly what the boot-time backfill looks for.
ALTER TABLE videos ADD COLUMN embed_rev INTEGER NOT NULL DEFAULT 0;

-- Re-embedding is queued rather than done inline because it touches the whole
-- library at once: one row per video, drained one at a time so a backfill
-- trickles instead of bursting at the embeddings endpoint.
--
-- Shape mirrors summary_jobs (0001_init.sql) exactly — no priority, no log
-- tail, no cancel. Unlike that queue this one costs no chat calls at all:
-- everything a re-embed needs is already stored (summary, chapters, and the
-- subtitle file on disk), so it is retryable for free.
--
-- This table is temporary by design: once every video reaches the current
-- recipe it has no further work to do, and issue #240 tracks removing it. The
-- embed_rev column above is the part that stays.
CREATE TABLE embed_jobs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    video_id     TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    state        TEXT NOT NULL DEFAULT 'pending'
                     CHECK (state IN ('pending','running','done','failed')),
    attempts     INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    last_error   TEXT NOT NULL DEFAULT '',
    enqueued_at  TEXT NOT NULL DEFAULT (datetime('now')),
    started_at   TEXT,
    finished_at  TEXT
);

CREATE INDEX idx_embed_jobs_state ON embed_jobs(state);
CREATE INDEX idx_embed_jobs_video ON embed_jobs(video_id);
