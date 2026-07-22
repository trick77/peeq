package taimport

import (
	"path/filepath"
	"strings"
)

// PathMapper resolves the source (TubeArchivist) and destination (peeq) file
// locations for an imported video. Every method is pure — no filesystem access
// — because path mapping is the highest-risk part of the import: a wrong
// subtitle path does not error, it makes the summarize worker silently see no
// transcript, so it is worth exhaustively unit-testing on its own.
//
// TubeArchivist stores files flat under its media volume as
// <channel>/<video>.mp4 and <channel>/<video>.<lang>.vtt, and thumbnails on a
// SEPARATE cache volume as videos/<lower(first char of id)>/<video>.jpg. peeq
// nests everything under a per-video directory, <channel>/<video>/, and stores
// media_path and thumbnail_path ABSOLUTE but subtitle_path RELATIVE to MediaDir.
type PathMapper struct {
	TAMediaRoot  string // read-only mount of TubeArchivist's media volume
	TACacheRoot  string // read-only mount of TubeArchivist's cache volume
	PeeqMediaDir string // peeq's media root (MediaDir)
}

// srcMedia is the .mp4 under the TA media mount. Paths are always built from
// the channel and video ids, never by parsing the API's media_url (which is
// url-quoted and prefixed).
func (m PathMapper) srcMedia(channelID, videoID string) string {
	return filepath.Join(m.TAMediaRoot, channelID, videoID+".mp4")
}

// srcSubtitle is the .<lang>.vtt under the TA media mount.
func (m PathMapper) srcSubtitle(channelID, videoID, lang string) string {
	return filepath.Join(m.TAMediaRoot, channelID, videoID+"."+lang+".vtt")
}

// srcThumbnail is the .jpg under the TA CACHE mount (a different volume from
// the media). TubeArchivist shards thumbnails by the lowercased first
// character of the video id; do NOT use the API's vid_thumb_url, which
// TubeArchivist rewrites on the way out. Returns "" for an empty id rather than
// panicking, so a malformed row skips instead of crashing the migration.
func (m PathMapper) srcThumbnail(videoID string) string {
	if videoID == "" {
		return ""
	}
	shard := strings.ToLower(videoID[:1])
	return filepath.Join(m.TACacheRoot, "videos", shard, videoID+".jpg")
}

// dstDir is peeq's per-video directory, MediaDir/<channel>/<video>/.
func (m PathMapper) dstDir(channelID, videoID string) string {
	return filepath.Join(m.PeeqMediaDir, channelID, videoID)
}

// dstMedia is the absolute path peeq stores in media_path and copies the mp4 to.
func (m PathMapper) dstMedia(channelID, videoID string) string {
	return filepath.Join(m.dstDir(channelID, videoID), videoID+".mp4")
}

// dstThumbnail is the absolute path peeq stores in thumbnail_path.
func (m PathMapper) dstThumbnail(channelID, videoID string) string {
	return filepath.Join(m.dstDir(channelID, videoID), videoID+".jpg")
}

// dstSubtitle is the absolute path the .vtt is copied to. It is NOT what peeq
// stores — see storedSubtitleRel.
func (m PathMapper) dstSubtitle(channelID, videoID, lang string) string {
	return filepath.Join(m.dstDir(channelID, videoID), videoID+"."+lang+".vtt")
}

// storedSubtitleRel is the subtitle path as peeq stores it: RELATIVE to
// MediaDir. media_path and thumbnail_path are stored absolute; subtitle_path is
// the odd one out, and getting it wrong makes the summarize worker silently see
// no transcript.
func (m PathMapper) storedSubtitleRel(channelID, videoID, lang string) string {
	return filepath.Join(channelID, videoID, videoID+"."+lang+".vtt")
}
