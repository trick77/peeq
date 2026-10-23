-- The re-embed backfill has done its one job: every video indexed under the
-- pre-chapter recipe was rebuilt under recipe 2. The worker, its queue and the
-- boot sweep are gone (issue #240), so this table has no writer and no reader.
--
-- videos.embed_rev deliberately STAYS. Nothing reads it today — the summarize
-- worker embeds unconditionally, because the step that would have gated on it
-- always invalidates the index immediately beforehand. It is kept as a RECORD
-- of which content recipe each video's index follows, which is knowable now and
-- impossible to reconstruct later, and which the next recipe change will need.
DROP TABLE IF EXISTS embed_jobs;
