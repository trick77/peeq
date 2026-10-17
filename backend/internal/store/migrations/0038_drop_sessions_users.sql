-- Sign-in moved to kit/auth: the session is an HMAC-signed cookie that carries
-- the identity itself, so there is no session table and no user row to look up.
-- Every open session ends with this migration; everyone signs in once more.
--
-- sessions first: it references users, and with foreign_keys on, DROP TABLE
-- users would fire its ON DELETE CASCADE into sessions. Nothing else references
-- either table (0011_playback_state.sql notes sessions.user_id was the only
-- user reference in the schema), so no other row is touched.
DROP TABLE sessions;
DROP TABLE users;
