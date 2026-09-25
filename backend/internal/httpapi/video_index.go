package httpapi

import (
	"fmt"

	"github.com/trick77/peeq/internal/videos"
)

// idsOf collects one string per element, for the endpoints that batch-read
// the videos behind a list of job rows or hits.
func idsOf[T any](xs []T, id func(T) string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, id(x))
	}
	return out
}

// videoIndex reads the videos behind a list of ids in one batch, for the
// endpoints that join rows to a title and channel. With no video store the
// index is empty and the rows list untitled; a read failure is the caller's
// 500 — a list with every title blank and nothing saying why is the kind of
// silent half-answer #449 removed elsewhere.
func (s *server) videoIndex(ids []string) (map[string]*videos.Video, error) {
	if s.videos == nil || len(ids) == 0 {
		return map[string]*videos.Video{}, nil
	}
	index, err := s.videos.GetMany(ids)
	if err != nil {
		return nil, fmt.Errorf("video index: %w", err)
	}
	return index, nil
}
