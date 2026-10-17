package videos

import (
	"context"
	"testing"

	"github.com/trick77/peeq/internal/rag"
)

// The channel's storage is split three ways: what is on disk, what was kept
// after its file went (tombstones, ignored reads), and the rest (the New tab,
// queued work). Each part sums what peeq stores in the database for it.
func TestChannelStorage_splitsOnDiskKeptAndOther(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	rs := rag.NewStore(db)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO channels (id, name) VALUES ('UC1','c'), ('UC2','other')`); err != nil {
		t.Fatal(err)
	}
	vec := make([]float32, 1536)
	vec[0] = 1
	type seed struct {
		id, channel, status, ledger string
		file                        int64
	}
	for _, sd := range []seed{
		{"disk", "UC1", StatusDownloaded, "queued", 1000},
		{"tomb", "UC1", StatusTombstoned, "queued", 0},
		{"ignored", "UC1", StatusNew, "ignored", 0},
		{"inbox", "UC1", StatusNew, "pending", 0},
		{"elsewhere", "UC2", StatusDownloaded, "queued", 5000},
	} {
		if _, err := db.Exec(`INSERT INTO channel_videos (video_id, channel_id, title, state) VALUES (?,?,'t',?)`, sd.id, sd.channel, sd.ledger); err != nil {
			t.Fatal(err)
		}
		if err := s.Upsert(Video{ID: sd.id, URL: "u", ChannelID: sd.channel}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE videos SET status = ?, filesize_bytes = ? WHERE id = ?`, sd.status, sd.file, sd.id); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTranscript(sd.id, TranscriptSourceCaption, "0123456789"); err != nil { // 10 bytes
			t.Fatal(err)
		}
		if err := s.SetSummaryText(sd.id, "abcde"); err != nil { // 5 bytes
			t.Fatal(err)
		}
		if err := s.SetThumbnail(sd.id, "image/jpeg", []byte("PNG")); err != nil { // 3 bytes
			t.Fatal(err)
		}
		if err := rs.ReplaceVideoChunks(ctx, sd.id, rag.IndexMeta{Model: "m", Dim: 1536, Rev: rag.ChunkRecipeRev},
			[]rag.ChunkRow{{Ordinal: 0, Text: "hello", Kind: "transcript"}}, [][]float32{vec}); err != nil { // 5 bytes, 1 vector
			t.Fatal(err)
		}
	}

	got, err := s.ChannelStorage(ctx, "UC1", "")
	if err != nil {
		t.Fatal(err)
	}
	one := func(videos int, file int64) StorageGroup {
		return StorageGroup{
			Videos: videos, FileBytes: file,
			TranscriptBytes: 10 * int64(videos), SummaryBytes: 5 * int64(videos),
			// The chunk text is stored twice: transcript_chunks and the
			// full-text index's own copy.
			SearchTextBytes: 10 * int64(videos), VectorBytes: 1536 * 4 * int64(videos),
			PosterBytes: 3 * int64(videos),
		}
	}
	want := ChannelStorage{OnDisk: one(1, 1000), Kept: one(2, 0), Other: one(1, 0)}
	// Summary bytes include chapters/key points; their empty defaults are
	// counted as stored, so compare everything but that against fixed numbers
	// and summaries against at-least.
	for name, pair := range map[string][2]StorageGroup{
		"on disk": {got.OnDisk, want.OnDisk}, "kept": {got.Kept, want.Kept}, "other": {got.Other, want.Other},
	} {
		g, w := pair[0], pair[1]
		if g.SummaryBytes < w.SummaryBytes {
			t.Errorf("%s summary bytes = %d, want >= %d", name, g.SummaryBytes, w.SummaryBytes)
		}
		g.SummaryBytes, w.SummaryBytes = 0, 0
		if g != w {
			t.Errorf("%s = %+v, want %+v", name, g, w)
		}
	}
}

// The edges: a legacy row with no channel id counts through the name fallback
// (as in the header), a NULL file size counts as nothing rather than failing,
// an ignored video that was once downloaded is not "kept" (a switch-off does
// not purge it), and a channel with no chunks needs no vector width.
func TestChannelStorage_edges(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO channels (id, name) VALUES ('UC1','Chan')`); err != nil {
		t.Fatal(err)
	}
	for _, v := range []Video{
		{ID: "legacy", URL: "u", ChannelName: "Chan"},
		{ID: "nullsize", URL: "u", ChannelID: "UC1"},
		{ID: "wasdl", URL: "u", ChannelID: "UC1"},
	} {
		if err := s.Upsert(v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE videos SET status = 'downloaded', filesize_bytes = 7 WHERE id = 'legacy'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE videos SET status = 'downloaded', filesize_bytes = NULL WHERE id = 'nullsize'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE videos SET status = 'new' WHERE id = 'wasdl'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO channel_videos (video_id, channel_id, title, state) VALUES ('wasdl','UC1','t','ignored')`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTranscript("wasdl", TranscriptSourceDownload, "x"); err != nil {
		t.Fatal(err)
	}

	got, err := s.ChannelStorage(ctx, "UC1", "Chan")
	if err != nil {
		t.Fatal(err)
	}
	if got.OnDisk.Videos != 2 || got.OnDisk.FileBytes != 7 {
		t.Fatalf("on disk = %+v, want 2 videos and 7 bytes (legacy row in, NULL size as 0)", got.OnDisk)
	}
	if got.Kept.Videos != 0 || got.Other.Videos != 1 {
		t.Fatalf("kept = %+v other = %+v, want the once-downloaded ignore under other", got.Kept, got.Other)
	}
	if got.OnDisk.VectorBytes != 0 {
		t.Fatalf("vector bytes = %d with no chunks", got.OnDisk.VectorBytes)
	}

	if _, err := db.Exec(`DROP TABLE video_in_depth`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChannelStorage(ctx, "UC1", ""); err == nil {
		t.Fatal("want an error from a broken query")
	}
}
