package diskimport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/videos"
)

// fakeStore records what the worker stored and how often it went looking.
type fakeStore struct {
	candidates           []videos.ThumbnailImportCandidate
	transcriptCandidates []videos.TranscriptImportCandidate
	stored               map[string][]byte
	mimes                map[string]string
	paths                map[string]string
	transcripts          map[string]string
	sources              map[string]string
	listCalls            int
}

func newFakeStore(c ...videos.ThumbnailImportCandidate) *fakeStore {
	return &fakeStore{
		candidates:  c,
		stored:      map[string][]byte{},
		mimes:       map[string]string{},
		paths:       map[string]string{},
		transcripts: map[string]string{},
		sources:     map[string]string{},
	}
}

// ThumbnaillessVideos mirrors the real query: a video stops being a candidate
// the moment it has stored bytes.
func (f *fakeStore) ThumbnaillessVideos(limit int) ([]videos.ThumbnailImportCandidate, error) {
	f.listCalls++
	var out []videos.ThumbnailImportCandidate
	for _, c := range f.candidates {
		if _, done := f.stored[c.ID]; done {
			continue
		}
		out = append(out, c)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) SetThumbnail(id, mime string, data []byte) error {
	f.stored[id] = data
	f.mimes[id] = mime
	return nil
}

func (f *fakeStore) SetThumbnailPath(id, path string) error {
	f.paths[id] = path
	return nil
}

// TranscriptlessVideos / SetTranscript mirror the thumbnail pair: a video stops
// being a candidate the moment it has stored text.
func (f *fakeStore) TranscriptlessVideos(limit int) ([]videos.TranscriptImportCandidate, error) {
	var out []videos.TranscriptImportCandidate
	for _, c := range f.transcriptCandidates {
		if _, done := f.transcripts[c.ID]; done {
			continue
		}
		out = append(out, c)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeStore) SetTranscript(id, source, vtt string) error {
	f.transcripts[id] = vtt
	f.sources[id] = source
	return nil
}

// writePoster puts a poster file where a download would have left it.
func writePoster(t *testing.T, mediaDir, channelID, videoID, ext string, body []byte) string {
	t.Helper()
	dir := filepath.Join(mediaDir, channelID, videoID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, videoID+ext)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatalf("write poster: %v", err)
	}
	return p
}

func newTestWorker(t *testing.T, store VideoStore, mediaDir string) *Worker {
	t.Helper()
	return NewWorker(Deps{Videos: store, MediaDir: mediaDir})
}

// The ordinary import: the row still points at its poster, so the worker reads
// that file and stores the bytes with the mime its extension implies.
func TestPass_importsPosterFromRecordedPath(t *testing.T) {
	mediaDir := t.TempDir()
	path := writePoster(t, mediaDir, "chan1", "v1", ".webp", []byte("WEBPDATA"))
	store := newFakeStore(videos.ThumbnailImportCandidate{ID: "v1", ChannelID: "chan1", ThumbnailPath: path})

	newTestWorker(t, store, mediaDir).pass(context.Background())

	if got := string(store.stored["v1"]); got != "WEBPDATA" {
		t.Fatalf("stored %q, want WEBPDATA", got)
	}
	if store.mimes["v1"] != "image/webp" {
		t.Fatalf("mime = %q, want image/webp", store.mimes["v1"])
	}
}

// The repair case, and the reason this worker exists: the pointer was blanked by
// a metadata write but the file survived, so the worker finds it at the location
// a download writes to and puts the pointer back.
func TestPass_findsPosterWhoseRecordedPathWasBlanked(t *testing.T) {
	mediaDir := t.TempDir()
	writePoster(t, mediaDir, "chan1", "v1", ".jpg", []byte("JPGDATA"))
	store := newFakeStore(videos.ThumbnailImportCandidate{ID: "v1", ChannelID: "chan1", ThumbnailPath: ""})

	newTestWorker(t, store, mediaDir).pass(context.Background())

	if got := string(store.stored["v1"]); got != "JPGDATA" {
		t.Fatalf("stored %q, want JPGDATA", got)
	}
	if want := filepath.Join("chan1", "v1", "v1.jpg"); store.paths["v1"] != want {
		t.Fatalf("repaired path = %q, want %q", store.paths["v1"], want)
	}
}

// A remote url in thumbnail_path (metadata preflight used to store one) is not a
// file. It must not stop the worker looking where the file actually is.
func TestPass_fallsThroughARemoteURL(t *testing.T) {
	mediaDir := t.TempDir()
	writePoster(t, mediaDir, "chan1", "v1", ".jpg", []byte("JPGDATA"))
	store := newFakeStore(videos.ThumbnailImportCandidate{
		ID: "v1", ChannelID: "chan1", ThumbnailPath: "https://i.ytimg.com/vi/v1/maxresdefault.jpg",
	})

	newTestWorker(t, store, mediaDir).pass(context.Background())

	if got := string(store.stored["v1"]); got != "JPGDATA" {
		t.Fatalf("stored %q, want JPGDATA", got)
	}
}

// A video whose file is genuinely gone (the pre-#239 tombstone unlinked it)
// stays without a poster — and is not stat'd again on the next pass, or the
// worker would re-check the same hopeless rows forever instead of idling.
func TestPass_stopsRetryingAVideoWithNoFile(t *testing.T) {
	mediaDir := t.TempDir()
	store := newFakeStore(videos.ThumbnailImportCandidate{ID: "gone", ChannelID: "chan1"})
	w := newTestWorker(t, store, mediaDir)

	w.pass(context.Background())
	if _, ok := store.stored["gone"]; ok {
		t.Fatal("stored a poster for a video with no file")
	}
	if _, marked := w.missing["thumb:gone"]; !marked {
		t.Fatal("video with no file was not written off")
	}

	// Second pass: the candidate still matches the query (it has no stored
	// poster), so the skip has to happen in the worker.
	writePoster(t, mediaDir, "chan1", "gone", ".jpg", []byte("APPEARED"))
	w.pass(context.Background())
	if _, ok := store.stored["gone"]; ok {
		t.Fatal("re-stat'd a video already written off this process")
	}
}

// A batch full of written-off rows must not starve the ones behind them: the
// pass reads past what it already knows is hopeless.
func TestPass_looksPastWrittenOffCandidates(t *testing.T) {
	mediaDir := t.TempDir()
	store := newFakeStore(
		videos.ThumbnailImportCandidate{ID: "gone", ChannelID: "chan1"},
		videos.ThumbnailImportCandidate{ID: "here", ChannelID: "chan1"},
	)
	writePoster(t, mediaDir, "chan1", "here", ".png", []byte("PNGDATA"))
	w := NewWorker(Deps{Videos: store, MediaDir: mediaDir, BatchSize: 1})

	w.pass(context.Background()) // sees "gone", writes it off
	w.pass(context.Background()) // must reach "here" behind it

	if got := string(store.stored["here"]); got != "PNGDATA" {
		t.Fatalf("stored %q, want PNGDATA", got)
	}
}

// A cancelled context stops the pass rather than working through the rest of the
// batch, so shutdown is prompt.
func TestPass_stopsOnCancelledContext(t *testing.T) {
	mediaDir := t.TempDir()
	writePoster(t, mediaDir, "chan1", "v1", ".jpg", []byte("JPGDATA"))
	store := newFakeStore(videos.ThumbnailImportCandidate{ID: "v1", ChannelID: "chan1"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if newTestWorker(t, store, mediaDir).pass(ctx) {
		t.Fatal("pass reported keep-going on a cancelled context")
	}
	if _, ok := store.stored["v1"]; ok {
		t.Fatal("kept working after cancellation")
	}
}

// fakeChannelStore and fakeLedger are the other two asset kinds' stores.
type fakeChannelStore struct {
	candidates []channels.ImageImportCandidate
	stored     map[string][]byte
}

func (f *fakeChannelStore) ImagelessChannels(limit int) ([]channels.ImageImportCandidate, error) {
	var out []channels.ImageImportCandidate
	for _, c := range f.candidates {
		if _, done := f.stored[c.ChannelID+":"+c.Kind]; done {
			continue
		}
		out = append(out, c)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeChannelStore) SetImage(channelID, kind, mime string, data []byte) error {
	f.stored[channelID+":"+kind] = data
	return nil
}

type fakeLedger struct {
	ids    []string
	stored map[string][]byte
}

func (f *fakeLedger) ThumbnaillessPendingIDs(limit int) ([]string, error) {
	var out []string
	for _, id := range f.ids {
		if _, done := f.stored[id]; done {
			continue
		}
		out = append(out, id)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *fakeLedger) SetThumbnail(videoID, mime string, data []byte) error {
	f.stored[videoID] = data
	return nil
}

// A downloaded video's captions are carried in and the file is removed — the
// unlink is what makes the media tree converge on video files only.
func TestPass_importsTranscriptAndRemovesTheFile(t *testing.T) {
	mediaDir := t.TempDir()
	dir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vtt := filepath.Join(dir, "v1.en.vtt")
	if err := os.WriteFile(vtt, []byte("WEBVTT\n\nhello"), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}
	store := newFakeStore()
	store.transcriptCandidates = []videos.TranscriptImportCandidate{
		{ID: "v1", ChannelID: "chan1", SubtitlePath: filepath.Join("chan1", "v1", "v1.en.vtt")},
	}

	newTestWorker(t, store, mediaDir).pass(context.Background())

	if got := store.transcripts["v1"]; got != "WEBVTT\n\nhello" {
		t.Fatalf("stored %q, want the vtt text", got)
	}
	if store.sources["v1"] != videos.TranscriptSourceDownload {
		t.Fatalf("source = %q, want download", store.sources["v1"])
	}
	if _, err := os.Stat(vtt); !os.IsNotExist(err) {
		t.Fatalf("vtt still on disk after import (err = %v)", err)
	}
}

// A caption read is told apart by WHERE its file was found. Getting this wrong
// would run the full analysis pipeline over a video nobody has asked for yet.
func TestPass_transcriptFromSummariesDirIsACaptionRead(t *testing.T) {
	mediaDir := t.TempDir()
	dir := filepath.Join(mediaDir, ".summaries", "v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "v1.en.vtt"), []byte("WEBVTT"), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}
	store := newFakeStore()
	store.transcriptCandidates = []videos.TranscriptImportCandidate{{ID: "v1", ChannelID: "chan1"}}

	newTestWorker(t, store, mediaDir).pass(context.Background())

	if store.sources["v1"] != videos.TranscriptSourceCaption {
		t.Fatalf("source = %q, want caption", store.sources["v1"])
	}
}

// Channel artwork and inbox posters ride the same pass, and their files go too.
func TestPass_importsChannelImagesAndInboxPosters(t *testing.T) {
	mediaDir := t.TempDir()
	avatarDir := filepath.Join(mediaDir, ".channels", "UC1")
	if err := os.MkdirAll(avatarDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	avatar := filepath.Join(avatarDir, "avatar.jpg")
	if err := os.WriteFile(avatar, []byte("AVATAR"), 0o644); err != nil {
		t.Fatalf("write avatar: %v", err)
	}
	pendingDir := filepath.Join(mediaDir, ".pending", "p1")
	if err := os.MkdirAll(pendingDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pendingDir, "thumbnail.jpg"), []byte("POSTER"), 0o644); err != nil {
		t.Fatalf("write poster: %v", err)
	}

	chans := &fakeChannelStore{
		candidates: []channels.ImageImportCandidate{
			{ChannelID: "UC1", Kind: channels.ImageAvatar, Path: filepath.Join(".channels", "UC1", "avatar.jpg")},
		},
		stored: map[string][]byte{},
	}
	ledger := &fakeLedger{ids: []string{"p1"}, stored: map[string][]byte{}}
	w := NewWorker(Deps{Channels: chans, Ledger: ledger, MediaDir: mediaDir})

	w.pass(context.Background())

	if got := string(chans.stored["UC1:avatar"]); got != "AVATAR" {
		t.Fatalf("avatar stored %q, want AVATAR", got)
	}
	if got := string(ledger.stored["p1"]); got != "POSTER" {
		t.Fatalf("inbox poster stored %q, want POSTER", got)
	}
}

// Once everything is in, the tree is tidied: leftovers no row ever referenced —
// an orphaned .info.json, a stale-extension poster, a caption directory that
// outlived its row — are removed, and a video file is not.
func TestPass_sweepsLeftoversOnceDrained(t *testing.T) {
	mediaDir := t.TempDir()
	dir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mp4 := filepath.Join(dir, "v1.mp4")
	for name, body := range map[string]string{
		"v1.mp4":         "VIDEO",
		"v1.info.json":   "{}",
		"v1.webp":        "STALE POSTER",
		"v1.en.vtt":      "WEBVTT",
		"v1.description": "text",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	legacy := filepath.Join(mediaDir, ".channels", "UC1")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "banner.jpg"), []byte("OLD"), 0o644); err != nil {
		t.Fatalf("write banner: %v", err)
	}
	// A download in flight must survive: it is resumed with --continue.
	staging := filepath.Join(mediaDir, ".staging", "v2")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	inFlightVTT := filepath.Join(staging, "v2.en.vtt")
	if err := os.WriteFile(inFlightVTT, []byte("WEBVTT"), 0o644); err != nil {
		t.Fatalf("write staging vtt: %v", err)
	}

	// Nothing to import: no candidates at all, so the pass is drained.
	w := NewWorker(Deps{Videos: newFakeStore(), MediaDir: mediaDir})
	w.pass(context.Background())

	if _, err := os.Stat(mp4); err != nil {
		t.Fatalf("the video file was swept: %v", err)
	}
	for _, name := range []string{"v1.info.json", "v1.webp", "v1.en.vtt", "v1.description"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived the sweep (err = %v)", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(mediaDir, ".channels")); !os.IsNotExist(err) {
		t.Fatalf(".channels survived the sweep (err = %v)", err)
	}
	if _, err := os.Stat(inFlightVTT); err != nil {
		t.Fatalf("the sweep reached into .staging: %v", err)
	}
}

// The sweep must not run while anything is still queued to be imported: at that
// point a file on disk can still be the only copy.
func TestPass_doesNotSweepWhileImportsRemain(t *testing.T) {
	mediaDir := t.TempDir()
	dir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vtt := filepath.Join(dir, "v1.en.vtt")
	if err := os.WriteFile(vtt, []byte("WEBVTT"), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}
	// One candidate, batch of one: the list comes back full, so there may be
	// more behind it and the pass is not drained.
	store := newFakeStore()
	store.transcriptCandidates = []videos.TranscriptImportCandidate{{ID: "other", ChannelID: "chan9"}}
	w := NewWorker(Deps{Videos: store, MediaDir: mediaDir, BatchSize: 1})

	w.pass(context.Background())

	if _, err := os.Stat(vtt); err != nil {
		t.Fatalf("a transcript was swept before the import drained: %v", err)
	}
}
