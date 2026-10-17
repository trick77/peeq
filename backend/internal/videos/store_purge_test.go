package videos

import (
	"context"
	"testing"

	"github.com/trick77/peeq/internal/rag"
)

// Purge is what a delete does on a channel whose keep_reads is off: the row and
// everything hanging off it go, the search index included, and the ledger row
// that stops a scan from offering the video again stays.
func TestPurge_removesRowTranscriptAndIndexKeepsLedger(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO channels (id, name) VALUES ('UC1','c')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO channel_videos (video_id, channel_id, title, state) VALUES ('v','UC1','t','queued')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(Video{ID: "v", URL: "u", ChannelID: "UC1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTranscript("v", TranscriptSourceDownload, "WEBVTT"); err != nil {
		t.Fatal(err)
	}
	vec := make([]float32, 1536)
	vec[0] = 1
	rs := rag.NewStore(db)
	if err := rs.ReplaceVideoChunks(ctx, "v", rag.IndexMeta{Model: "m", Dim: 1536, Rev: rag.ChunkRecipeRev},
		[]rag.ChunkRow{{Ordinal: 0, Text: "hello", Kind: "transcript"}}, [][]float32{vec}); err != nil {
		t.Fatal(err)
	}

	if err := s.Purge("v"); err != nil {
		t.Fatalf("purge: %v", err)
	}

	if got, err := s.Get("v"); err != nil || got != nil {
		t.Fatalf("Get after purge = %v, %v; want nil row", got, err)
	}
	for _, q := range []string{
		`SELECT COUNT(*) FROM video_transcripts WHERE video_id = 'v'`,
		`SELECT COUNT(*) FROM transcript_chunks WHERE video_id = 'v'`,
		`SELECT COUNT(*) FROM vec_chunks`,
		`SELECT COUNT(*) FROM fts_chunks`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if n != 0 {
			t.Fatalf("%s = %d after purge, want 0", q, n)
		}
	}
	var ledger int
	if err := db.QueryRow(`SELECT COUNT(*) FROM channel_videos WHERE video_id = 'v'`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if ledger != 1 {
		t.Fatalf("ledger rows = %d after purge, want 1 (it blocks a re-fetch)", ledger)
	}
}

// A video added by URL has no ledger row, and one parked as 'unavailable' is
// revived by a scan once its videos row is gone. Purge leaves both settled at
// 'queued', which no scan revisits.
func TestPurge_settlesTheLedgerSoAScanNeverBringsItBack(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	if _, err := db.Exec(`INSERT INTO channels (id, name) VALUES ('UC1','c')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO channel_videos (video_id, channel_id, title, state) VALUES ('parked','UC1','t','unavailable')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"byurl", "parked"} {
		if err := s.Upsert(Video{ID: id, URL: "https://youtu.be/" + id, Title: "T " + id, ChannelID: "UC1"}); err != nil {
			t.Fatal(err)
		}
		if err := s.Purge(id); err != nil {
			t.Fatalf("purge %s: %v", id, err)
		}
		var state string
		if err := db.QueryRow(`SELECT state FROM channel_videos WHERE video_id = ?`, id).Scan(&state); err != nil {
			t.Fatalf("%s ledger row: %v", id, err)
		}
		if state != "queued" {
			t.Fatalf("%s ledger state = %q, want queued", id, state)
		}
	}
}

// The inbox poster hangs off the ledger row, not the videos row, so the cascade
// misses it; Purge removes it itself.
func TestPurge_removesTheInboxPoster(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	if _, err := db.Exec(`INSERT INTO channels (id, name) VALUES ('UC1','c')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO channel_videos (video_id, channel_id, title, state) VALUES ('v','UC1','t','ignored')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pending_thumbnails (video_id, mime, bytes) VALUES ('v','image/jpeg',x'00')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(Video{ID: "v", URL: "u", ChannelID: "UC1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Purge("v"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending_thumbnails`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("pending_thumbnails = %d after purge, want 0", n)
	}
}

// keptFixture seeds one channel with a video in every state PurgeKept has to
// tell apart, and indexes the ones that would be indexed in production.
func keptFixture(t *testing.T) (*Store, *rag.Store) {
	t.Helper()
	db := openTestDB(t)
	s := New(db)
	rs := rag.NewStore(db)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO channels (id, name, keep_reads) VALUES ('UC1','c',0), ('UC2','other',0)`); err != nil {
		t.Fatal(err)
	}
	vec := make([]float32, 1536)
	vec[0] = 1
	type seed struct {
		id, channel, status, ledger, source string
		indexed                             bool
	}
	for _, sd := range []seed{
		{"tomb", "UC1", StatusTombstoned, "queued", TranscriptSourceDownload, true},
		{"ignored", "UC1", StatusNew, "ignored", TranscriptSourceCaption, true},
		{"inbox", "UC1", StatusNew, "pending", TranscriptSourceCaption, true},
		{"inboxbare", "UC1", StatusNew, "pending", TranscriptSourceCaption, false},
		{"ondisk", "UC1", StatusDownloaded, "queued", TranscriptSourceDownload, true},
		{"cancelled", "UC1", StatusNew, "queued", TranscriptSourceCaption, true},
		{"elsewhere", "UC2", StatusTombstoned, "queued", TranscriptSourceDownload, true},
	} {
		if _, err := db.Exec(`INSERT INTO channel_videos (video_id, channel_id, title, state) VALUES (?,?,'t',?)`, sd.id, sd.channel, sd.ledger); err != nil {
			t.Fatal(err)
		}
		if err := s.Upsert(Video{ID: sd.id, URL: "u", ChannelID: sd.channel}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE videos SET status = ? WHERE id = ?`, sd.status, sd.id); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTranscript(sd.id, sd.source, "WEBVTT"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetSummaryText(sd.id, "summary"); err != nil {
			t.Fatal(err)
		}
		if sd.indexed {
			if err := rs.ReplaceVideoChunks(ctx, sd.id, rag.IndexMeta{Model: "m", Dim: 1536, Rev: rag.ChunkRecipeRev},
				[]rag.ChunkRow{{Ordinal: 0, Text: "hello " + sd.id, Kind: "transcript"}}, [][]float32{vec}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return s, rs
}

func TestKeptImpact_countsWhatASwitchOffWouldTake(t *testing.T) {
	s, _ := keptFixture(t)
	got, err := s.KeptImpact("UC1")
	if err != nil {
		t.Fatal(err)
	}
	want := KeptCounts{Deleted: 1, Ignored: 1, Inbox: 1}
	if got != want {
		t.Fatalf("KeptImpact = %+v, want %+v", got, want)
	}
}

func TestPurgeKept_purgesKeptAndDropsInboxIndexOnly(t *testing.T) {
	s, rs := keptFixture(t)
	ctx := context.Background()

	got, err := s.PurgeKept("UC1", func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if want := (KeptCounts{Deleted: 1, Ignored: 1, Inbox: 1}); got != want {
		t.Fatalf("PurgeKept = %+v, want %+v", got, want)
	}

	for _, id := range []string{"tomb", "ignored"} {
		if v, err := s.Get(id); err != nil || v != nil {
			t.Fatalf("%s = %+v err=%v, want purged", id, v, err)
		}
	}
	inbox, err := s.Get("inbox")
	if err != nil || inbox == nil {
		t.Fatalf("inbox row = %+v err=%v, want kept", inbox, err)
	}
	if inbox.Summary != "summary" {
		t.Fatalf("inbox summary = %q, want kept (the New tab card shows it)", inbox.Summary)
	}
	if inbox.EmbedRev != 0 || inbox.EmbedModel != "" {
		t.Fatalf("inbox embed = %q/%d, want cleared so a download re-embeds", inbox.EmbedModel, inbox.EmbedRev)
	}
	if has, err := rs.HasChunks(ctx, "inbox"); err != nil || has {
		t.Fatalf("inbox HasChunks = %v err=%v, want false", has, err)
	}
	for _, id := range []string{"ondisk", "cancelled", "elsewhere", "inboxbare"} {
		if v, err := s.Get(id); err != nil || v == nil {
			t.Fatalf("%s = %+v err=%v, want untouched", id, v, err)
		}
	}
	for _, id := range []string{"ondisk", "cancelled", "elsewhere"} {
		if has, err := rs.HasChunks(ctx, id); err != nil || !has {
			t.Fatalf("%s HasChunks = %v err=%v, want kept", id, has, err)
		}
	}
}

// The switch can be turned back on while a purge runs; what is left stays.
func TestPurgeKept_stopsWhenTheSwitchGoesBackOn(t *testing.T) {
	s, _ := keptFixture(t)
	got, err := s.PurgeKept("UC1", func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if got != (KeptCounts{}) {
		t.Fatalf("PurgeKept = %+v, want nothing done", got)
	}
	if v, err := s.Get("tomb"); err != nil || v == nil {
		t.Fatalf("tomb = %+v err=%v, want kept", v, err)
	}
}

// A video whose channel row was never created reads as keep: purging something
// no switch was ever shown for is the wrong default.
func TestChannelKeepReads_missingChannelReadsAsOn(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Video{ID: "v", URL: "u", ChannelID: "UCgone"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatal(err)
	}
	if !got.ChannelKeepReads {
		t.Fatal("ChannelKeepReads = false for a video with no channel row, want true")
	}
}
