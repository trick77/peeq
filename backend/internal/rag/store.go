package rag

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/trick77/peeq/internal/store"
)

// Store persists transcript chunks and their embeddings and retrieves nearest
// neighbors. Peeq is single-user, so there is no scope. vec0 forbids triggers/FK
// cascades, so every delete of transcript_chunks also deletes the matching
// vec_chunks rows in the SAME transaction.
type Store struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// ChunkRow is one transcript window to index.
type ChunkRow struct {
	Ordinal      int
	Text         string
	Kind         string
	StartSeconds int
	TokenCount   int
}

// Hit is one retrieved chunk with its cosine/L2 distance (smaller == closer).
type Hit struct {
	VideoID      string
	Ordinal      int
	Text         string
	Kind         string
	StartSeconds int
	Distance     float64
}

// deleteVideoTx removes a video's chunk + vec rows within tx. vec_chunks.rowid ==
// transcript_chunks.id, so the ids are gathered first, then both tables purged.
func deleteVideoTx(ctx context.Context, tx *sql.Tx, videoID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM transcript_chunks WHERE video_id = ?`, videoID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM vec_chunks WHERE rowid = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM fts_chunks WHERE rowid = ?`, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM transcript_chunks WHERE video_id = ?`, videoID); err != nil {
		return err
	}
	return nil
}

// ReplaceVideoChunks atomically replaces a video's transcript chunks and
// embeddings and records the embedding model/dim on the video row.
func (s *Store) ReplaceVideoChunks(ctx context.Context, videoID, model string, dim int, rows []ChunkRow, vectors [][]float32) error {
	if len(rows) != len(vectors) {
		return fmt.Errorf("rag: %d rows but %d vectors", len(rows), len(vectors))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteVideoTx(ctx, tx, videoID); err != nil {
		return err
	}
	for i, r := range rows {
		kind := r.Kind
		if kind == "" {
			kind = "transcript"
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO transcript_chunks (video_id, ordinal, text, kind, start_seconds, token_count) VALUES (?,?,?,?,?,?)`,
			videoID, r.Ordinal, r.Text, kind, r.StartSeconds, r.TokenCount)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vec_chunks (rowid, embedding) VALUES (?, ?)`, id, store.VecLiteral(vectors[i])); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO fts_chunks (rowid, text) VALUES (?, ?)`, id, r.Text); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE videos SET embed_model = ?, embed_dim = ? WHERE id = ?`, model, dim, videoID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteVideoChunks removes a video's chunks + embeddings (used on full delete).
func (s *Store) DeleteVideoChunks(ctx context.Context, videoID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteVideoTx(ctx, tx, videoID); err != nil {
		return err
	}
	return tx.Commit()
}

// Retrieve returns up to k chunks nearest to queryEmbedding across all videos.
func (s *Store) Retrieve(ctx context.Context, queryEmbedding []float32, k int) ([]Hit, error) {
	if k <= 0 {
		k = 10
	}
	const q = `
		SELECT c.video_id, c.ordinal, c.text, c.kind, c.start_seconds, v.distance
		FROM vec_chunks v
		JOIN transcript_chunks c ON c.id = v.rowid
		WHERE v.embedding MATCH ? AND k = ?
		ORDER BY v.distance`
	rows, err := s.db.QueryContext(ctx, q, store.VecLiteral(queryEmbedding), k)
	if err != nil {
		return nil, fmt.Errorf("retrieve: %w", err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.VideoID, &h.Ordinal, &h.Text, &h.Kind, &h.StartSeconds, &h.Distance); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// BuiltDim reports the dimension the vec_chunks table was created with, so the
// boot reconcile can detect an embed-model/dim change that invalidates it.
//
// pragma_table_info('vec_chunks') reports an empty "type" for vec0 virtual
// table columns on this sqlite-vec build (verified against v0.1.7-alpha.2),
// so the dimension can't come from there. It IS present, however, in the
// table's original DDL text (e.g. "embedding float[1536]"), which sqlite
// keeps verbatim in sqlite_master.sql — parse the bracketed dimension out of
// that instead.
func (s *Store) BuiltDim(ctx context.Context) (int, error) {
	var ddl string
	err := s.db.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'vec_chunks'`).Scan(&ddl)
	if err != nil {
		return 0, fmt.Errorf("rag: read vec_chunks schema: %w", err)
	}
	open := strings.IndexByte(ddl, '[')
	close := strings.IndexByte(ddl, ']')
	if open < 0 || close <= open {
		return 0, fmt.Errorf("rag: could not determine vec_chunks dimension from schema %q", ddl)
	}
	return strconv.Atoi(ddl[open+1 : close])
}
