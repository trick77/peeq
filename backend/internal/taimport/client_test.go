package taimport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
