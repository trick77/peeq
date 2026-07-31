package videos

import (
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

func TestPhase3Setters(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	store.Migrate(db)
	s := New(db)
	if err := s.Upsert(Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAudioLanguage("v1", "en"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSummaryStatus("v1", "running", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSummary("v1", "prose", `[{"ts":0,"title":"Intro","source":"mimo"}]`, `[{"ts":12,"text":"wow"}]`); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.AudioLanguage != "en" {
		t.Fatalf("audio_language = %q, want en", got.AudioLanguage)
	}
	// SetSummary transitions summary_status to 'done' (per its documented
	// contract), superseding the earlier SetSummaryStatus("running") call.
	if got.Summary != "prose" || got.SummaryStatus != "done" {
		t.Fatalf("summary fields: %+v", got)
	}
	if got.Chapters == "" || got.KeyPoints == "" {
		t.Fatal("chapters/key_points not stored")
	}
}
