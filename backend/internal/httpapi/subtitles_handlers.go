package httpapi

import (
	"net/http"
	"strings"
	"time"
)

// handleVideoSubtitles serves the video's WebVTT captions from the row that
// owns them (migration 0023). 404 covers both "no video" and "video has no
// transcript" — and a video deleted to reclaim space keeps its captions, since
// a tombstone takes the media file and nothing else.
//
// The body is the stored text verbatim: the <track> element, the transcript
// panel's own parser and the user-facing .vtt download all want exactly what
// yt-dlp produced.
func (s *server) handleVideoSubtitles(w http.ResponseWriter, r *http.Request) {
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	serveTranscript(w, r, s, v.ID)
}

// serveTranscript writes one stored transcript, shared by the library and
// share-page endpoints so the two cannot drift.
func serveTranscript(w http.ResponseWriter, r *http.Request, s *server, videoID string) {
	if s.videos == nil {
		writeJSONError(w, http.StatusNotFound, "subtitles not available")
		return
	}
	t, err := s.videos.GetTranscript(videoID)
	if err != nil {
		serverError(w, r, err, "subtitles not available")
		return
	}
	if t == nil {
		writeJSONError(w, http.StatusNotFound, "no subtitles for this video")
		return
	}
	modTime, terr := time.Parse("2006-01-02 15:04:05", t.UpdatedAt)
	if terr != nil {
		modTime = time.Time{}
	}
	w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
	http.ServeContent(w, r, "captions.vtt", modTime, strings.NewReader(t.VTT))
}
