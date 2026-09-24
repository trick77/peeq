package channelvideos

import (
	"context"
	"fmt"

	"github.com/trick77/peeq/internal/store"
)

// inChunk is how many ids one IN (...) list carries. SQLite's default bound
// on host parameters is far higher, but a scan lists a few dozen ids per tab,
// so one chunk is the ordinary case and the cap only guards a pathological
// listing.
const inChunk = 500

// GetMany returns the ledger rows for ids, keyed by video id; ids with no row
// are simply absent. It replaces one Get per listed entry in the scan loop —
// a few hundred statements per channel per pass — with one query per chunk.
// An empty input runs no query.
func (s *Store) GetMany(ids []string) (map[string]*Entry, error) {
	out := make(map[string]*Entry, len(ids))
	for start := 0; start < len(ids); start += inChunk {
		end := min(start+inChunk, len(ids))
		if err := s.getChunk(ids[start:end], out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) getChunk(chunk []string, out map[string]*Entry) error {
	args := make([]any, len(chunk))
	for i, id := range chunk {
		args[i] = id
	}
	// The only dynamic part of the statement is the placeholder list; every
	// id travels as a bound parameter.
	query := `SELECT ` + selectColumns + ` FROM channel_videos WHERE video_id IN (` + store.Placeholders(len(chunk)) + `)` //nolint:gosec // placeholders only, ids are bound
	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return fmt.Errorf("get channel videos: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		e, err := scanRow(rows)
		if err != nil {
			return fmt.Errorf("scan channel video: %w", err)
		}
		out[e.VideoID] = &e
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate channel videos: %w", err)
	}
	return nil
}
