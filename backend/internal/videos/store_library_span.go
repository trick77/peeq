package videos

import (
	"context"
	"fmt"
)

// librarySpanSQL reads every row from idx_videos_cards, which holds all three
// columns: no video row is opened. The release date is rag.Filter's and
// Browse's (a dateless row counts by the day it was recorded), so the span
// matches what search's and list_videos' after/before can reach.
const librarySpanSQL = `
SELECT COUNT(*),
       COALESCE(MIN(COALESCE(v.published_at, date(v.created_at))), ''),
       COALESCE(MAX(COALESCE(v.published_at, date(v.created_at))), '')
FROM videos v INDEXED BY idx_videos_cards`

// LibrarySpan counts every video peeq holds, whatever its status, and the
// release dates they span.
func (s *Store) LibrarySpan(ctx context.Context) (LibrarySpan, error) {
	var out LibrarySpan
	if err := s.db.QueryRowContext(ctx, librarySpanSQL).Scan(&out.Videos, &out.Oldest, &out.Newest); err != nil {
		return LibrarySpan{}, fmt.Errorf("library span: %w", err)
	}
	return out, nil
}
