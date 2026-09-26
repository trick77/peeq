package rag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The vec_model row names the embedding model every vector in vec_chunks came
// from (migration 0031). Same-width models embed into different spaces, so the
// width check alone cannot catch a swap; this can.

// RecordedEmbedModel returns the model recorded for the stored vectors, or ""
// when none is recorded yet.
func (s *Store) RecordedEmbedModel(ctx context.Context) (string, error) {
	return recordedEmbedModel(ctx, s.db)
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func recordedEmbedModel(ctx context.Context, q queryer) (string, error) {
	var m string
	err := q.QueryRowContext(ctx, `SELECT model FROM vec_model WHERE id = 1`).Scan(&m)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("rag: read vec_model: %w", err)
	}
	return m, nil
}

// CheckEmbedModel refuses a configured embedding model other than the one the
// stored vectors came from, width notwithstanding.
//
// With no vectors stored there is nothing to mismatch: the configured model is
// recorded, replacing any earlier one, so an empty library (new, or emptied by
// deletes) may switch to another model of the same width and only a write
// that stores vectors pins one. The width itself is fixed by the vec_chunks
// migration and checked separately (CheckVecWidth), empty table or not. With vectors but nothing recorded it records a model first: the one the
// indexed videos name when they all name the same one (a database indexed
// before the record existed), otherwise the configured one (a history that
// cannot say).
func (s *Store) CheckEmbedModel(ctx context.Context, model string) error {
	var hasVectors bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM transcript_chunks)`).Scan(&hasVectors); err != nil {
		return fmt.Errorf("rag: count stored vectors: %w", err)
	}
	if !hasVectors {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO vec_model (id, model) VALUES (1, ?)
			ON CONFLICT(id) DO UPDATE SET model = excluded.model`, model); err != nil {
			return fmt.Errorf("rag: record vec_model: %w", err)
		}
		return nil
	}
	stored, err := s.RecordedEmbedModel(ctx)
	if err != nil {
		return err
	}
	if stored == "" {
		if stored, err = s.adoptEmbedModel(ctx, model); err != nil {
			return err
		}
	}
	if stored == model {
		return nil
	}
	return fmt.Errorf("vec_chunks holds vectors from %s, but BACKEND_EMBED_MODEL=%s; "+
		"either set BACKEND_EMBED_MODEL back to %s, or re-index: ship a migration that clears "+
		"vec_chunks and vec_model and sets embed_rev = 0 on every video, so each is re-embedded", stored, model, stored)
}

// adoptEmbedModel records the model for a database that has none recorded.
func (s *Store) adoptEmbedModel(ctx context.Context, configured string) (string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT embed_model FROM videos
		WHERE embed_model != '' AND id IN (SELECT video_id FROM transcript_chunks)`)
	if err != nil {
		return "", fmt.Errorf("rag: read indexed models: %w", err)
	}
	var models []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			_ = rows.Close()
			return "", err
		}
		models = append(models, m)
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	adopted := configured
	if len(models) == 1 {
		adopted = models[0]
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO vec_model (id, model) VALUES (1, ?) ON CONFLICT(id) DO NOTHING`, adopted); err != nil {
		return "", fmt.Errorf("rag: record vec_model: %w", err)
	}
	return s.RecordedEmbedModel(ctx)
}

// recordEmbedModelTx records model for the vectors a write adds, refusing a
// model other than the one already recorded so two vector spaces never mix.
func recordEmbedModelTx(ctx context.Context, tx *sql.Tx, model string) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO vec_model (id, model) VALUES (1, ?) ON CONFLICT(id) DO NOTHING`, model); err != nil {
		return fmt.Errorf("rag: record vec_model: %w", err)
	}
	stored, err := recordedEmbedModel(ctx, tx)
	if err != nil {
		return err
	}
	if stored != model {
		return fmt.Errorf("rag: vectors from %s cannot join vec_chunks, which holds %s", model, stored)
	}
	return nil
}
