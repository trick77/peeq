-- What each video's analysis cost, in tokens and in money.
--
-- Until now the token counts a chat call reports were logged and dropped
-- (internal/llm/callinfo.go). Logs answer "why was that run slow" but not "what
-- has this library cost me", because a log is retention-bound and a video is
-- not: the analysis of a video downloaded a year ago is long out of any log,
-- and its cost is still a fact about the row.
--
-- Chat only. Embedding tokens come from a different endpoint with a different
-- price and are deliberately not folded in here — a single blended figure would
-- be untraceable to either. The chat_ prefix matches the chat_tokens_* keys the
-- worker already logs, so the column and the log line name the same thing.
--
-- Written additively (see videos.AddChatUsage), so the counts cover every
-- attempt the job queue spent on the video, not just the one that succeeded.
ALTER TABLE videos ADD COLUMN chat_prompt_tokens     INTEGER NOT NULL DEFAULT 0;

-- Cached prompt tokens, a SUBSET of chat_prompt_tokens rather than an addition
-- to it — the endpoint reports prompt_tokens_details.cached_tokens inside
-- prompt_tokens. Stored separately because it is a fifth of the price, so the
-- cost below cannot be re-derived from the other three columns without it.
ALTER TABLE videos ADD COLUMN chat_cached_tokens     INTEGER NOT NULL DEFAULT 0;

-- Completion tokens, which INCLUDE the model's reasoning tokens. Thinking
-- cannot be switched off on this deployment (see internal/llm/thinking.go), so
-- that share is never zero and is never priced twice.
ALTER TABLE videos ADD COLUMN chat_completion_tokens INTEGER NOT NULL DEFAULT 0;

-- Cost in NANODOLLARS — billionths of a dollar, an integer, never a float.
-- A whole video costs well under a cent at current rates, so a REAL column
-- would be storing money in exactly the digits that drift.
--
-- Frozen at the time the analysis ran, not derived on read. The tokens above
-- are kept alongside precisely so a rate change reprices future videos without
-- silently rewriting the history of past ones, and so a pricing bug stays
-- correctable from data that survived it.
ALTER TABLE videos ADD COLUMN chat_cost_nano_usd     INTEGER NOT NULL DEFAULT 0;
