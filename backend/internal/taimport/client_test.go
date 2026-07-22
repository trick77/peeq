package taimport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChannelPage_parsesDataAndSendsTokenAuth(t *testing.T) {
	var gotAuth, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"channel_id":"UC_aaa","channel_name":"Alpha","channel_active":true,"channel_subscribed":true},
			{"channel_id":"UC_bbb","channel_name":"Beta","channel_active":false,"channel_subscribed":true}
		]}`))
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "secret-token", srv.Client())

	got, more, err := testee.ChannelPage(context.Background(), 1)
	if err != nil {
		t.Fatalf("ChannelPage: %v", err)
	}
	if !more {
		t.Error("more = false, want true (page was non-empty)")
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "UC_aaa" || got[0].Name != "Alpha" || !got[0].Active || !got[0].Subscribed {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Active {
		t.Error("got[1].Active = true, want false")
	}

	// DRF token scheme, not Bearer.
	if gotAuth != "Token secret-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Token secret-token")
	}
	if gotPath != "/api/channel/" {
		t.Errorf("path = %q, want /api/channel/", gotPath)
	}
	// Only subscribed channels: TA indexes a channel for every video ever
	// downloaded, so an unfiltered import would subscribe to channels that
	// were never followed.
	if gotQuery != "filter=subscribed&page=1" {
		t.Errorf("query = %q, want filter=subscribed&page=1", gotQuery)
	}
}

func TestChannelPage_404MeansNoMorePages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"no results"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())

	got, more, err := testee.ChannelPage(context.Background(), 9)
	if err != nil {
		t.Fatalf("ChannelPage: %v", err)
	}
	if more {
		t.Error("more = true, want false on 404")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestChannelPage_emptyDataMeansNoMorePages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())

	_, more, err := testee.ChannelPage(context.Background(), 3)
	if err != nil {
		t.Fatalf("ChannelPage: %v", err)
	}
	if more {
		t.Error("more = true, want false on empty data")
	}
}

func TestChannelPage_malformedJSONIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":`)) // truncated / invalid JSON
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())

	_, _, err := testee.ChannelPage(context.Background(), 1)
	if err == nil {
		t.Fatal("err = nil, want an error decoding malformed JSON")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("err = %q, want it to mention decode", err)
	}
}

func TestChannelPage_connectionFailureIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // closed before use: any request against it fails at the transport level

	testee := NewClient(srv.URL, "t", srv.Client())

	_, _, err := testee.ChannelPage(context.Background(), 1)
	if err == nil {
		t.Fatal("err = nil, want an error when the connection itself fails")
	}
	if !strings.Contains(err.Error(), "GET") {
		t.Errorf("err = %q, want it to mention the failed GET", err)
	}
}

func TestChannelPage_serverErrorIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())

	if _, _, err := testee.ChannelPage(context.Background(), 1); err == nil {
		t.Fatal("err = nil, want an error on HTTP 500")
	}
}

func TestChannelPage_unauthorizedMentionsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())

	_, _, err := testee.ChannelPage(context.Background(), 1)
	if err == nil {
		t.Fatal("err = nil, want an error on HTTP 401")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Errorf("err = %q, want it to mention the token", err)
	}
}

func TestAllChannels_walksUntilEmptyPage(t *testing.T) {
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("page")
		pages = append(pages, p)
		switch p {
		case "1":
			_, _ = w.Write([]byte(`{"data":[{"channel_id":"UC_a","channel_name":"A","channel_subscribed":true,"channel_active":true}]}`))
		case "2":
			_, _ = w.Write([]byte(`{"data":[{"channel_id":"UC_b","channel_name":"B","channel_subscribed":true,"channel_active":true}]}`))
		default:
			http.Error(w, `{"error":"none"}`, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())

	got, err := testee.AllChannels(context.Background())
	if err != nil {
		t.Fatalf("AllChannels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ID != "UC_a" || got[1].ID != "UC_b" {
		t.Errorf("got = %+v", got)
	}
	if len(pages) != 3 {
		t.Errorf("requested pages = %v, want three requests (1, 2, then the empty 3)", pages)
	}
}

func TestAllChannels_propagatesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())

	if _, err := testee.AllChannels(context.Background()); err == nil {
		t.Fatal("err = nil, want the page error propagated")
	}
}

func TestAllChannels_capExhaustionErrorsRatherThanTruncating(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always return a non-empty page so the walk never terminates naturally.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"channel_id":"UC_ch","channel_name":"Channel","channel_subscribed":true,"channel_active":true}]}`))
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())

	got, err := testee.AllChannels(context.Background())
	if err == nil {
		t.Fatal("err = nil, want an error when cap is exhausted")
	}
	if got != nil {
		t.Errorf("got = %+v, want nil (must not hand back a truncated list)", got)
	}
	if !strings.Contains(err.Error(), "2000") {
		t.Errorf("err = %q, want it to mention the page cap (2000)", err)
	}
}

func TestVideoPage_mapsFieldsAndSendsWatchFilter(t *testing.T) {
	var gotAuth, gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			_, _ = w.Write([]byte(`{"data":[{
				"youtube_id":"vid1","title":"A Title","published":"2024-05-01","vid_type":"videos",
				"channel":{"channel_id":"UC1","channel_name":"Chan"},
				"player":{"duration":600,"position":123.5,"watched":false},
				"subtitles":[{"lang":"en"},{"lang":"de"},{"lang":""}]
			}],"paginate":{}}`))
			return
		}
		// Any later page: the list endpoint ends with 200 + empty data (no 404).
		_, _ = w.Write([]byte(`{"data":[],"paginate":{}}`))
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "secret", srv.Client())
	vids, more, err := testee.VideoPage(context.Background(), "UC1", "continue", 1)
	if err != nil {
		t.Fatalf("VideoPage: %v", err)
	}
	if gotAuth != "Token secret" {
		t.Errorf("auth = %q, want Token scheme", gotAuth)
	}
	if gotPath != "/api/video/" {
		t.Errorf("path = %q", gotPath)
	}
	for _, want := range []string{"channel=UC1", "watch=continue", "page=1"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	if !more || len(vids) != 1 {
		t.Fatalf("more=%v n=%d, want true and 1", more, len(vids))
	}
	v := vids[0]
	if v.ID != "vid1" || v.ChannelID != "UC1" || v.ChannelName != "Chan" || v.Title != "A Title" ||
		v.Published != "2024-05-01" || v.DurationSeconds != 600 || v.Position != 123.5 || v.VidType != "videos" {
		t.Fatalf("mapped video = %+v", v)
	}
	// Empty-lang entries are dropped; order preserved.
	if len(v.SubtitleLangs) != 2 || v.SubtitleLangs[0] != "en" || v.SubtitleLangs[1] != "de" {
		t.Fatalf("subtitle langs = %v, want [en de]", v.SubtitleLangs)
	}

	// A later page with empty data terminates the walk.
	_, more2, err := testee.VideoPage(context.Background(), "UC1", "continue", 2)
	if err != nil {
		t.Fatalf("VideoPage p2: %v", err)
	}
	if more2 {
		t.Fatal("more = true on an empty page, want false")
	}
}

func TestFlexDate_acceptsEpochAndString(t *testing.T) {
	var got videoDoc
	if err := json.Unmarshal([]byte(`{"published":"2023-01-02"}`), &got); err != nil {
		t.Fatalf("string form: %v", err)
	}
	if string(got.Published) != "2023-01-02" {
		t.Errorf("string published = %q", got.Published)
	}

	const epoch = int64(1714521600)
	want := time.Unix(epoch, 0).UTC().Format("2006-01-02")
	got = videoDoc{}
	if err := json.Unmarshal([]byte(`{"published":1714521600}`), &got); err != nil {
		t.Fatalf("epoch form: %v", err)
	}
	if string(got.Published) != want {
		t.Errorf("epoch published = %q, want %q", got.Published, want)
	}

	// TubeArchivist's REST layer returns a full ISO timestamp; only the date
	// part is kept so imported rows match native YYYY-MM-DD.
	got = videoDoc{}
	if err := json.Unmarshal([]byte(`{"published":"2023-01-15T13:45:30+00:00"}`), &got); err != nil {
		t.Fatalf("iso form: %v", err)
	}
	if string(got.Published) != "2023-01-15" {
		t.Errorf("iso published = %q, want 2023-01-15 (date only)", got.Published)
	}
}

func TestChannelVideos_walksUntilEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = w.Write([]byte(`{"data":[{"youtube_id":"a","channel":{"channel_id":"UC1"},"player":{}}]}`))
		case "2":
			_, _ = w.Write([]byte(`{"data":[{"youtube_id":"b","channel":{"channel_id":"UC1"},"player":{}}]}`))
		default:
			_, _ = w.Write([]byte(`{"data":[]}`))
		}
	}))
	defer srv.Close()

	testee := NewClient(srv.URL, "t", srv.Client())
	vids, err := testee.ChannelVideos(context.Background(), "UC1", "unwatched")
	if err != nil {
		t.Fatalf("ChannelVideos: %v", err)
	}
	if len(vids) != 2 || vids[0].ID != "a" || vids[1].ID != "b" {
		t.Fatalf("videos = %+v, want a,b across two pages", vids)
	}
}
