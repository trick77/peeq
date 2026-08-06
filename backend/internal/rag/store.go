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
//
// Snippet is set only by the keyword lane, which knows which part of the chunk
// actually matched; the vector lane matches a chunk as a whole and leaves it
// empty for the caller to fall back to Text.
type Hit struct {
	VideoID      string
	Ordinal      int
	Text         string
	Kind         string
	StartSeconds int
	Distance     float64
	Snippet      string
}

// Matched terms inside a Snippet are delimited by these two ASCII control
// characters rather than by markup. FTS5's snippet() requires some start/end
// marker, and anything HTML-shaped would have to be either escaped downstream
// or rendered with dangerouslySetInnerHTML. STX/ETX cannot occur in subtitle
// text, survive JSON transport intact, and let the UI split the string into
// plain text nodes and <mark> elements — highlighting with no injection surface.
const (
	HighlightStart = "\x02"
	HighlightEnd   = "\x03"
)

// deleteVideoTx removes a video's rows from all three chunk tables
// (transcript_chunks, vec_chunks, fts_chunks) within tx. vec_chunks.rowid ==
// fts_chunks.rowid == transcript_chunks.id, so the ids are gathered first,
// then vec_chunks and fts_chunks are purged by rowid before transcript_chunks
// itself is deleted.
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

// IndexMeta describes the index a write produces: which model and dimension
// the vectors came from, and which content recipe the chunks follow. Rev
// travels with the write so a video can never be marked current under a recipe
// it was not actually built with.
type IndexMeta struct {
	Model string
	Dim   int
	Rev   int
}

// ReplaceVideoChunks atomically replaces a video's transcript chunks and
// embeddings and records the index metadata on the video row.
func (s *Store) ReplaceVideoChunks(ctx context.Context, videoID string, meta IndexMeta, rows []ChunkRow, vectors [][]float32) error {
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
	if _, err := tx.ExecContext(ctx,
		`UPDATE videos SET embed_model = ?, embed_dim = ?, embed_rev = ? WHERE id = ?`,
		meta.Model, meta.Dim, meta.Rev, videoID); err != nil {
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

// HasChunks reports whether this video is indexed at all.
//
// It answers a question about consequences rather than about settings: deleting
// a video row takes its chunks with it on the cascade, so a caller about to do
// that can ask here whether anything searchable would go with it. Reading the
// channel's keep_reads instead would give a different answer depending on when
// the switch was last flipped, and take results away that the user can neither
// see nor account for.
func (s *Store) HasChunks(ctx context.Context, videoID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM transcript_chunks WHERE video_id = ?)`, videoID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("rag: has chunks %s: %w", videoID, err)
	}
	return n != 0, nil
}

// KindCount is how many chunks of one kind a video contributed to the index,
// and how much text they carry.
type KindCount struct {
	Kind   string
	Count  int
	Tokens int
}

// ChunkStats reports a video's chunks grouped by kind, newest recipe first in
// no particular order — the caller sorts for display.
//
// Grouped rather than three named counters because the kinds are a pipeline
// detail (transcript, summary, chapter today; BuildVideoChunks decides), and a
// fourth one added there should show up here without a schema change on the way
// out. A video that was never indexed returns no rows, not an error: "nothing
// indexed" is an answer, and the caller renders it as one.
func (s *Store) ChunkStats(ctx context.Context, videoID string) ([]KindCount, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT kind, COUNT(*), COALESCE(SUM(token_count), 0)
		   FROM transcript_chunks WHERE video_id = ? GROUP BY kind`, videoID)
	if err != nil {
		return nil, fmt.Errorf("rag: chunk stats %s: %w", videoID, err)
	}
	defer rows.Close()
	var out []KindCount
	for rows.Next() {
		var k KindCount
		if err := rows.Scan(&k.Kind, &k.Count, &k.Tokens); err != nil {
			return nil, fmt.Errorf("rag: chunk stats %s: %w", videoID, err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rag: chunk stats %s: %w", videoID, err)
	}
	return out, nil
}

// Retrieve returns up to k chunks nearest to queryEmbedding across all videos.
//
// Note this is an unbounded KNN: it returns k rows for ANY query vector, however
// unrelated, because "nearest" is relative. Callers that surface results to a
// user want RetrieveWithin so a query the library has nothing to say about comes
// back empty instead of full of the least-distant noise.
func (s *Store) Retrieve(ctx context.Context, queryEmbedding []float32, k int) ([]Hit, error) {
	return s.RetrieveWithin(ctx, queryEmbedding, k, 0)
}

// RetrieveWithin returns up to k chunks nearest to queryEmbedding whose
// distance is below maxDistance. A non-positive maxDistance disables the bound.
//
// The cutoff sits on the KNN's output, not inside the KNN itself: vec0 still
// picks its k nearest rows and the bound then drops the far ones, so a query
// with few close chunks legitimately returns fewer than k. Doing it in SQL
// rather than in Go only saves shipping those rows' text across the driver — it
// does not buy back the KNN budget the far rows already consumed.
func (s *Store) RetrieveWithin(ctx context.Context, queryEmbedding []float32, k int, maxDistance float64) ([]Hit, error) {
	return s.RetrieveWithinFiltered(ctx, queryEmbedding, k, maxDistance, Filter{})
}

// vecKMax is vec0's own ceiling on the `k = ?` constraint
// (SQLITE_VEC_VEC0_K_MAX in sqlite-vec.c). Exceeding it is an error from the
// vtab, so k is clamped rather than passed through.
const vecKMax = 4096

// RetrieveWithinFiltered is RetrieveWithin scoped to the videos f admits.
//
// The two bounds in this query are not the same kind of thing, and confusing
// them is the mistake this comment exists to prevent:
//
//   - maxDistance is a POST-filter. vec0 picks its k nearest rows and the bound
//     then drops the far ones, exactly as RetrieveWithin documents.
//   - the filter is a PRE-filter. vec0 treats `rowid IN (...)` as a first-class
//     KNN constraint: it bitmap-ANDs the candidate rowids into the validity mask
//     BEFORE computing distances, so k applies to the filtered set. "The 40
//     nearest chunks among Veritasium's", not "the 40 nearest overall, of which
//     some happen to be Veritasium's".
//
// That distinction is the whole feature. Post-filtering would return nothing for
// any channel holding a small slice of the library, which is most of them.
// TestRetrieveWithinFilteredIsAPreFilter pins it: it builds a fixture whose
// global top-40 is entirely one channel and asserts the other channel's chunk
// still comes back.
func (s *Store) RetrieveWithinFiltered(ctx context.Context, queryEmbedding []float32, k int, maxDistance float64, f Filter) ([]Hit, error) {
	if k <= 0 {
		k = 10
	}
	if k > vecKMax {
		k = vecKMax
	}
	// vec0 requires the `k = ?` constraint to sit alongside the MATCH; the
	// distance bound is an ordinary predicate applied to the KNN output.
	inner := `SELECT c.video_id, c.ordinal, c.text, c.kind, c.start_seconds, v.distance
			FROM vec_chunks v
			JOIN transcript_chunks c ON c.id = v.rowid
			WHERE v.embedding MATCH ? AND k = ?`
	args := []any{store.VecLiteral(queryEmbedding), k}
	if !f.Empty() {
		sub, subArgs := f.chunkSubquery()
		inner += "\n\t\t\t  AND v.rowid IN (" + sub + ")"
		args = append(args, subArgs...)
	}
	q := `
		SELECT video_id, ordinal, text, kind, start_seconds, distance FROM (` + inner + `)
		WHERE ? <= 0 OR distance < ?
		ORDER BY distance`
	args = append(args, maxDistance, maxDistance)
	rows, err := s.db.QueryContext(ctx, q, args...)
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

// SearchFTS returns up to n chunks whose text matches the FTS5 expression,
// best (lowest bm25) first. match must already be sanitized by BuildFTSMatch;
// an empty match yields no hits without touching the DB. Distance is left 0 —
// FTS rank is positional, and the caller fuses by rank, not score.
//
// bm25() must reference the FTS5 table by its real name, not the "f" alias
// (SQLite resolves it as a hidden column lookup on the vtab itself, and
// aliases aren't recognized there) — verified empirically against the
// sqlite3 CLI (bm25(f) errors "no such column: f"; bm25(fts_chunks) works).
func (s *Store) SearchFTS(ctx context.Context, match string, n int) ([]Hit, error) {
	return s.SearchFTSFiltered(ctx, match, n, Filter{})
}

// SearchFTSFiltered is SearchFTS scoped to the videos f admits.
//
// No vec0-style care is needed here: FTS5 is not a top-k operator, so joining
// videos and constraining it in the WHERE happens before ORDER BY bm25 … LIMIT
// ever runs. This is a pre-filter by construction.
func (s *Store) SearchFTSFiltered(ctx context.Context, match string, n int, f Filter) ([]Hit, error) {
	if strings.TrimSpace(match) == "" {
		return nil, nil
	}
	if n <= 0 {
		n = 10
	}
	// snippet() returns the window of the chunk AROUND the match, with the
	// matched terms delimited. Taking the head of the chunk instead — as this
	// did before — shows a preview that usually does not contain the searched
	// word at all: a chunk is ~600 tokens and a hit is rarely in its first
	// line. Column 0 is fts_chunks' only column; 32 is the token budget for the
	// window (FTS5 caps it at 64).
	q := `
		SELECT c.video_id, c.ordinal, c.text, c.kind, c.start_seconds,
		       snippet(fts_chunks, 0, ?, ?, '…', 32)
		FROM fts_chunks f
		JOIN transcript_chunks c ON c.id = f.rowid`
	args := []any{HighlightStart, HighlightEnd}
	where := []string{"f.text MATCH ?"}
	if !f.Empty() {
		q += "\n\t\tJOIN videos fv ON fv.id = c.video_id"
	}
	args = append(args, match)
	if !f.Empty() {
		conds, fargs := f.predicates("fv")
		where = append(where, conds...)
		args = append(args, fargs...)
	}
	q += "\n\t\tWHERE " + strings.Join(where, " AND ") + `
		ORDER BY bm25(fts_chunks)
		LIMIT ?`
	args = append(args, n)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.VideoID, &h.Ordinal, &h.Text, &h.Kind, &h.StartSeconds, &h.Snippet); err != nil {
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
