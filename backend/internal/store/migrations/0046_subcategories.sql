-- Subcategories: the fixed vocabulary in internal/videos/subcategory.go, keyed
-- "category/id". Up to three per video (of the video's own category) and up to
-- three per channel (of any category), first = primary. MCP search and Ask
-- filter by them; the Player details card and the channel header show them.
--
-- Their own tables, not columns on videos: a video holds several, and the
-- filter must seek by subcategory (idx on (subcategory, video_id)) rather than
-- walk videos rows, whose small columns sit behind the big text ones.
CREATE TABLE video_subcategories (
    video_id    TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    subcategory TEXT NOT NULL,
    rank        INTEGER NOT NULL,
    PRIMARY KEY (video_id, subcategory)
);
CREATE INDEX idx_video_subcategories_sub ON video_subcategories (subcategory, video_id);

-- One row per video the classifier has answered for, empty answer included, so
-- the idle sweep does not ask again. category records which category the
-- labels were chosen under; a category change deletes the row (and the labels),
-- handing the video back to the sweep.
CREATE TABLE video_subcategory_state (
    video_id      TEXT PRIMARY KEY REFERENCES videos(id) ON DELETE CASCADE,
    category      TEXT NOT NULL,
    classified_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE channel_subcategories (
    channel_id  TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    subcategory TEXT NOT NULL,
    rank        INTEGER NOT NULL,
    PRIMARY KEY (channel_id, subcategory)
);
CREATE INDEX idx_channel_subcategories_sub ON channel_subcategories (subcategory, channel_id);

-- manual = 1: the user set the labels on the channel page; the classifier never
-- overwrites them. video_count is how many videos the channel had when the
-- classifier last ran, so a channel classified from a handful of titles is
-- asked again once it has grown.
CREATE TABLE channel_subcategory_state (
    channel_id    TEXT PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE,
    manual        INTEGER NOT NULL DEFAULT 0,
    video_count   INTEGER NOT NULL DEFAULT 0,
    classified_at TEXT NOT NULL DEFAULT (datetime('now'))
);
