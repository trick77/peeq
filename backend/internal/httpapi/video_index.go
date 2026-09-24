package httpapi

import (
	"log/slog"

	"github.com/trick77/peeq/internal/videos"
)

// videoIndex reads the videos behind a list of ids in one batch, for the
// endpoints that join job rows to a title and channel. Nil-safe: with no
// video store, or on a read failure (logged), it returns what it has, and the
// list degrades to untitled rows rather than failing — the queue is about
// the jobs, and a title is decoration.
func (s *server) videoIndex(ids []string) map[string]*videos.Video {
	if s.videos == nil || len(ids) == 0 {
		return map[string]*videos.Video{}
	}
	index, err := s.videos.GetMany(ids)
	if err != nil {
		slog.Warn("video index read failed", "count", len(ids), "err", err)
		return map[string]*videos.Video{}
	}
	return index
}
