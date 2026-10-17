package channels

import (
	"context"
	"fmt"
)

// SearchableChannel is a channel whose videos the MCP server offers: the
// narrow projection the channel hint and list_channels read.
type SearchableChannel struct {
	ID          string
	Handle      string
	Name        string
	Description string
	Subscribed  bool
	KeepReads   bool
}

// Searchable lists the subscribed channels plus every channel whose reads
// are kept, ordered by display name, under List's own base predicate (added
// or has downloads) so a removed or cache-only row never shows. Unlike List it computes no counts and
// touches no image or ledger table: it runs on every MCP discovery.
func (s *Store) Searchable(ctx context.Context) ([]SearchableChannel, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.handle, c.name, c.description, s.channel_id IS NOT NULL, c.keep_reads
FROM channels c
LEFT JOIN subscriptions s ON s.channel_id = c.id
WHERE (c.added_at IS NOT NULL OR ` + hasDownloadsPredicate + `)
  AND (s.channel_id IS NOT NULL OR c.keep_reads = 1)
ORDER BY COALESCE(NULLIF(c.name, ''), NULLIF(c.handle, ''), c.id) COLLATE NOCASE, c.id`)
	if err != nil {
		return nil, fmt.Errorf("searchable channels: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []SearchableChannel
	for rows.Next() {
		var c SearchableChannel
		if err := rows.Scan(&c.ID, &c.Handle, &c.Name, &c.Description, &c.Subscribed, &c.KeepReads); err != nil {
			return nil, fmt.Errorf("searchable channels: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("searchable channels: %w", err)
	}
	return out, nil
}
