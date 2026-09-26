package rag

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/store"
)

const compactDim = 4

// vecDB is a database holding only a narrow vec_chunks: the compaction reads
// nothing else, and 3000 1536-wide vectors would make every test slow.
func vecDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE VIRTUAL TABLE vec_chunks USING vec0(embedding float[4])`); err != nil {
		t.Fatal(err)
	}
	return db
}

func unit(marker float32) []float32 { return unitOf(compactDim, marker) }

func unitOf(dim int, marker float32) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = marker
	}
	return v
}

// churnVectors inserts n vectors and deletes all but the last keep, the way a
// re-embed does: vec0 clears a deleted row's validity bit and never frees its
// chunk, and inserts only append to the newest chunk.
func churnVectors(t *testing.T, db *sql.DB, n, keep int) { churnVectorsOf(t, db, compactDim, n, keep) }

func churnVectorsOf(t *testing.T, db *sql.DB, dim, n, keep int) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		if _, err := tx.Exec(`INSERT INTO vec_chunks (rowid, embedding) VALUES (?, ?)`,
			i, store.VecLiteral(unitOf(dim, float32(i)))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM vec_chunks WHERE rowid <= ?`, n-keep); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, db *sql.DB, q string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func nearestIDs(t *testing.T, db *sql.DB, marker float32, k int) []int64 {
	t.Helper()
	rows, err := db.Query(`SELECT rowid FROM vec_chunks WHERE embedding MATCH ? AND k = ? ORDER BY distance`,
		store.VecLiteral(unit(marker)), k)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// logTo points a logger at a text buffer.
func logTo() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

func wantAll(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("log = %q, want %q", got, w)
		}
	}
}

func TestCompactVectors_dropsTheChunksDeletesLeftBehind(t *testing.T) {
	db := vecDB(t)
	churnVectors(t, db, 3000, 100)
	before := nearestIDs(t, db, 2950, 10)

	got, err := CompactVectors(context.Background(), db)
	if err != nil {
		t.Fatalf("CompactVectors() err = %v", err)
	}

	want := VecCompaction{Compacted: true, Rows: 100, ChunksBefore: 3, ChunksAfter: 1}
	if got != want {
		t.Errorf("CompactVectors() = %+v, want %+v", got, want)
	}
	// Same rowids, same vectors: search joins vec_chunks to transcript_chunks
	// on rowid.
	if after := nearestIDs(t, db, 2950, 10); !slices.Equal(after, before) {
		t.Errorf("nearest after compaction = %v, want %v", after, before)
	}
	var ddl string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'vec_chunks'`).Scan(&ddl); err != nil ||
		!strings.Contains(ddl, "float[4]") {
		t.Errorf("ddl = %q, %v, want the table rebuilt as it was", ddl, err)
	}
}

func TestCompactVectors_leavesAHealthyTableAlone(t *testing.T) {
	db := vecDB(t)
	churnVectors(t, db, 1500, 1400)

	got, err := CompactVectors(context.Background(), db)
	if err != nil {
		t.Fatalf("CompactVectors() err = %v", err)
	}
	if got.Compacted || got.ChunksBefore != 2 || got.Rows != 1400 {
		t.Errorf("CompactVectors() = %+v, want untouched with 2 chunks and 1400 rows", got)
	}
}

func TestCompactVectors_anEmptyTableIsNotBloated(t *testing.T) {
	db := vecDB(t)
	if got, err := CompactVectors(context.Background(), db); err != nil || got.Compacted {
		t.Errorf("CompactVectors() = %+v, %v, want untouched", got, err)
	}
}

func TestCompactVectors_aFailureRollsBackWhole(t *testing.T) {
	db := vecDB(t)
	churnVectors(t, db, 3000, 100)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := CompactVectors(ctx, db); err == nil {
		t.Fatal("CompactVectors() on a cancelled context succeeded")
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM vec_chunks_rowids`); n != 100 {
		t.Errorf("rows = %d, want 100", n)
	}
}

func TestLogVectorIndexAtBoot_compactsAndVacuumsABloatedTable(t *testing.T) {
	db := vecDB(t)
	churnVectors(t, db, 3000, 100)
	log, out := logTo()

	LogVectorIndexAtBoot(context.Background(), db, log)

	wantAll(t, out.String(), `msg="vector index compacted"`, "reason=boot", "rows=100",
		"chunks_before=3", "chunks_after=1", "took=", `msg="database vacuumed"`, "bytes_before=", "bytes_after=")
	if n := countRows(t, db, `SELECT COUNT(*) FROM vec_chunks_chunks`); n != 1 {
		t.Errorf("vec_chunks_chunks = %d, want 1", n)
	}
}

// records keeps every log record, so a test can read messages in order and
// attrs by key.
type records struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (c *records) Enabled(context.Context, slog.Level) bool { return true }
func (c *records) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, r.Clone())
	return nil
}
func (c *records) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *records) WithGroup(string) slog.Handler      { return c }

func (c *records) find(msg string) (slog.Record, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.recs {
		if r.Message == msg {
			return r, true
		}
	}
	return slog.Record{}, false
}

func attrsOf(r slog.Record) map[string]string {
	m := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.String()
		return true
	})
	return m
}

// Both rewrites can take minutes and hold the boot before the server
// listens, so each says it started, not only that it finished.
func TestLogVectorIndexAtBoot_saysEachRewriteBeforeItRuns(t *testing.T) {
	db := vecDB(t)
	churnVectors(t, db, 3000, 100)
	logs := &records{}

	LogVectorIndexAtBoot(context.Background(), db, slog.New(logs))

	var msgs []string
	for _, r := range logs.recs {
		msgs = append(msgs, r.Message)
	}
	want := []string{"compacting the vector index", "copying vectors out", "writing vectors back",
		"vector index compacted", "vacuuming the database", "database vacuumed"}
	if !slices.Equal(msgs, want) {
		t.Fatalf("messages = %q, want %q", msgs, want)
	}
	attrs := attrsOf(logs.recs[0])
	for k, v := range map[string]string{"reason": "boot", "rows": "100", "chunks": "3", "chunks_needed": "1"} {
		if attrs[k] != v {
			t.Errorf("attr %s = %q, want %q (all: %v)", k, attrs[k], v, attrs)
		}
	}
	var before int64
	logs.recs[4].Attrs(func(a slog.Attr) bool {
		if a.Key == "bytes_before" {
			before = a.Value.Int64()
		}
		return true
	})
	if before <= 0 {
		t.Errorf("vacuum start line has no bytes_before: %v", logs.recs[4])
	}
}

// A copy longer than one heartbeat says where it has got to while it runs.
func TestCompactVectorsAndLog_heartbeatReportsProgress(t *testing.T) {
	db := vecDB(t)
	churnVectors(t, db, 6000, 1000)
	defer func(d time.Duration, b int) { compactHeartbeat, compactBatch = d, b }(compactHeartbeat, compactBatch)
	compactHeartbeat, compactBatch = time.Millisecond, 64
	logs := &records{}

	CompactVectorsAndLog(context.Background(), db, slog.New(logs), "reason", "boot")

	r, ok := logs.find("compacting the vector index, still running")
	if !ok {
		t.Fatalf("no heartbeat, records = %v", logs.recs)
	}
	attrs := attrsOf(r)
	for _, k := range []string{"reason", "step", "done", "total", "elapsed"} {
		if attrs[k] == "" {
			t.Errorf("heartbeat lacks %s: %v", k, attrs)
		}
	}
	if n := countRows(t, db, `SELECT COUNT(*) FROM vec_chunks_rowids`); n != 1000 {
		t.Errorf("rows = %d, want 1000", n)
	}
	if got := nearestIDs(t, db, 5999, 1); len(got) != 1 || got[0] != 5999 {
		t.Errorf("nearest = %v, want [5999]", got)
	}
}

func TestLogVectorIndexAtBoot_statesAHealthyTable(t *testing.T) {
	db := vecDB(t)
	churnVectors(t, db, 10, 10)
	log, out := logTo()

	LogVectorIndexAtBoot(context.Background(), db, log)

	wantAll(t, out.String(), `msg="vector index"`, "rows=10", "chunks=1")
	if strings.Contains(out.String(), "vacuumed") {
		t.Errorf("log = %q, want no vacuum of a healthy table", out.String())
	}
}

func TestLogVectorIndexAtBoot_aFailureOnlyWarns(t *testing.T) {
	db := vecDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	log, out := logTo()

	LogVectorIndexAtBoot(ctx, db, log)

	wantAll(t, out.String(), "level=WARN", `msg="compacting the vector index failed"`, "err=")
}

func TestVacuumAndLog_reportsTheFileShrinking(t *testing.T) {
	db := vecDB(t)
	churnVectors(t, db, 3000, 100)
	if _, err := CompactVectors(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	log, out := logTo()

	VacuumAndLog(context.Background(), db, log)

	wantAll(t, out.String(), `msg="database vacuumed"`)
	if strings.Contains(out.String(), "failed") {
		t.Errorf("log = %q", out.String())
	}
}

func TestVacuumAndLog_aFailureWarns(t *testing.T) {
	db := vecDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	log, out := logTo()

	VacuumAndLog(ctx, db, log)

	wantAll(t, out.String(), "level=WARN", `msg="vacuuming the database failed"`)
}

func TestWarnVectorBloat_warnsWithoutTouchingTheTable(t *testing.T) {
	db := vecDB(t)
	churnVectors(t, db, 3000, 100)
	log, out := logTo()

	WarnVectorBloat(context.Background(), db, log, "video_id", "v1")

	wantAll(t, out.String(), "level=WARN", `msg="vector index bloated, compacted at next boot"`,
		"video_id=v1", "rows=100", "chunks=3", "chunks_needed=1")
	if n := countRows(t, db, `SELECT COUNT(*) FROM vec_chunks_chunks`); n != 3 {
		t.Errorf("vec_chunks_chunks = %d, want 3 untouched", n)
	}
}

func TestWarnVectorBloat_quietWhenHealthy(t *testing.T) {
	db := vecDB(t)
	churnVectors(t, db, 10, 10)
	log, out := logTo()

	WarnVectorBloat(context.Background(), db, log)

	if out.Len() != 0 {
		t.Errorf("log = %q, want nothing", out.String())
	}
}

func TestWarnVectorBloat_aFailedMeasureWarns(t *testing.T) {
	db := vecDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	log, out := logTo()

	WarnVectorBloat(ctx, db, log)

	wantAll(t, out.String(), "level=WARN", `msg="measuring the vector index failed"`)
}

func TestVideoChunkWrites_warnOfBloatTheyLeave(t *testing.T) {
	// The writes that delete vectors are where bloat comes from, so they are
	// where it is reported, naming the video that tipped it.
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	const dim = 1536
	churnVectorsOf(t, db, dim, 3000, 100)
	if _, err := db.Exec(`INSERT INTO videos (id, url) VALUES ('v1','u')`); err != nil {
		t.Fatal(err)
	}
	log, out := logTo()
	restore := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(restore) })
	s := NewStore(db)
	ctx := context.Background()

	if err := s.ReplaceVideoChunks(ctx, "v1", IndexMeta{Model: "e5", Dim: dim, Rev: ChunkRecipeRev},
		[]ChunkRow{{Text: "titanium frame"}}, [][]float32{unitOf(dim, 1)}); err != nil {
		t.Fatal(err)
	}
	wantAll(t, out.String(), `msg="vector index bloated, compacted at next boot"`, "video_id=v1")

	out.Reset()
	if err := s.DeleteVideoChunks(ctx, "v1"); err != nil {
		t.Fatal(err)
	}
	wantAll(t, out.String(), `msg="vector index bloated, compacted at next boot"`, "video_id=v1")
}
