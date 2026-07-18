package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/trick77/vark/internal/videos"
)

// videoDTO is the JSON shape returned by the videos API. It mirrors
// videos.Video, translating the zero-value sentinel used internally
// (empty string / nil) into fields the frontend can read directly — the
// "Expires in N days" UI is watched_at + settings.retention_days.
type videoDTO struct {
	ID                    string  `json:"id"`
	URL                   string  `json:"url"`
	Title                 string  `json:"title"`
	ChannelID             string  `json:"channel_id"`
	ChannelName           string  `json:"channel_name"`
	DurationSeconds       int64   `json:"duration_seconds,omitempty"`
	PublishedAt           string  `json:"published_at,omitempty"`
	Description           string  `json:"description,omitempty"`
	ThumbnailPath         string  `json:"thumbnail_path,omitempty"`
	HasMedia              bool    `json:"has_media"`
	FilesizeBytes         int64   `json:"filesize_bytes,omitempty"`
	FormatUsed            string  `json:"format_used,omitempty"`
	Availability          string  `json:"availability"`
	Status                string  `json:"status"`
	ErrorMessage          string  `json:"error_message,omitempty"`
	Watched               bool    `json:"watched"`
	WatchedAt             string  `json:"watched_at,omitempty"`
	ResumePositionSeconds float64 `json:"resume_position_seconds"`
	Favorite              bool    `json:"favorite"`
	DownloadedAt          string  `json:"downloaded_at,omitempty"`
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
		ThumbnailPath:         v.ThumbnailPath,
		HasMedia:              v.MediaPath != "",
		FilesizeBytes:         v.FilesizeBytes,
		FormatUsed:            v.FormatUsed,
		Availability:          v.Availability,
		Status:                v.Status,
		ErrorMessage:          v.ErrorMessage,
		Watched:               v.Watched,
		WatchedAt:             v.WatchedAt,
		ResumePositionSeconds: v.ResumePositionSeconds,
		Favorite:              v.Favorite,
		DownloadedAt:          v.DownloadedAt,
	}
}

// handleListVideos returns the video library, optionally narrowed by
// ?filter=all|unwatched|watched|favorites|downloading (see videos.Store.List
// for exact filter semantics; an unrecognized/empty filter means "all").
func (s *server) handleListVideos(w http.ResponseWriter, r *http.Request) {
	if s.videos == nil {
		writeJSON(w, []videoDTO{})
		return
	}
	all, err := s.videos.List(r.URL.Query().Get("filter"))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "list videos failed")
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

	if v.MediaPath != "" {
		if safe, err := safeMediaPath(s.mediaDir, v.MediaPath); err == nil {
			removeMediaAndSidecars(safe)
		}
	}
	if v.ThumbnailPath != "" {
		// A queued-but-not-yet-downloaded video may have a remote thumbnail
		// URL here instead of a local path; safeMediaPath rejecting that (or
		// the file simply not existing under MediaDir) is harmless — there
		// is nothing on local disk to remove in that case.
		if safe, err := safeMediaPath(s.mediaDir, v.ThumbnailPath); err == nil {
			_ = os.Remove(safe)
		}
	}

	if err := s.videos.Tombstone(v.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "delete video failed")
		return
	}
	writeJSON(w, map[string]string{"status": "tombstoned"})
}

// removeMediaAndSidecars unlinks the media file itself plus any sibling
// subtitle (.vtt) files in the same directory. Best-effort: a missing file
// is not an error (it may already be gone), and any single removal failure
// doesn't stop the others.
func removeMediaAndSidecars(mediaPath string) {
	_ = os.Remove(mediaPath)

	dir := filepath.Dir(mediaPath)
	base := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, base) && strings.HasSuffix(name, ".vtt") {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
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
		writeJSONError(w, http.StatusInternalServerError, "set favorite failed")
		return
	}
	writeJSON(w, map[string]bool{"favorite": newVal})
}

// watchedRequest is the required body of POST .../watched.
type watchedRequest struct {
	Watched *bool `json:"watched"`
}

// handleWatchedVideo is the manual watched toggle. true marks watched
// (without resetting watched_at if already set); false clears both watched
// and watched_at, rescuing the video from the retention sweep.
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
		writeJSONError(w, http.StatusInternalServerError, "set watched failed")
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
	if err := s.videos.SetResume(v.ID, *req.Position); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "set resume failed")
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
	safe, err := safeMediaPath(s.mediaDir, v.MediaPath)
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
		writeJSONError(w, http.StatusInternalServerError, "media not available")
		return
	}
	http.ServeContent(w, r, filepath.Base(safe), stat.ModTime(), f)
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
		writeJSONError(w, http.StatusInternalServerError, "get video failed")
		return nil, false
	}
	if v == nil {
		writeJSONError(w, http.StatusNotFound, "video not found")
		return nil, false
	}
	return v, true
}

// safeMediaPath resolves a stored media_path safely under mediaDir. It
// rejects paths (relative or absolute) that traverse or otherwise resolve
// outside mediaDir, and rejects symlink escapes by evaluating symlinks on
// both mediaDir and the candidate path (or its nearest existing ancestor,
// for a not-yet-existing file such as a rename target) before confirming
// containment. This is the single place the stream and delete/tombstone
// handlers trust a database-stored path enough to touch the filesystem.
func safeMediaPath(mediaDir, storedPath string) (string, error) {
	if mediaDir == "" {
		return "", errors.New("media dir not configured")
	}
	if storedPath == "" {
		return "", errors.New("empty media path")
	}

	absMediaDir, err := filepath.Abs(mediaDir)
	if err != nil {
		return "", fmt.Errorf("resolve media dir: %w", err)
	}

	var candidate string
	if filepath.IsAbs(storedPath) {
		candidate = filepath.Clean(storedPath)
	} else {
		candidate = filepath.Join(absMediaDir, storedPath)
	}
	if err := requireWithin(absMediaDir, candidate); err != nil {
		return "", err
	}

	resolvedMediaDir, err := filepath.EvalSymlinks(absMediaDir)
	if err != nil {
		return "", fmt.Errorf("resolve media dir symlinks: %w", err)
	}
	resolvedCandidate, err := resolveExistingOrAncestor(candidate)
	if err != nil {
		return "", err
	}
	if err := requireWithin(resolvedMediaDir, resolvedCandidate); err != nil {
		return "", err
	}

	return candidate, nil
}

// requireWithin errors unless candidate is root or a descendant of root
// (lexically — no filesystem access).
func requireWithin(root, candidate string) error {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes media dir %q", candidate, root)
	}
	return nil
}

// resolveExistingOrAncestor evaluates symlinks on path, walking up to the
// nearest existing ancestor if path itself doesn't exist yet (e.g. a file
// about to be created), then rejoins the non-existing suffix.
func resolveExistingOrAncestor(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	resolvedParent, err := resolveExistingOrAncestor(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(path)), nil
}
