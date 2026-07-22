package taimport

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/videos"
)

// --- fakes -------------------------------------------------------------------

type fakeVideoLister struct {
	byKey map[string][]Video // channelID + "|" + watch -> videos
}

func (f *fakeVideoLister) ChannelVideos(_ context.Context, channelID, watch string) ([]Video, error) {
	return f.byKey[channelID+"|"+watch], nil
}

type spyVideoWriter struct {
	existing   map[string]*videos.Video
	calls      []string
	downloaded map[string]videos.DownloadedResult
	resumes    map[string]float64
	summaries  []string
}

func (s *spyVideoWriter) Get(id string) (*videos.Video, error) { return s.existing[id], nil }
func (s *spyVideoWriter) Upsert(v videos.Video) error {
	s.calls = append(s.calls, "Upsert:"+v.ID)
	return nil
}
func (s *spyVideoWriter) SetDownloaded(id string, r videos.DownloadedResult) error {
	s.calls = append(s.calls, "SetDownloaded:"+id)
	if s.downloaded == nil {
		s.downloaded = map[string]videos.DownloadedResult{}
	}
	s.downloaded[id] = r
	return nil
}
func (s *spyVideoWriter) SetResumeRaw(id string, p float64) error {
	s.calls = append(s.calls, "SetResumeRaw:"+id)
	if s.resumes == nil {
		s.resumes = map[string]float64{}
	}
	s.resumes[id] = p
	return nil
}
func (s *spyVideoWriter) EnqueueSummary(id string) (int64, error) {
	s.calls = append(s.calls, "EnqueueSummary:"+id)
	s.summaries = append(s.summaries, id)
	return int64(len(s.summaries)), nil
}

// writeTAVideo lays down a fake TubeArchivist video: the .mp4 and any .vtt on
// the media mount, and the .jpg on the cache mount (sharded by the lowercased
// first character of the id, as TubeArchivist does).
func writeTAVideo(t *testing.T, mediaRoot, cacheRoot, channelID, videoID string, langs []string) {
	t.Helper()
	write := func(path, content string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(mediaRoot, channelID, videoID+".mp4"), "video-"+videoID)
	shard := strings.ToLower(videoID[:1])
	write(filepath.Join(cacheRoot, "videos", shard, videoID+".jpg"), "thumb-"+videoID)
	for _, l := range langs {
		write(filepath.Join(mediaRoot, channelID, videoID+"."+l+".vtt"), "WEBVTT "+l)
	}
}

func assertOrder(t *testing.T, calls []string, seq ...string) {
	t.Helper()
	idx := 0
	for _, c := range calls {
		if idx < len(seq) && c == seq[idx] {
			idx++
		}
	}
	if idx != len(seq) {
		t.Errorf("calls %v do not contain the ordered sequence %v", calls, seq)
	}
}

// --- task 6: runner against a spy -------------------------------------------

func TestImportVideos_writesInOrderFiltersAndSkips(t *testing.T) {
	taMedia, taCache, peeqMedia := t.TempDir(), t.TempDir(), t.TempDir()
	writeTAVideo(t, taMedia, taCache, "UC1", "Aid11111111", []string{"de", "en"}) // continue, subs
	writeTAVideo(t, taMedia, taCache, "UC1", "Bid22222222", nil)                  // unwatched
	// Cid (short) is filtered by type before any file check; Did (downloaded)
	// is skipped at Get; Eid (missing) has no .mp4 on disk. None need files.

	lister := &fakeVideoLister{byKey: map[string][]Video{
		// One unwatched pass returns never-started AND partially-watched videos.
		// A is a partial (player.watched=false) whose resume position TA injects
		// — the case the earlier two-pass/dedup code silently dropped.
		"UC1|unwatched": {
			{ID: "Aid11111111", ChannelID: "UC1", Title: "A", DurationSeconds: 600, Position: 123.5, VidType: "videos", SubtitleLangs: []string{"de", "en"}},
			{ID: "Bid22222222", ChannelID: "UC1", Title: "B", VidType: "videos"}, // never started, no position
			{ID: "Cid33333333", ChannelID: "UC1", Title: "C", VidType: "shorts"},
			{ID: "Did44444444", ChannelID: "UC1", Title: "D", VidType: "videos"},
			{ID: "Eid55555555", ChannelID: "UC1", Title: "E", VidType: "videos"},
		},
	}}
	w := &spyVideoWriter{existing: map[string]*videos.Video{
		"Did44444444": {ID: "Did44444444", Status: "downloaded"},
	}}
	opts := ImportOptions{
		Paths: PathMapper{TAMediaRoot: taMedia, TACacheRoot: taCache, PeeqMediaDir: peeqMedia},
		Types: []string{"videos"}, // exclude the short
	}

	res, err := ImportVideos(context.Background(), lister, w, []string{"UC1"}, opts, false)
	if err != nil {
		t.Fatalf("ImportVideos: %v", err)
	}
	if res.Imported != 2 || res.SkippedDownloaded != 1 || res.SkippedType != 1 || res.MissingFile != 1 || res.WithResume != 1 {
		t.Fatalf("result = %+v, want imported 2 / skippedDownloaded 1 / skippedType 1 / missing 1 / withResume 1", res)
	}
	// A carries a position -> resume written; B never-started -> not.
	if w.resumes["Aid11111111"] != 123.5 {
		t.Errorf("resume for A = %v, want 123.5", w.resumes["Aid11111111"])
	}
	if _, ok := w.resumes["Bid22222222"]; ok {
		t.Error("B (no position) got a resume write, want none")
	}
	if len(w.summaries) != 2 {
		t.Errorf("summaries = %v, want 2", w.summaries)
	}
	assertOrder(t, w.calls, "Upsert:Aid11111111", "SetDownloaded:Aid11111111", "SetResumeRaw:Aid11111111", "EnqueueSummary:Aid11111111")
	assertOrder(t, w.calls, "Upsert:Bid22222222", "SetDownloaded:Bid22222222", "EnqueueSummary:Bid22222222")
	// English preferred over German; stored RELATIVE to MediaDir.
	if got := w.downloaded["Aid11111111"].SubtitleRelPath; got != "UC1/Aid11111111/Aid11111111.en.vtt" {
		t.Errorf("subtitle rel = %q, want the en track relative to MediaDir", got)
	}
	// media_path is absolute under peeq's MediaDir.
	if got, want := w.downloaded["Aid11111111"].MediaPath, filepath.Join(peeqMedia, "UC1", "Aid11111111", "Aid11111111.mp4"); got != want {
		t.Errorf("media_path = %q, want %q", got, want)
	}
}

// --- task 6/8: dry run writes nothing ---------------------------------------

func TestImportVideos_dryRunPlansButWritesNothing(t *testing.T) {
	taMedia, taCache, peeqMedia := t.TempDir(), t.TempDir(), t.TempDir()
	writeTAVideo(t, taMedia, taCache, "UC1", "Vid00000001", nil)
	lister := &fakeVideoLister{byKey: map[string][]Video{
		"UC1|unwatched": {{ID: "Vid00000001", ChannelID: "UC1", VidType: "videos"}},
	}}
	w := &spyVideoWriter{}
	opts := ImportOptions{
		Paths:       PathMapper{TAMediaRoot: taMedia, TACacheRoot: taCache, PeeqMediaDir: peeqMedia},
		WatchStates: []string{"unwatched"},
	}

	res, err := ImportVideos(context.Background(), lister, w, []string{"UC1"}, opts, true)
	if err != nil {
		t.Fatalf("ImportVideos dry run: %v", err)
	}
	if res.Planned != 1 || res.Imported != 0 || res.BytesMedia == 0 {
		t.Fatalf("result = %+v, want planned 1 / imported 0 / bytes>0", res)
	}
	if len(w.calls) != 0 {
		t.Errorf("dry run wrote: %v", w.calls)
	}
	if _, ok := statSize(filepath.Join(peeqMedia, "UC1", "Vid00000001", "Vid00000001.mp4")); ok {
		t.Error("dry run copied a file")
	}
}

// --- task 8: free-space preflight fails closed ------------------------------

func TestImportVideos_freeSpacePreflightFailsClosed(t *testing.T) {
	taMedia, taCache, peeqMedia := t.TempDir(), t.TempDir(), t.TempDir()
	writeTAVideo(t, taMedia, taCache, "UC1", "Vid00000001", nil)
	lister := &fakeVideoLister{byKey: map[string][]Video{
		"UC1|unwatched": {{ID: "Vid00000001", ChannelID: "UC1", VidType: "videos"}},
	}}
	w := &spyVideoWriter{}
	wantErr := errors.New("not enough free space")
	opts := ImportOptions{
		Paths:       PathMapper{TAMediaRoot: taMedia, TACacheRoot: taCache, PeeqMediaDir: peeqMedia},
		WatchStates: []string{"unwatched"},
		CheckSpace: func(n int64) error {
			if n <= 0 {
				t.Errorf("CheckSpace got %d bytes, want the summed media size", n)
			}
			return wantErr
		},
	}

	res, err := ImportVideos(context.Background(), lister, w, []string{"UC1"}, opts, false)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want the free-space error", err)
	}
	if res.Imported != 0 || len(w.calls) != 0 {
		t.Errorf("wrote despite failing preflight: imported=%d calls=%v", res.Imported, w.calls)
	}
	if _, ok := statSize(filepath.Join(peeqMedia, "UC1", "Vid00000001", "Vid00000001.mp4")); ok {
		t.Error("copied a file despite the failed free-space preflight")
	}
}

// --- task 7: real stores + real filesystem ----------------------------------

func TestImportVideos_realStoresWriteRowsAndFilesIdempotently(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	vstore := videos.New(db)
	sstore := summaryjobs.New(db)
	w := NewStoreWriter(vstore, sstore)

	taMedia, taCache, peeqMedia := t.TempDir(), t.TempDir(), t.TempDir()
	writeTAVideo(t, taMedia, taCache, "UC1", "Vid00000001", []string{"en"})

	lister := &fakeVideoLister{byKey: map[string][]Video{
		// 950/1000 = 95%: SetResume would auto-watch this; the import must not.
		// A partial video appears in the unwatched set with its position.
		"UC1|unwatched": {{ID: "Vid00000001", ChannelID: "UC1", Title: "Real", Description: "desc", DurationSeconds: 1000, Position: 950, VidType: "videos", SubtitleLangs: []string{"en"}}},
	}}
	opts := ImportOptions{
		Paths: PathMapper{TAMediaRoot: taMedia, TACacheRoot: taCache, PeeqMediaDir: peeqMedia},
	}

	res, err := ImportVideos(context.Background(), lister, w, []string{"UC1"}, opts, false)
	if err != nil {
		t.Fatalf("ImportVideos: %v", err)
	}
	if res.Imported != 1 || res.WithResume != 1 {
		t.Fatalf("result = %+v, want imported 1 / withResume 1", res)
	}

	v, err := vstore.Get("Vid00000001")
	if err != nil || v == nil {
		t.Fatalf("get: %v v=%v", err, v)
	}
	if v.Status != "downloaded" {
		t.Errorf("status = %q, want downloaded", v.Status)
	}
	wantMedia := filepath.Join(peeqMedia, "UC1", "Vid00000001", "Vid00000001.mp4")
	if v.MediaPath != wantMedia {
		t.Errorf("media_path = %q, want %q (absolute)", v.MediaPath, wantMedia)
	}
	if v.SubtitlePath != "UC1/Vid00000001/Vid00000001.en.vtt" {
		t.Errorf("subtitle_path = %q, want relative to MediaDir", v.SubtitlePath)
	}
	if v.ResumePositionSeconds != 950 {
		t.Errorf("resume = %v, want 950", v.ResumePositionSeconds)
	}
	if v.Watched {
		t.Error("watched = true, want false — the import must not auto-watch a 95% position")
	}
	if v.Description != "desc" {
		t.Errorf("description = %q, want desc", v.Description)
	}
	if _, ok := statSize(wantMedia); !ok {
		t.Error("media file not copied to disk")
	}
	if _, ok := statSize(filepath.Join(peeqMedia, "UC1", "Vid00000001", "Vid00000001.en.vtt")); !ok {
		t.Error("subtitle file not copied to disk")
	}

	countJobs := func() int {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM summary_jobs WHERE video_id = 'Vid00000001'`).Scan(&n); err != nil {
			t.Fatalf("count jobs: %v", err)
		}
		return n
	}
	if countJobs() != 1 {
		t.Fatalf("summary jobs after import = %d, want 1", countJobs())
	}

	// Re-run: the row is already downloaded, so nothing is re-written and no
	// duplicate summary job is billed.
	res2, err := ImportVideos(context.Background(), lister, w, []string{"UC1"}, opts, false)
	if err != nil {
		t.Fatalf("ImportVideos re-run: %v", err)
	}
	if res2.Imported != 0 || res2.SkippedDownloaded != 1 {
		t.Errorf("re-run result = %+v, want imported 0 / skippedDownloaded 1", res2)
	}
	if countJobs() != 1 {
		t.Errorf("summary jobs after re-run = %d, want still 1 (no duplicate)", countJobs())
	}
}
