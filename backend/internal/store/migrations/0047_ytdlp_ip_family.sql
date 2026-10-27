-- Pin yt-dlp to one IP family. YouTube blocks by address and per family: a
-- server's IPv4 address answered every call, with and without a cookie,
-- "Video unavailable. This content isn't available." while the same cookie
-- over IPv6 played. '' leaves the choice to yt-dlp (today's behaviour).
ALTER TABLE settings ADD COLUMN ytdlp_ip_family TEXT NOT NULL DEFAULT ''
    CHECK (ytdlp_ip_family IN ('', '4', '6'));
