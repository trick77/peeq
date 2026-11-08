package rag

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/store"
)

// Store persists transcript chunks and their embeddings and retrieves nearest
// neighbors. Peeq is single-user, so there is no scope. vec0 forbids triggers/FK
// cascades, so every delete of transcript_chunks also deletes the matching
// vec_chunks rows in the SAME transaction.
type Store struct {
	db *sql.DB
}

// NewStore returns a RAG store backed by db.
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

// chunkIDsTx returns a video's chunk ids with their text, oldest first.
func chunkIDsTx(ctx context.Context, tx *sql.Tx, videoID string) (ids []int64, texts []string, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, text FROM transcript_chunks WHERE video_id = ? ORDER BY id`, videoID)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var text string
		if err := rows.Scan(&id, &text); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		texts = append(texts, text)
	}
	return ids, texts, rows.Err()
}

// DeleteChunksTx removes the vector and keyword rows of the given chunk ids
// within tx. vec_chunks.rowid == fts_chunks.rowid == transcript_chunks.id, and
// neither virtual table can ride a foreign-key cascade, so whoever deletes
// transcript_chunks rows (here, or by deleting their videos) calls this first.
//
// One row at a time on purpose: vec0 answers `rowid = ?` with a point lookup
// and `rowid IN (...)` with a scan of every chunk (see copyOutSelect).
func DeleteChunksTx(ctx context.Context, tx *sql.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	delVec, err := tx.PrepareContext(ctx, `DELETE FROM vec_chunks WHERE rowid = ?`)
	if err != nil {
		return err
	}
	defer func() { _ = delVec.Close() }()
	delFTS, err := tx.PrepareContext(ctx, `DELETE FROM fts_chunks WHERE rowid = ?`)
	if err != nil {
		return err
	}
	defer func() { _ = delFTS.Close() }()
	for _, id := range ids {
		if _, err := delVec.ExecContext(ctx, id); err != nil {
			return err
		}
		if _, err := delFTS.ExecContext(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// DeleteVideoTx removes a video's rows from all three chunk tables within tx
// and reports how many chunks went. A caller deleting the videos row in the
// same tx (videos.Store.Purge) calls it first: the cascade would take
// transcript_chunks and strand the vec/fts rows.
func DeleteVideoTx(ctx context.Context, tx *sql.Tx, videoID string) (int, error) {
	ids, _, err := chunkIDsTx(ctx, tx, videoID)
	if err != nil {
		return 0, err
	}
	if err := DeleteChunksTx(ctx, tx, ids); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM transcript_chunks WHERE video_id = ?`, videoID); err != nil {
		return 0, err
	}
	return len(ids), nil
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

// ReusableTexts returns the chunk texts already stored for a video with
// vectors from model, each with how many chunks carry it. A caller about to
// re-index the video embeds only the texts not in here and passes a nil vector
// for the rest (see ReplaceVideoChunks).
//
// Empty when the stored vectors came from another model: the same text under a
// different model is a different vector.
func (s *Store) ReusableTexts(ctx context.Context, videoID, model string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT tc.text FROM transcript_chunks tc
JOIN videos v ON v.id = tc.video_id
WHERE tc.video_id = ? AND v.embed_model = ?`, videoID, model)
	if err != nil {
		return nil, fmt.Errorf("rag: reusable texts %s: %w", videoID, err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, fmt.Errorf("rag: reusable texts %s: %w", videoID, err)
		}
		out[text]++
	}
	return out, rows.Err()
}

// ReplaceVideoChunks atomically makes rows the video's chunks and records the
// index metadata on the video row.
//
// vectors[i] is row i's embedding, or nil to keep the vector already stored
// for a chunk with the same text. A kept chunk stays under its rowid and has
// only its position updated; chunks no row claims are deleted. That is what
// keeps a re-index of unchanged text from growing vec_chunks: vec0 never frees
// a deleted vector, so replacing every row wholesale left a dead copy of the
// video's vectors behind each time (see CompactVectors). A nil vector with no
// stored chunk to keep is an error and writes nothing.
func (s *Store) ReplaceVideoChunks(ctx context.Context, videoID string, meta IndexMeta, rows []ChunkRow, vectors [][]float32) error {
	if len(rows) != len(vectors) {
		return fmt.Errorf("rag: %d rows but %d vectors", len(rows), len(vectors))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if len(rows) > 0 {
		if err := recordEmbedModelTx(ctx, tx, meta.Model); err != nil {
			return err
		}
	}
	ids, texts, err := chunkIDsTx(ctx, tx, videoID)
	if err != nil {
		return err
	}
	// The stored chunks a nil-vector row may claim, by text, oldest first.
	free := make(map[string][]int64, len(ids))
	for i, id := range ids {
		free[texts[i]] = append(free[texts[i]], id)
	}
	kept := make([]int64, len(rows)) // 0 = insert
	for i, r := range rows {
		if vectors[i] != nil {
			continue
		}
		candidates := free[r.Text]
		if len(candidates) == 0 {
			return fmt.Errorf("rag: chunk %d of %s has no vector and no stored chunk to keep", r.Ordinal, videoID)
		}
		kept[i], free[r.Text] = candidates[0], candidates[1:]
	}
	var stale []int64
	for _, id := range ids {
		if !slices.Contains(kept, id) {
			stale = append(stale, id)
		}
	}
	if err := DeleteChunksTx(ctx, tx, stale); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM transcript_chunks WHERE id = ?`, id); err != nil {
			return err
		}
	}
	for i, r := range rows {
		kind := r.Kind
		if kind == "" {
			kind = "transcript"
		}
		if kept[i] != 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE transcript_chunks SET ordinal = ?, kind = ?, start_seconds = ?, token_count = ? WHERE id = ?`,
				r.Ordinal, kind, r.StartSeconds, r.TokenCount, kept[i]); err != nil {
				return err
			}
			continue
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
	if err := tx.Commit(); err != nil {
		return err
	}
	if len(stale) > 0 {
		// Deleted vectors keep their storage (CompactVectors).
		WarnVectorBloat(ctx, s.db, slog.Default(), "video_id", videoID)
	}
	return nil
}

// DeleteVideoChunks removes a video's chunks + embeddings (used on full delete).
func (s *Store) DeleteVideoChunks(ctx context.Context, videoID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	deleted, err := DeleteVideoTx(ctx, tx, videoID)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if deleted > 0 {
		WarnVectorBloat(ctx, s.db, slog.Default(), "video_id", videoID)
	}
	return nil
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
	defer func() { _ = rows.Close() }()
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
	start := time.Now()
	out, err := s.scanVector(ctx, q, args)
	elapsed := time.Since(start).Round(time.Millisecond)
	if err != nil {
		// sqlite-vec reports an interrupt as "SQL logic error: chunks iter
		// error", which reads as corruption. Name the cancel and how long the
		// search had run before it.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("retrieve: vector search interrupted after %s (%w): %w", elapsed, context.Cause(ctx), err)
		}
		return nil, fmt.Errorf("retrieve: %w", err)
	}
	if elapsed >= slowVectorSearch {
		slog.Warn("slow vector search", "k", k, "filtered", !f.Empty(), "hits", len(out), "took", elapsed.String())
	}
	return out, nil
}

// slowVectorSearch is the duration past which a vector search is logged. A
// healthy one takes milliseconds; one reading a bloated vec_chunks takes
// minutes (CompactVectors).
var slowVectorSearch = 2 * time.Second

func (s *Store) scanVector(ctx context.Context, q string, args []any) ([]Hit, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
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

// SearchFTSFiltered is SearchFTS scoped to the videos f admits, filtered before
// LIMIT like the vector lane (see ftsQuery).
func (s *Store) SearchFTSFiltered(ctx context.Context, match string, n int, f Filter) ([]Hit, error) {
	if strings.TrimSpace(match) == "" {
		return nil, nil
	}
	if n <= 0 {
		n = 10
	}
	q, args := ftsQuery(match, n, f)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer func() { _ = rows.Close() }()
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

// ftsQuery renders the ranked keyword query.
//
// ORDER BY rank (bm25, no rank override is configured) is consumed by FTS5
// itself: it still scores every match internally, but it hands rows out
// ranked, so the chunk join and snippet() run only for the n rows LIMIT keeps.
// ORDER BY bm25(fts_chunks) was not consumed: SQLite sorted every match in a
// temp b-tree, joining and computing snippet() for each first. Measured on the
// production DB with the floor of the query that timed out: 17.5 s before,
// 1.6 s after, cold. Scoring every match is still the floor's cost, which is
// why askLanes keeps a budget on it.
//
// CROSS JOIN pins fts_chunks as the outer loop, which is what keeps the order
// FTS5's; the join drops an fts row with no chunk before LIMIT counts it.
//
// The filter is the vector lane's id set (chunkSubquery), built once per query
// from indexes, probed per ranked row before LIMIT counts it: a pre-filter.
// Not a join against videos: a narrow filter would then read a videos row per
// match walked, and its small columns sit behind description and analysis
// text (overflow pages, on 7200rpm disks). +rowid keeps the IN out of FTS5's
// xBestIndex: handed to FTS5 it becomes one unranked rowid= lookup per admitted
// chunk, and the temp b-tree comes back.
//
// snippet() returns the window AROUND the match, with the matched terms
// delimited; the head of a ~600-token chunk rarely holds the searched word.
// Column 0 is fts_chunks' only column; 32 is the token budget for the window
// (FTS5 caps it at 64).
func ftsQuery(match string, n int, f Filter) (string, []any) {
	q := `
		SELECT c.video_id, c.ordinal, c.text, c.kind, c.start_seconds,
		       snippet(fts_chunks, 0, ?, ?, '…', 32)
		FROM fts_chunks
		CROSS JOIN transcript_chunks c ON c.id = fts_chunks.rowid
		WHERE fts_chunks MATCH ?`
	args := []any{HighlightStart, HighlightEnd, match}
	if !f.Empty() {
		sub, subArgs := f.chunkSubquery()
		q += "\n\t\t  AND +fts_chunks.rowid IN (" + sub + ")"
		args = append(args, subArgs...)
	}
	// Qualified: an unqualified rank would turn ambiguous, or bind to another
	// table's column, the day a joined table grows one.
	q += "\n\t\tORDER BY fts_chunks.rank\n\t\tLIMIT ?"
	return q, append(args, n)
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
