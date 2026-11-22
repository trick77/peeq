package videos

import (
	"maps"
	"testing"
)

// Searchable = at least one transcript_chunks row, whatever the status (the
// channel page's IndexedCount). A video with many chunks counts once; a channel
// with none indexed is absent; a legacy row without a channel id forms none.
func TestChannelSearchableCounts(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	for _, row := range []struct {
		id, ch, status string
		chunks         int
	}{
		{"all1", "UCall", StatusDownloaded, 3},
		{"all2", "UCall", StatusTombstoned, 1},
		{"some1", "UCsome", StatusDownloaded, 2},
		{"some2", "UCsome", StatusNew, 0},
		{"some3", "UCsome", StatusQueued, 0},
		{"none1", "UCnone", StatusNew, 0},
		{"legacy1", "", StatusDownloaded, 1},
	} {
		mustExecBrowse(t, db, `INSERT INTO videos (id, url, channel_id, channel_name, status) VALUES (?, 'u', ?, 'Legacy Name', ?)`,
			row.id, row.ch, row.status)
		for i := range row.chunks {
			mustExecBrowse(t, db, `INSERT INTO transcript_chunks (video_id, ordinal, text) VALUES (?, ?, 'words')`, row.id, i)
		}
	}

	got, err := s.ChannelSearchableCounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"UCall": 2, "UCsome": 1}; !maps.Equal(got, want) {
		t.Errorf("searchable counts = %v, want %v", got, want)
	}
}

func TestChannelSearchableCounts_emptyLibrary(t *testing.T) {
	got, err := New(openTestDB(t)).ChannelSearchableCounts(t.Context())
	if err != nil || len(got) != 0 {
		t.Errorf("empty library: %v, %v", got, err)
	}
}

// One statement for every channel, driven by idx_videos_channel_search in
// channel order (no sort) and probing chunks by key: never a video row.
func TestChannelSearchableCounts_staysOnTheIndexes(t *testing.T) {
	db := openTestDB(t)
	assertPlanOnly(t, "searchable per channel", queryPlan(t, db, channelSearchableSQL),
		"SCAN v USING COVERING INDEX idx_videos_channel_search",
		"CORRELATED SCALAR SUBQUERY *",
		"SEARCH c USING COVERING INDEX idx_transcript_chunks_video (video_id=?)")
}
