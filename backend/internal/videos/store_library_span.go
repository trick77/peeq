package videos

import (
	"context"
	"fmt"
)

// librarySpanSQL reads every row from idx_videos_cards, which holds both
// columns: no video row is opened. The span is published_at alone, not
// rag.Filter's COALESCE with created_at: scan-seeded rows carry no release
// date on purpose, and the day peeq recorded one is no release date.
const librarySpanSQL = `
SELECT COUNT(*),
       COALESCE(MIN(NULLIF(v.published_at, '')), ''),
       COALESCE(MAX(NULLIF(v.published_at, '')), '')
FROM videos v INDEXED BY idx_videos_cards`

// LibrarySpan counts every video peeq holds, whatever its status, and the
// release dates of those that have one.
func (s *Store) LibrarySpan(ctx context.Context) (LibrarySpan, error) {
	var out LibrarySpan
	if err := s.db.QueryRowContext(ctx, librarySpanSQL).Scan(&out.Videos, &out.Oldest, &out.Newest); err != nil {
		return LibrarySpan{}, fmt.Errorf("library span: %w", err)
	}
	return out, nil
}
