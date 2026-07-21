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
