package httpapi

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/trick77/peeq/internal/media"
)

// handleVideoSubtitles serves the video's subtitle (VTT) file, resolved
// safely under mediaDir exactly like handleVideoThumbnail does for the
// thumbnail image. 404 covers both "no video" and "video has no local
// subtitle" (including any stale/remote subtitle_path — SafeMediaPath
// rejects that too, which is the correct outcome here).
func (s *server) handleVideoSubtitles(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	if v.SubtitlePath == "" {
		writeJSONError(w, http.StatusNotFound, "no subtitles for this video")
		return
	}
	safe, err := media.SafeMediaPath(s.mediaDir, v.SubtitlePath)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "subtitles not available")
		return
	}
	f, err := os.Open(safe)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "subtitles not available")
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		serverError(w, r, err, "subtitles not available")
		return
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	http.ServeContent(w, r, filepath.Base(safe), stat.ModTime(), f)
}
