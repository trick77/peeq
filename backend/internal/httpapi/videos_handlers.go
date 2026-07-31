package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	// ThumbnailVersion goes in the poster's URL, which is what lets that
	// response be cached as immutable: the URL changes exactly when the bytes
	// do. Omitted when there is no poster, where has_thumbnail already said so.
	ThumbnailVersion string `json:"thumbnail_version,omitempty"`
	HasMedia         bool   `json:"has_media"`
	FilesizeBytes    int64  `json:"filesize_bytes,omitempty"`
	FormatUsed       string `json:"format_used,omitempty"`
	// The media facts the player's stat strip shows, as ffprobe reported them
	// ("mp4", "h264", 1080, "aac"). omitempty on purpose: an unprobed video
	// omits the columns rather than rendering blanks. Friendly wording
	// ("H.264", "1080p") is the UI's job, not the wire's, so it can change
	// without a migration.
	MediaContainer        string  `json:"media_container,omitempty"`
	VideoCodec            string  `json:"video_codec,omitempty"`
	VideoHeight           int64   `json:"video_height,omitempty"`
	AudioCodec            string  `json:"audio_codec,omitempty"`
	Availability          string  `json:"availability"`
	Status                string  `json:"status"`
	ErrorMessage          string  `json:"error_message,omitempty"`
	Watched               bool    `json:"watched"`
	WatchedAt             string  `json:"watched_at,omitempty"`
	ResumePositionSeconds float64 `json:"resume_position_seconds"`
	// StateVersion is the row's watched-state generation counter. The Player
	// echoes it back on every resume POST so a watched toggle made elsewhere
	// can't be undone by a client that never saw it — see videos.SetResume
	// and issue #97.
	StateVersion         int64                    `json:"state_version"`
	Favorite             bool                     `json:"favorite"`
	DownloadedAt         string                   `json:"downloaded_at,omitempty"`
	SponsorblockSegments []sponsorblockSegmentDTO `json:"sponsorblock_segments,omitempty"`
	Summary              string                   `json:"summary"`
	Chapters             json.RawMessage          `json:"chapters,omitempty"`
	KeyPoints            json.RawMessage          `json:"key_points,omitempty"`
	SummaryStatus        string                   `json:"summary_status"`
	Category             string                   `json:"category"`
	AudioLanguage        string                   `json:"audio_language"`
	HasSubtitles         bool                     `json:"has_subtitles"`
	// MediaType/LiveStatus/YTTags/YTCategories are YouTube's own facts about
	// the video, straight from yt-dlp. Note Category (peeq's classification
	// enum) and YTCategories (YouTube's labels) are different things that
	// happen to share a word.
	MediaType    string          `json:"media_type,omitempty"`
	LiveStatus   string          `json:"live_status,omitempty"`
	YTTags       json.RawMessage `json:"yt_tags,omitempty"`
	YTCategories json.RawMessage `json:"yt_categories,omitempty"`
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
		HasThumbnail:          v.HasThumbnail,
		ThumbnailVersion:      v.ThumbnailVersion,
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
		StateVersion:          v.StateVersion,
		Favorite:              v.Favorite,
		DownloadedAt:          v.DownloadedAt,
		SponsorblockSegments:  parseSponsorblockSegments(v.SponsorblockSegments),
		Summary:               v.Summary,
		Chapters:              rawJSONOrNil(v.Chapters),
		KeyPoints:             rawJSONOrNil(v.KeyPoints),
		SummaryStatus:         v.SummaryStatus,
		Category:              v.Category,
		AudioLanguage:         v.AudioLanguage,
		HasSubtitles:          v.HasTranscript,
		MediaType:             v.MediaType,
		LiveStatus:            v.LiveStatus,
		YTTags:                rawJSONOrNil(v.YTTags),
		YTCategories:          rawJSONOrNil(v.YTCategories),
	}
}

// handleListVideos returns the video library, optionally narrowed by
// ?filter=all|unwatched|watched|favorites (see videos.Store.List for exact
// filter semantics; an unrecognized/empty filter means "all") and
// independently by ?category=<id>, which is ANDed with ?filter= rather than
// replacing it. An empty or unrecognized category value means "all
// categories" (see videos.Store.List). ?q= narrows by a case-insensitive
// title substring search, and
// ?sort=newest|oldest|added_newest|added_oldest|longest|title controls
// ordering (unrecognized/empty falls back to newest). newest/oldest are the
// default and mean the video's release date; the added_* pair means when peeq
// fetched the file, with never-downloaded videos last.
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
// tombstones the video (unlinking the media file from disk to reclaim space,
// keeping the thumbnail so the remembered card still has a poster and the
// subtitle so the transcript survives) while keeping the row for watched
// history and a future re-download badge. Never-delete-a-playing-video is a
// Task 12 sweeper concern, not this endpoint's.
func (s *server) handleDeleteVideo(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}

	media.RemoveTombstonedVideoFiles(s.mediaDir, v.MediaPath)

	if err := s.videos.Tombstone(v.ID); err != nil {
		serverError(w, r, err, "delete video failed")
		return
	}
	// A deleted video must not stay "now playing" — the rail would keep offering
	// to reopen it. Belt and braces with playback.Store.Get's own status filter,
	// which the tombstone (row kept, status flipped) is why we need at all.
	s.clearPlaybackIfPointing(r, v.ID)
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

// watchedResponse is the body of POST .../watched.
type watchedResponse struct {
	Watched      bool  `json:"watched"`
	StateVersion int64 `json:"state_version"`
}

// handleWatchedVideo is the manual watched toggle. true marks watched
// (without resetting watched_at if already set); false clears both watched
// and watched_at, rescuing the video from the retention sweep. Either
// direction resets resume_position_seconds to 0 — see videos.SetWatched.
// The response carries the new watched flag and the bumped state_version, but
// not the zeroed position: a client holding a local copy of the video has to
// zero that itself.
//
// Returning the version is not a convenience. The Player's watched toggle
// pauses and rewinds (#87), the spec fires a timeupdate on that seek even while
// paused, and the resulting resume POST would still carry the pre-toggle
// version — so without handing the new one back here, the very client that
// pressed the button would 409 against its own toggle and tell itself the video
// had been marked watched elsewhere.
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
	version, err := s.videos.SetWatched(v.ID, *req.Watched)
	if err != nil {
		serverError(w, r, err, "set watched failed")
		return
	}
	if *req.Watched {
		// Marking watched already zeroes the resume position, so leaving the
		// "now playing" pointer here would have the rail reopen a finished video
		// at 0:00. Un-watching means "I want this back", so it keeps the pointer.
		s.clearPlaybackIfPointing(r, v.ID)
	}
	writeJSON(w, watchedResponse{Watched: *req.Watched, StateVersion: version})
}

// resumeRequest is the required body of POST .../resume.
type resumeRequest struct {
	Position *float64 `json:"position"`
	// StateVersion is the videos.state_version the client last read, echoed
	// back so a position written by a client that never saw a watched toggle
	// elsewhere can be refused (issue #97). Optional on purpose: nil skips the
	// check entirely, which keeps every non-Player caller — and an older cached
	// SPA bundle still pinging after a deploy — working exactly as before.
	StateVersion *int64 `json:"state_version"`
}

// resumeResponse is the body of POST .../resume. It reports the row's version
// after the write so the client can keep echoing a current value: SetResume's
// own >=90% auto-watch bumps it, and a client refreshing only from GET
// /api/videos/{id} would 409 against its own threshold crossing.
type resumeResponse struct {
	Position     float64 `json:"position"`
	StateVersion int64   `json:"state_version"`
	Watched      bool    `json:"watched"`
}

// handleResumeVideo records the player's resume position. The >=90%
// auto-watched rule (and its no-reset-on-rewatch guarantee) lives in
// videos.Store.SetResume; this handler is just the transport, plus the mapping
// of a stale echoed version to 409 so the losing client refetches instead of
// clobbering.
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
	version, watched, err := s.videos.SetResume(v.ID, *req.Position, req.StateVersion)
	if errors.Is(err, videos.ErrStaleVersion) {
		writeJSONError(w, http.StatusConflict, "video state changed on another device")
		return
	}
	if err != nil {
		serverError(w, r, err, "set resume failed")
		return
	}
	writeJSON(w, resumeResponse{Position: *req.Position, StateVersion: version, Watched: watched})
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

// handleVideoThumbnail serves the video's poster from the database (migration
// 0022). There is no filesystem lookup left here: the bytes are either stored on
// the row or the video has no poster, which is the whole point of the move — a
// path column could go stale under a perfectly good image, stored bytes cannot.
//
// 404 covers both "no video" and "video has no poster". Videos deleted to
// reclaim space keep theirs: a tombstone takes the media file, not the card.
func (s *server) handleVideoThumbnail(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	serveThumbnail(w, r, s.videos, v.ID, imageOwnedHour)
}

// serveStoredImage writes one image held in the database — a video poster, a
// channel avatar or banner, an inbox poster. Every asset route funnels through
// here so they cannot drift on caching or content type.
//
// ServeContent gets the stored updated_at as the modification time, so
// conditional requests still work and a browser that already has the image gets
// a 304 instead of the bytes; the name carries the extension matching the stored
// mime, which is what makes the Content-Type right. The lookup stays with the
// caller — the stores are different types and only the writing is shared.
//
// The ETag is what makes that 304 dependable. Last-Modified alone rests on a
// stamp that is second-resolution, and missing entirely on the inbox's
// lazy-fetch path; a content hash is right on every path. ServeContent reads the
// header we set here, so it must go on before the call.
func serveStoredImage(w http.ResponseWriter, r *http.Request, mime string, data []byte, updatedAt string) {
	modTime, err := time.Parse("2006-01-02 15:04:05", updatedAt)
	if err != nil {
		// An unparsable stamp only costs conditional requests, never the image:
		// a zero time makes ServeContent skip the Last-Modified header.
		modTime = time.Time{}
	}
	w.Header().Set("ETag", etagFor(data))
	http.ServeContent(w, r, "image"+media.ThumbnailExtForMime(mime), modTime, bytes.NewReader(data))
}

// serveThumbnail writes one stored video poster, shared by the library and
// share-page endpoints so the two cannot drift. The caller passes its own
// Cache-Control because that is the one thing the two do not share: the library
// route is behind a session and the share route is behind a link.
func serveThumbnail(w http.ResponseWriter, r *http.Request, store *videos.Store, videoID string, policy imagePolicy) {
	if store == nil {
		writeJSONError(w, http.StatusNotFound, "thumbnail not available")
		return
	}
	t, err := store.GetThumbnail(videoID)
	if err != nil {
		serverError(w, r, err, "thumbnail not available")
		return
	}
	if t == nil {
		writeJSONError(w, http.StatusNotFound, "no thumbnail for this video")
		return
	}
	policy.apply(w, r)
	serveStoredImage(w, r, t.Mime, t.Bytes, t.UpdatedAt)
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
	if v.Status != videos.StatusError && v.Status != videos.StatusTombstoned {
		writeJSONError(w, http.StatusConflict, "only failed or removed videos can be re-downloaded")
		return
	}
	if s.jobs == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "downloads are not configured")
		return
	}
	// A video swept for age is watched with an aged watched_at, so simply
	// re-queueing it would leave it matching SweepCandidates and the next hourly
	// pass would reclaim the media this download is about to fetch. Restarting the
	// retention clock rescues it for a full retention_days instead.
	//
	// This used to mark the video unwatched, which bought the same rescue by
	// rewriting history: a tombstoned video is NOT always watched (the manual
	// Delete tombstones an unwatched video just as happily), and one that was
	// watched stayed watched in the only sense that matters — you watched it. The
	// lifecycle state and the watch state move independently.
	if err := s.videos.RestartRetentionClock(v.ID); err != nil {
		serverError(w, r, err, "restart retention clock failed")
		return
	}
	if err := s.videos.SetStatus(v.ID, videos.StatusQueued, ""); err != nil {
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
