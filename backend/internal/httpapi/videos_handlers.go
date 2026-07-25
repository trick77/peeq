package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/videos"
)

// videoDTO is the JSON shape returned by the videos API. It mirrors
// videos.Video, translating the zero-value sentinel used internally
// (empty string / nil) into fields the frontend can read directly — the
// "Expires in N days" UI is watched_at + settings.retention_days.
type videoDTO struct {
	ID              string `json:"id"`
	URL             string `json:"url"`
	Title           string `json:"title"`
	ChannelID       string `json:"channel_id"`
	ChannelName     string `json:"channel_name"`
	DurationSeconds int64  `json:"duration_seconds,omitempty"`
	PublishedAt     string `json:"published_at,omitempty"`
	Description     string `json:"description,omitempty"`
	HasThumbnail    bool   `json:"has_thumbnail"`
	HasMedia        bool   `json:"has_media"`
	FilesizeBytes   int64  `json:"filesize_bytes,omitempty"`
	FormatUsed      string `json:"format_used,omitempty"`
	// The media facts the player's stat strip shows, as ffprobe reported them
	// ("mp4", "h264", 1080, "aac"). omitempty on purpose: an unprobed video
	// omits the columns rather than rendering blanks. Friendly wording
	// ("H.264", "1080p") is the UI's job, not the wire's, so it can change
	// without a migration.
	MediaContainer        string                   `json:"media_container,omitempty"`
	VideoCodec            string                   `json:"video_codec,omitempty"`
	VideoHeight           int64                    `json:"video_height,omitempty"`
	AudioCodec            string                   `json:"audio_codec,omitempty"`
	Availability          string                   `json:"availability"`
	Status                string                   `json:"status"`
	ErrorMessage          string                   `json:"error_message,omitempty"`
	Watched               bool                     `json:"watched"`
	WatchedAt             string                   `json:"watched_at,omitempty"`
	ResumePositionSeconds float64                  `json:"resume_position_seconds"`
	Favorite              bool                     `json:"favorite"`
	DownloadedAt          string                   `json:"downloaded_at,omitempty"`
	SponsorblockSegments  []sponsorblockSegmentDTO `json:"sponsorblock_segments,omitempty"`
	Summary               string                   `json:"summary"`
	Chapters              json.RawMessage          `json:"chapters,omitempty"`
	KeyPoints             json.RawMessage          `json:"key_points,omitempty"`
	SummaryStatus         string                   `json:"summary_status"`
	Category              string                   `json:"category"`
	AudioLanguage         string                   `json:"audio_language"`
	HasSubtitles          bool                     `json:"has_subtitles"`
}

// sponsorblockSegmentDTO is one entry of the parsed sponsorblock_segments
// column (see download/worker.go's segmentJSON, which writes this exact
// shape). The player auto-skips [StartTime, EndTime) ranges client-side.
type sponsorblockSegmentDTO struct {
	Category  string  `json:"category"`
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
}

// parseSponsorblockSegments decodes the stored JSON text column. A blank or
// malformed value (including "[]", the empty-but-valid case) yields a nil
// slice, which the omitempty tag above then drops from the response
// entirely rather than emitting "sponsorblock_segments": [].
func parseSponsorblockSegments(raw string) []sponsorblockSegmentDTO {
	if raw == "" {
		return nil
	}
	var segs []sponsorblockSegmentDTO
	if err := json.Unmarshal([]byte(raw), &segs); err != nil || len(segs) == 0 {
		return nil
	}
	return segs
}

// rawJSONOrNil turns a stored JSON-text column (chapters/key_points) into a
// json.RawMessage so the client receives an actual array, not a
// double-encoded string. A blank or malformed value yields a JSON RawMessage
// of an empty array ("[]") rather than nil — belt-and-suspenders so the
// frontend's `chapters: Chapter[]` / `key_points: string[]` types can always
// safely .map() the field, even if a row ever stored "" or something
// unparsable, without relying solely on the migration's '[]' column default.
func rawJSONOrNil(raw string) json.RawMessage {
	if raw == "" || !json.Valid([]byte(raw)) {
		return json.RawMessage("[]")
	}
	return json.RawMessage(raw)
}

// toVideoDTO maps a store row to its JSON shape. media_path itself is
// never exposed (it's a server-local filesystem path); callers get a
// has_media bool and use GET .../stream to play it.
func toVideoDTO(v *videos.Video) videoDTO {
	return videoDTO{
		ID:                    v.ID,
		URL:                   v.URL,
		Title:                 v.Title,
		ChannelID:             v.ChannelID,
		ChannelName:           v.ChannelName,
		DurationSeconds:       v.DurationSeconds,
		PublishedAt:           v.PublishedAt,
		Description:           v.Description,
		HasThumbnail:          v.ThumbnailPath != "",
		HasMedia:              v.MediaPath != "",
		FilesizeBytes:         v.FilesizeBytes,
		FormatUsed:            v.FormatUsed,
		MediaContainer:        v.MediaContainer,
		VideoCodec:            v.VideoCodec,
		VideoHeight:           v.VideoHeight,
		AudioCodec:            v.AudioCodec,
		Availability:          v.Availability,
		Status:                v.Status,
		ErrorMessage:          v.ErrorMessage,
		Watched:               v.Watched,
		WatchedAt:             v.WatchedAt,
		ResumePositionSeconds: v.ResumePositionSeconds,
		Favorite:              v.Favorite,
		DownloadedAt:          v.DownloadedAt,
		SponsorblockSegments:  parseSponsorblockSegments(v.SponsorblockSegments),
		Summary:               v.Summary,
		Chapters:              rawJSONOrNil(v.Chapters),
		KeyPoints:             rawJSONOrNil(v.KeyPoints),
		SummaryStatus:         v.SummaryStatus,
		Category:              v.Category,
		AudioLanguage:         v.AudioLanguage,
		HasSubtitles:          v.SubtitlePath != "",
	}
}

// handleListVideos returns the video library, optionally narrowed by
// ?filter=all|unwatched|watched|favorites (see videos.Store.List for exact
// filter semantics; an unrecognized/empty filter means "all") and
// independently by ?category=<id>, which is ANDed with ?filter= rather than
// replacing it. An empty or unrecognized category value means "all
// categories" (see videos.Store.List). ?q= narrows by a case-insensitive
// title substring search, and
// ?sort=newest|oldest|air_newest|air_oldest|longest|title controls ordering
// (unrecognized/empty falls back to newest). newest/oldest mean when peeq
// added the video; the air_* pair means its release date.
func (s *server) handleListVideos(w http.ResponseWriter, r *http.Request) {
	if s.videos == nil {
		writeJSON(w, []videoDTO{})
		return
	}
	channelID := r.URL.Query().Get("channel")
	channelName := ""
	if channelID != "" && s.channels != nil {
		if c, cerr := s.channels.Get(channelID); cerr == nil && c != nil {
			channelName = c.Name
		} else if n, found, nerr := s.channels.NameFromVideos(channelID); nerr == nil && found {
			channelName = n
		}
	}
	all, err := s.videos.List(videos.ListOptions{
		Filter:      r.URL.Query().Get("filter"),
		Category:    r.URL.Query().Get("category"),
		Query:       r.URL.Query().Get("q"),
		Sort:        r.URL.Query().Get("sort"),
		ChannelID:   channelID,
		ChannelName: channelName,
	})
	if err != nil {
		serverError(w, r, err, "list videos failed")
		return
	}
	out := make([]videoDTO, 0, len(all))
	for i := range all {
		out = append(out, toVideoDTO(&all[i]))
	}
	writeJSON(w, out)
}

// handleGetVideo returns one video by id, 404 if it doesn't exist.
func (s *server) handleGetVideo(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	writeJSON(w, toVideoDTO(v))
}

// handleDeleteVideo is the manual DELETE endpoint: unconditionally
// tombstones the video (unlinking its media/thumbnail/subtitle files from
// disk to reclaim space) while keeping the row for watched history and a
// future re-download badge. Never-delete-a-playing-video is a Task 12
// sweeper concern, not this endpoint's.
func (s *server) handleDeleteVideo(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}

	media.RemoveVideoFiles(s.mediaDir, v.MediaPath, v.ThumbnailPath, v.SubtitlePath)

	if err := s.videos.Tombstone(v.ID); err != nil {
		serverError(w, r, err, "delete video failed")
		return
	}
	writeJSON(w, map[string]string{"status": "tombstoned"})
}

// favoriteRequest is the optional body of POST .../favorite. When absent or
// unparsable, the endpoint toggles the current value instead.
type favoriteRequest struct {
	Favorite *bool `json:"favorite"`
}

// handleFavoriteVideo sets (or toggles, if no body/field given) the
// favorite flag.
func (s *server) handleFavoriteVideo(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	var req favoriteRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // empty/invalid body => toggle

	newVal := !v.Favorite
	if req.Favorite != nil {
		newVal = *req.Favorite
	}
	if err := s.videos.SetFavorite(v.ID, newVal); err != nil {
		serverError(w, r, err, "set favorite failed")
		return
	}
	writeJSON(w, map[string]bool{"favorite": newVal})
}

// categoryRequest is the required body of POST .../category.
type categoryRequest struct {
	Category *string `json:"category"`
}

// handleCategoryVideo sets a video's category by hand, from the Player. The
// write is unconditional on purpose: the user overrules the model, never the
// other way round. What makes the choice stick without a "set by a human"
// flag is the other side — the classifier writes through
// videos.Store.SetCategoryIfUnset, which refuses a row that already has one.
//
// The id must be an exact enum member: unlike a model reply, which
// videos.NormalizeCategory repairs, a bad id here is a caller bug and is
// worth a 400 rather than a silent downgrade to 'uncategorized'.
func (s *server) handleCategoryVideo(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	var req categoryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Category == nil {
		writeJSONError(w, http.StatusBadRequest, "category (string) is required")
		return
	}
	if !videos.ValidCategory(*req.Category) {
		writeJSONError(w, http.StatusBadRequest, "unknown category")
		return
	}
	if err := s.videos.SetCategory(v.ID, *req.Category); err != nil {
		serverError(w, r, err, "set category failed")
		return
	}
	writeJSON(w, map[string]string{"category": *req.Category})
}

// watchedRequest is the required body of POST .../watched.
type watchedRequest struct {
	Watched *bool `json:"watched"`
}

// handleWatchedVideo is the manual watched toggle. true marks watched
// (without resetting watched_at if already set); false clears both watched
// and watched_at, rescuing the video from the retention sweep. Either
// direction resets resume_position_seconds to 0 — see videos.SetWatched.
// The response carries only the new watched flag, so a client holding a
// local copy of the video has to zero the position itself.
func (s *server) handleWatchedVideo(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	var req watchedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Watched == nil {
		writeJSONError(w, http.StatusBadRequest, "watched (bool) is required")
		return
	}
	if err := s.videos.SetWatched(v.ID, *req.Watched); err != nil {
		serverError(w, r, err, "set watched failed")
		return
	}
	writeJSON(w, map[string]bool{"watched": *req.Watched})
}

// resumeRequest is the required body of POST .../resume.
type resumeRequest struct {
	Position *float64 `json:"position"`
}

// handleResumeVideo records the player's resume position. The >=90%
// auto-watched rule (and its no-reset-on-rewatch guarantee) lives in
// videos.Store.SetResume; this handler is just the transport.
func (s *server) handleResumeVideo(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	var req resumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Position == nil {
		writeJSONError(w, http.StatusBadRequest, "position (number) is required")
		return
	}
	if *req.Position < 0 {
		writeJSONError(w, http.StatusBadRequest, "position must not be negative")
		return
	}
	if err := s.videos.SetResume(v.ID, *req.Position); err != nil {
		serverError(w, r, err, "set resume failed")
		return
	}
	writeJSON(w, map[string]float64{"position": *req.Position})
}

// handleStreamVideo serves the video's media file via http.ServeContent,
// which handles conditional requests and byte-range requests (the player
// needs Range support for seeking).
func (s *server) handleStreamVideo(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	if v.MediaPath == "" {
		writeJSONError(w, http.StatusNotFound, "no media for this video")
		return
	}
	// Record this access before serving so the retention sweeper's
	// now-playing guard (Task 12) sees an in-progress stream and skips this
	// video, even though the sweeper runs in a wholly separate package. A
	// player re-issues range requests throughout playback, so this keeps
	// firing for as long as the video is actually being watched.
	if s.streamAccess != nil {
		s.streamAccess.RecordAccess(v.ID)
	}
	safe, err := media.SafeMediaPath(s.mediaDir, v.MediaPath)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "media not available")
		return
	}
	f, err := os.Open(safe)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "media not available")
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		serverError(w, r, err, "media not available")
		return
	}
	// ?download=1 turns the same stream into a save-to-disk. It has to be the
	// server that names the file: media_path is deliberately never exposed in
	// videoDTO, so the UI cannot know whether this video is .mp4, .webm or
	// .mkv, and an <a download="…"> guessing an extension would rename the
	// file to something that no longer matches its contents.
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", attachmentDisposition(
			downloadFilename(v.Title, v.ID, filepath.Ext(safe)),
		))
	}
	http.ServeContent(w, r, filepath.Base(safe), stat.ModTime(), f)
}

// downloadFilename builds the name a downloaded media file is saved under:
// the video's title, stripped of anything that upsets a filesystem, plus the
// real extension of the stored file. Falls back to the video id for titles
// that reduce to nothing (e.g. a purely CJK or emoji title).
func downloadFilename(title, id, ext string) string {
	base := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == ' ', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, title)
	base = strings.Trim(strings.TrimSpace(base), "._")
	if len(base) > 120 {
		base = strings.TrimSpace(base[:120])
	}
	if base == "" {
		base = id
	}
	return base + ext
}

// attachmentDisposition emits both the plain and the RFC 5987 encoded form of
// the filename, which is what browsers expect when the name may contain
// non-ASCII: the bare `filename` is the legacy fallback, `filename*` is the
// one every current browser actually reads.
func attachmentDisposition(name string) string {
	return fmt.Sprintf("attachment; filename=%q; filename*=UTF-8''%s",
		name, url.PathEscape(name))
}

// handleVideoThumbnail serves the video's thumbnail image file, resolved
// safely under mediaDir exactly like handleStreamVideo does for the media
// file itself. 404 covers both "no video" and "video has no local
// thumbnail" (including the not-yet-downloaded case where ThumbnailPath may
// hold a remote URL rather than a local path — SafeMediaPath rejects that
// too, which is the correct outcome here).
func (s *server) handleVideoThumbnail(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	if v.ThumbnailPath == "" {
		writeJSONError(w, http.StatusNotFound, "no thumbnail for this video")
		return
	}
	safe, err := media.SafeMediaPath(s.mediaDir, v.ThumbnailPath)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "thumbnail not available")
		return
	}
	f, err := os.Open(safe)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "thumbnail not available")
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		serverError(w, r, err, "thumbnail not available")
		return
	}
	http.ServeContent(w, r, filepath.Base(safe), stat.ModTime(), f)
}

// handleRedownloadVideo re-queues a failed or tombstoned video for download.
// A fresh job row (attempts=0) is the "reset"; the worker's success path
// re-populates media and auto-enqueues a summary job, so re-download also
// re-indexes. Only error/tombstoned videos are eligible — re-downloading a
// queued/downloading video would double-enqueue, and a downloaded one is a
// no-op.
func (s *server) handleRedownloadVideo(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	if v.Status != "error" && v.Status != "tombstoned" {
		writeJSONError(w, http.StatusConflict, "only failed or removed videos can be re-downloaded")
		return
	}
	if s.jobs == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "downloads are not configured")
		return
	}
	// A tombstoned video is always watched=1 and aged (that's how it got
	// tombstoned by the retention sweeper); resetting the watched state here
	// rescues it from SweepCandidates so the sweeper doesn't delete the
	// freshly re-downloaded media within its next hourly pass. Mirrors the
	// existing "un-watch rescues from the auto-delete sweep" rule from P1.
	if err := s.videos.SetWatched(v.ID, false); err != nil {
		serverError(w, r, err, "reset watched state failed")
		return
	}
	if err := s.videos.SetStatus(v.ID, "queued", ""); err != nil {
		serverError(w, r, err, "requeue failed")
		return
	}
	if _, err := s.jobs.Enqueue(v.ID, downloadPriority); err != nil {
		serverError(w, r, err, "enqueue failed")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// lookupVideo resolves {id} from the route and fetches the video, writing
// the appropriate error response (503 if the store isn't wired, 404 if the
// id is unknown) and returning ok=false if the caller should stop.
func (s *server) lookupVideo(w http.ResponseWriter, r *http.Request) (*videos.Video, bool) {
	if s.videos == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "videos are not configured")
		return nil, false
	}
	id := r.PathValue("id")
	v, err := s.videos.Get(id)
	if err != nil {
		serverError(w, r, err, "get video failed")
		return nil, false
	}
	if v == nil {
		writeJSONError(w, http.StatusNotFound, "video not found")
		return nil, false
	}
	return v, true
}
