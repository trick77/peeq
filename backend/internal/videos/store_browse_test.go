package videos

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
)

// browseRow is one seeded video for the Browse tests.
type browseRow struct {
	id, title, channelID, channelName string
	published, created, downloaded    string
	status, summary                   string
	transcript, chunks                bool
}

func seedBrowse(t *testing.T, db *sql.DB, rows []browseRow) *Store {
	t.Helper()
	s := New(db)
	if _, err := db.Exec(`INSERT INTO channels (id, name) VALUES ('UCa', 'Alpha Cached'), ('UCb', 'Beta')`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if err := s.Upsert(Video{ID: r.id, URL: "u-" + r.id, Title: r.title, ChannelID: r.channelID,
			ChannelName: r.channelName, PublishedAt: r.published, DurationSeconds: 90}); err != nil {
			t.Fatal(err)
		}
		mustExecBrowse(t, db, `UPDATE videos SET status = ?, summary_status = ?, created_at = ?, downloaded_at = NULLIF(?, '') WHERE id = ?`,
			r.status, r.summary, r.created, r.downloaded, r.id)
		if r.transcript {
			if err := s.SetTranscript(r.id, TranscriptSourceCaption, "WEBVTT"); err != nil {
				t.Fatal(err)
			}
		}
		if r.chunks {
			mustExecBrowse(t, db, `INSERT INTO transcript_chunks (video_id, ordinal, text) VALUES (?, 0, 'words')`, r.id)
		}
	}
	return s
}

func mustExecBrowse(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// browseFixture covers every status, an inbox read without downloaded_at, a
// dateless row and a legacy row recorded without a channel id.
func browseFixture(t *testing.T) *Store {
	t.Helper()
	return seedBrowse(t, openTestDB(t), []browseRow{
		{id: "new1", title: "Inbox read about Trail", channelID: "UCa", published: "2026-03-01",
			created: "2026-04-05 10:00:00", status: StatusNew, summary: SummaryDone, transcript: true, chunks: true},
		{id: "queued1", title: "Queued wheel video", channelID: "UCa", published: "2026-02-01",
			created: "2026-04-01 10:00:00", status: StatusQueued, summary: SummaryPending},
		{id: "dl1", title: "Downloading forks", channelID: "UCa", published: "2026-01-01",
			created: "2026-03-01 10:00:00", status: StatusDownloading, summary: SummaryPending},
		{id: "done1", title: "Downloaded TRAIL basics", channelID: "UCa", channelName: "Alpha Own", published: "2025-12-01",
			created: "2025-12-02 10:00:00", downloaded: "2026-04-03 10:00:00", status: StatusDownloaded,
			summary: SummaryDone, transcript: true, chunks: true},
		{id: "tomb1", title: "Tombstoned frame", channelID: "UCa", published: "2025-11-01",
			created: "2025-11-02 10:00:00", downloaded: "2025-11-03 10:00:00", status: StatusTombstoned,
			summary: SummaryError, transcript: true},
		{id: "err1", title: "Failed download", channelID: "UCb", published: "2025-10-01",
			created: "2025-10-02 10:00:00", status: StatusError, summary: SummaryNoTranscript},
		{id: "legacy1", title: "Legacy row", channelName: "Alpha Old", published: "2025-09-01",
			created: "2025-09-02 10:00:00", status: StatusDownloaded, summary: SummaryRunning, transcript: true},
		{id: "nodate1", title: "No release date", channelID: "UCb", created: "2026-04-04 10:00:00",
			status: StatusNew, summary: SummaryPending},
	})
}

func browseIDs(p BrowsePage) []string {
	out := make([]string, len(p.Videos))
	for i, v := range p.Videos {
		out[i] = v.ID
	}
	return out
}

func mustBrowse(t *testing.T, s *Store, opts BrowseOptions) BrowsePage {
	t.Helper()
	p, err := s.Browse(context.Background(), opts)
	if err != nil {
		t.Fatalf("Browse(%+v): %v", opts, err)
	}
	return p
}

// Every row of the videos table is listed, whatever its status: the inbox
// reads (new) and the queue (queued, downloading) are what the Library hides.
func TestBrowse_listsEveryStatus(t *testing.T) {
	s := browseFixture(t)
	p := mustBrowse(t, s, BrowseOptions{Limit: 50})
	if p.Total != 8 || len(p.Videos) != 8 {
		t.Fatalf("total %d, %d videos, want 8: %v", p.Total, len(p.Videos), browseIDs(p))
	}
	statuses := map[string]bool{}
	for _, v := range p.Videos {
		statuses[v.Status] = true
	}
	for _, st := range Statuses {
		if !statuses[st] {
			t.Errorf("status %q not listed: %v", st, statuses)
		}
	}
}

// newest ranks by release date (the dateless row by the day it was recorded),
// oldest the reverse; added_newest by when peeq fetched the file, falling back
// to when it recorded the row; title case-insensitively.
func TestBrowse_sorts(t *testing.T) {
	s := browseFixture(t)
	for sort, want := range map[string][]string{
		"":             {"nodate1", "new1", "queued1", "dl1", "done1", "tomb1", "err1", "legacy1"},
		"newest":       {"nodate1", "new1", "queued1", "dl1", "done1", "tomb1", "err1", "legacy1"},
		"oldest":       {"legacy1", "err1", "tomb1", "done1", "dl1", "queued1", "new1", "nodate1"},
		"added_newest": {"new1", "nodate1", "done1", "queued1", "dl1", "tomb1", "err1", "legacy1"},
		"title":        {"done1", "dl1", "err1", "new1", "legacy1", "nodate1", "queued1", "tomb1"},
		"bogus":        {"nodate1", "new1", "queued1", "dl1", "done1", "tomb1", "err1", "legacy1"},
	} {
		got := browseIDs(mustBrowse(t, s, BrowseOptions{Sort: sort, Limit: 50}))
		if !slices.Equal(got, want) {
			t.Errorf("sort %q = %v, want %v", sort, got, want)
		}
	}
}

// A channel matches by id, and by exact name on rows recorded without one; a
// name never widens an id match onto another channel's rows.
func TestBrowse_channelFilter(t *testing.T) {
	s := browseFixture(t)
	got := browseIDs(mustBrowse(t, s, BrowseOptions{ChannelIDs: []string{"UCa"}, ChannelNames: []string{"Alpha Old"}, Limit: 50}))
	want := []string{"new1", "queued1", "dl1", "done1", "tomb1", "legacy1"}
	if !slices.Equal(got, want) {
		t.Errorf("channel UCa + legacy name = %v, want %v", got, want)
	}
	got = browseIDs(mustBrowse(t, s, BrowseOptions{ChannelIDs: []string{"UCb"}, Limit: 50}))
	if !slices.Equal(got, []string{"nodate1", "err1"}) {
		t.Errorf("channel UCb = %v", got)
	}
	got = browseIDs(mustBrowse(t, s, BrowseOptions{ChannelNames: []string{"Alpha Own"}, Limit: 50}))
	if len(got) != 0 {
		t.Errorf("a name matched a row that has a channel id: %v", got)
	}
}

// The display name is the row's own, else the channels cache, else the id.
func TestBrowse_channelDisplayName(t *testing.T) {
	s := browseFixture(t)
	names := map[string]string{}
	for _, v := range mustBrowse(t, s, BrowseOptions{Limit: 50}).Videos {
		names[v.ID] = v.ChannelName
	}
	for id, want := range map[string]string{"new1": "Alpha Cached", "done1": "Alpha Own", "legacy1": "Alpha Old", "err1": "Beta"} {
		if names[id] != want {
			t.Errorf("%s channel = %q, want %q", id, names[id], want)
		}
	}
}

// The title filter is the Library search box's: a case-insensitive substring,
// LIKE wildcards taken literally.
func TestBrowse_titleFilter(t *testing.T) {
	s := browseFixture(t)
	p := mustBrowse(t, s, BrowseOptions{Title: " trail ", Limit: 50})
	if got := browseIDs(p); !slices.Equal(got, []string{"new1", "done1"}) || p.Total != 2 {
		t.Errorf("title trail = %v (total %d)", got, p.Total)
	}
	if p := mustBrowse(t, s, BrowseOptions{Title: "%", Limit: 50}); p.Total != 0 {
		t.Errorf("a %% matched %v", browseIDs(p))
	}
}

// Release-date bounds are inclusive and use the recorded day for a dateless
// row, as search's filter does.
func TestBrowse_dateBounds(t *testing.T) {
	s := browseFixture(t)
	p := mustBrowse(t, s, BrowseOptions{After: "2025-12-01", Before: "2026-02-01", Limit: 50})
	if got := browseIDs(p); !slices.Equal(got, []string{"queued1", "dl1", "done1"}) || p.Total != 3 {
		t.Errorf("2025-12-01..2026-02-01 = %v (total %d)", got, p.Total)
	}
	p = mustBrowse(t, s, BrowseOptions{After: "2026-04-04", Limit: 50})
	if got := browseIDs(p); !slices.Equal(got, []string{"nodate1"}) {
		t.Errorf("after 2026-04-04 = %v", got)
	}
}

// Pages never repeat or skip a row, ties included: every row here shares its
// release and recorded dates, so only the id orders them.
func TestBrowse_pagingIsTotal(t *testing.T) {
	var rows []browseRow
	for _, id := range []string{"e", "b", "g", "a", "f", "c", "d"} {
		rows = append(rows, browseRow{id: id, title: "Same", channelID: "UCa", published: "2026-01-01",
			created: "2026-01-01 00:00:00", status: StatusNew, summary: SummaryPending})
	}
	s := seedBrowse(t, openTestDB(t), rows)
	for _, sort := range BrowseSorts {
		var all []string
		for offset := 0; offset < 10; offset += 3 {
			p := mustBrowse(t, s, BrowseOptions{Sort: sort, Limit: 3, Offset: offset})
			if p.Total != 7 {
				t.Fatalf("%s offset %d: total %d", sort, offset, p.Total)
			}
			all = append(all, browseIDs(p)...)
		}
		sorted := slices.Clone(all)
		slices.Sort(sorted)
		if !slices.Equal(sorted, []string{"a", "b", "c", "d", "e", "f", "g"}) {
			t.Errorf("%s: pages = %v, want every row once", sort, all)
		}
	}
	// Past the end: no rows, the total still told.
	if p := mustBrowse(t, s, BrowseOptions{Limit: 3, Offset: 20}); len(p.Videos) != 0 || p.Total != 7 {
		t.Errorf("offset past the end = %v, total %d", browseIDs(p), p.Total)
	}
}

// Limit is bounded: never more than BrowseMaxLimit rows, and a zero or
// negative limit still pages.
func TestBrowse_limitIsBounded(t *testing.T) {
	var rows []browseRow
	for i := range BrowseMaxLimit + 5 {
		id := "v" + strings.Repeat("x", i)
		rows = append(rows, browseRow{id: id, title: "t", channelID: "UCa", created: "2026-01-01 00:00:00",
			status: StatusNew, summary: SummaryPending})
	}
	s := seedBrowse(t, openTestDB(t), rows)
	if p := mustBrowse(t, s, BrowseOptions{Limit: 500}); len(p.Videos) != BrowseMaxLimit || p.Total != BrowseMaxLimit+5 {
		t.Errorf("limit 500: %d videos, total %d", len(p.Videos), p.Total)
	}
	if p := mustBrowse(t, s, BrowseOptions{}); len(p.Videos) != 1 {
		t.Errorf("limit 0: %d videos, want 1", len(p.Videos))
	}
}

// Step two fills what makes a video searchable: its summary status, a stored
// transcript and any search-index chunk.
func TestBrowse_searchabilityFlags(t *testing.T) {
	s := browseFixture(t)
	byID := map[string]BrowseVideo{}
	for _, v := range mustBrowse(t, s, BrowseOptions{Limit: 50}).Videos {
		byID[v.ID] = v
	}
	for id, want := range map[string]BrowseVideo{
		"new1":    {SummaryStatus: SummaryDone, HasTranscript: true, Indexed: true},
		"queued1": {SummaryStatus: SummaryPending},
		"tomb1":   {SummaryStatus: SummaryError, HasTranscript: true},
		"err1":    {SummaryStatus: SummaryNoTranscript},
		"legacy1": {SummaryStatus: SummaryRunning, HasTranscript: true},
	} {
		got := byID[id]
		if got.SummaryStatus != want.SummaryStatus || got.HasTranscript != want.HasTranscript || got.Indexed != want.Indexed {
			t.Errorf("%s = %+v, want %+v", id, got, want)
		}
	}
	v := byID["done1"]
	if v.Title != "Downloaded TRAIL basics" || v.PublishedAt != "2025-12-01" || v.DurationSeconds != 90 || v.Status != StatusDownloaded {
		t.Errorf("done1 card fields = %+v", v)
	}
}

func TestBrowse_honoursCancellation(t *testing.T) {
	s := browseFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Browse(ctx, BrowseOptions{Limit: 5}); err == nil {
		t.Error("Browse ignored a cancelled context")
	}
}
