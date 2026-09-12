-- 0029: drop the priced column 0028 added.
--
-- The token counts stay; the dollar figure goes. It was priced from a rate
-- table (peeq's own, later llmwire's) kept in step with a vendor page by hand,
-- and the one time the two were compared the table was off by half. Nothing
-- reads the column any more: the client no longer prices, the worker no longer
-- banks it and the video DTO no longer carries it.
ALTER TABLE videos DROP COLUMN chat_cost_nano_usd;
