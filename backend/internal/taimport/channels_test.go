package taimport

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/channels"
)

// fakeLister returns a canned channel list.
type fakeLister struct {
	out []Channel
	err error
}

func (f *fakeLister) AllChannels(context.Context) ([]Channel, error) { return f.out, f.err }

// spyWriter records what the runner asked for.
type spyWriter struct {
	upserted   []channels.Channel
	subscribed []string
	nextScanAt []string
	failOn     string // channel id whose Upsert should fail
}

func (s *spyWriter) Upsert(c channels.Channel) error {
	if c.ID == s.failOn {
		return errors.New("boom")
	}
	s.upserted = append(s.upserted, c)
	return nil
}

func (s *spyWriter) Subscribe(channelID, nextScanAt string) error {
	s.subscribed = append(s.subscribed, channelID)
	s.nextScanAt = append(s.nextScanAt, nextScanAt)
	return nil
}

var fixedNow = time.Date(2026, 7, 21, 14, 30, 5, 0, time.UTC)

func TestImportChannels_importsActiveAndInactive(t *testing.T) {
	// Given
	lister := &fakeLister{out: []Channel{
		{ID: "UC_a", Name: "Alpha", Active: true, Subscribed: true},
		{ID: "UC_b", Name: "Beta", Active: false, Subscribed: true},
	}}
	w := &spyWriter{}

	// When
	got, err := ImportChannels(context.Background(), lister, w, false, fixedNow)

	// Then
	if err != nil {
		t.Fatalf("ImportChannels: %v", err)
	}
	if got.Subscribed != 2 || got.Active != 1 || got.Inactive != 1 || got.Skipped != 0 {
		t.Errorf("result = %+v, want Subscribed:2 Active:1 Inactive:1 Skipped:0", got)
	}
	if len(w.upserted) != 2 || len(w.subscribed) != 2 {
		t.Fatalf("upserted=%d subscribed=%d, want 2 and 2", len(w.upserted), len(w.subscribed))
	}
	if w.upserted[0].ID != "UC_a" || w.upserted[0].Name != "Alpha" {
		t.Errorf("upserted[0] = %+v", w.upserted[0])
	}
	// TubeArchivist has no @handle, so peeq's stays empty.
	if w.upserted[0].Handle != "" {
		t.Errorf("Handle = %q, want empty (TA does not store handles)", w.upserted[0].Handle)
	}
	// next_scan_at must match the format used elsewhere in peeq.
	if w.nextScanAt[0] != "2026-07-21 14:30:05" {
		t.Errorf("nextScanAt = %q, want %q", w.nextScanAt[0], "2026-07-21 14:30:05")
	}
}

func TestImportChannels_skipsUnsubscribed(t *testing.T) {
	// Given: a channel TubeArchivist knows about only from a one-off download.
	lister := &fakeLister{out: []Channel{
		{ID: "UC_a", Name: "Alpha", Active: true, Subscribed: true},
		{ID: "UC_oneoff", Name: "OneOff", Active: true, Subscribed: false},
	}}
	w := &spyWriter{}

	// When
	got, err := ImportChannels(context.Background(), lister, w, false, fixedNow)

	// Then
	if err != nil {
		t.Fatalf("ImportChannels: %v", err)
	}
	if got.Subscribed != 1 || got.Skipped != 1 {
		t.Errorf("result = %+v, want Subscribed:1 Skipped:1", got)
	}
	if len(w.subscribed) != 1 || w.subscribed[0] != "UC_a" {
		t.Errorf("subscribed = %v, want [UC_a] only", w.subscribed)
	}
	for _, c := range w.upserted {
		if c.ID == "UC_oneoff" {
			t.Error("never-subscribed channel was tracked; it must be skipped entirely")
		}
	}
}

func TestImportChannels_dryRunWritesNothing(t *testing.T) {
	// Given
	lister := &fakeLister{out: []Channel{
		{ID: "UC_a", Name: "Alpha", Active: true, Subscribed: true},
		{ID: "UC_b", Name: "Beta", Active: false, Subscribed: true},
	}}
	w := &spyWriter{}

	// When
	got, err := ImportChannels(context.Background(), lister, w, true, fixedNow)

	// Then
	if err != nil {
		t.Fatalf("ImportChannels: %v", err)
	}
	if got.Subscribed != 2 || got.Active != 1 || got.Inactive != 1 {
		t.Errorf("result = %+v, want the same counts as a real run", got)
	}
	if len(w.upserted) != 0 || len(w.subscribed) != 0 {
		t.Errorf("dry run wrote: upserted=%v subscribed=%v", w.upserted, w.subscribed)
	}
}

func TestImportChannels_skipsEmptyID(t *testing.T) {
	// Given: a malformed TubeArchivist document.
	lister := &fakeLister{out: []Channel{
		{ID: "", Name: "Nameless", Active: true, Subscribed: true},
	}}
	w := &spyWriter{}

	// When
	got, err := ImportChannels(context.Background(), lister, w, false, fixedNow)

	// Then
	if err != nil {
		t.Fatalf("ImportChannels: %v", err)
	}
	if got.Subscribed != 0 || got.Skipped != 1 {
		t.Errorf("result = %+v, want Subscribed:0 Skipped:1", got)
	}
	if len(w.upserted) != 0 {
		t.Error("a channel with no id must not be written")
	}
}

func TestImportChannels_listErrorPropagates(t *testing.T) {
	lister := &fakeLister{err: errors.New("network down")}
	w := &spyWriter{}

	if _, err := ImportChannels(context.Background(), lister, w, false, fixedNow); err == nil {
		t.Fatal("err = nil, want the lister error propagated")
	}
}

func TestImportChannels_upsertErrorPropagates(t *testing.T) {
	lister := &fakeLister{out: []Channel{
		{ID: "UC_bad", Name: "Bad", Active: true, Subscribed: true},
	}}
	w := &spyWriter{failOn: "UC_bad"}

	if _, err := ImportChannels(context.Background(), lister, w, false, fixedNow); err == nil {
		t.Fatal("err = nil, want the upsert error propagated")
	}
}
