package rag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// The vec_model row names the embedding model every vector in vec_chunks came
// from (migration 0035). Same-width models embed into different spaces, so the
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
// recorded, replacing any earlier one, so an empty library (new, emptied by
// deletes, or re-indexed by a migration that cleared vec_chunks and vec_model)
// may switch to another model of the same width and only a write that stores
// vectors pins one. Vectors are counted in vec_chunks itself, never by
// transcript_chunks: a re-index keeps the chunk rows and drops only their
// vectors. The width is fixed by the vec_chunks migration and checked
// separately (CheckVecWidth), empty table or not.
func (s *Store) CheckEmbedModel(ctx context.Context, model string) error {
	var hasVectors bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM vec_chunks_rowids)`).Scan(&hasVectors); err != nil {
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
		// Every vector write records its model in the same transaction, so this
		// is a migration that cleared vec_model but left the vectors. Guessing a
		// model would let two vector spaces mix.
		return errors.New("vec_chunks holds vectors but vec_model names no model; " +
			"re-index: ship a migration that clears vec_chunks and vec_model and sets embed_rev = 0 " +
			"on every video, so each is re-embedded")
	}
	if stored == model {
		return nil
	}
	return fmt.Errorf("vec_chunks holds vectors from %s, but BACKEND_EMBED_MODEL=%s; "+
		"either set BACKEND_EMBED_MODEL back to %s, or re-index: ship a migration that clears "+
		"vec_chunks and vec_model and sets embed_rev = 0 on every video, so each is re-embedded", stored, model, stored)
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
