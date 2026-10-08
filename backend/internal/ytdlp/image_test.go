package ytdlp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func imageServer(t *testing.T, hits *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff jpeg"))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestFetchImage_takesItsTurn: an image request to YouTube's CDN is a YouTube
// call like any other. It waits for the running yt-dlp to exit and for the gap
// after it, and the next call is spaced from it in turn.
func TestFetchImage_takesItsTurn(t *testing.T) {
	var hits atomic.Int32
	url := imageServer(t, &hits)
	c := newClock()
	var waits []time.Duration
	r := noJitterRunner(c, func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil })

	release, err := r.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := r.FetchImage(context.Background(), url); done <- err }()
	waitQueued(t, r, 1)
	if hits.Load() != 0 {
		t.Fatal("image fetched while another call held the turn")
	}
	release(true)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
	if err := r.paceOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := waits[len(waits)-1]; got != 20*time.Second {
		t.Fatalf("call after an image fetch waited %v, want the 20s gap", got)
	}
}

// TestFetchImage_gated: no cookie or the kill switch means no request, like
// every other YouTube call.
func TestFetchImage_gated(t *testing.T) {
	var hits atomic.Int32
	url := imageServer(t, &hits)
	for name, cfg := range map[string]RunnerConfig{
		"no cookie": {},
		"paused": {
			CookieProvider: func() (string, string) { return "c", "valid" },
			PauseProvider:  func() (bool, string) { return true, "" },
		},
	} {
		cfg.Sleep = func(context.Context, time.Duration) error { return nil }
		r := New(cfg)
		_, _, err := r.FetchImage(context.Background(), url)
		var refused *RefusedError
		if !errors.As(err, &refused) {
			t.Fatalf("%s: err = %v, want a RefusedError", name, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("a refused image fetch reached the server %d times", hits.Load())
	}
}

// TestFetchImage_emptyURLTakesNoTurn: a channel without a banner is normal and
// must not cost a turn.
func TestFetchImage_emptyURLTakesNoTurn(t *testing.T) {
	slept := false
	r := noJitterRunner(newClock(), func(context.Context, time.Duration) error { slept = true; return nil })
	mime, data, err := r.FetchImage(context.Background(), "")
	if err != nil || mime != "" || data != nil || slept {
		t.Fatalf("empty url: %q %v %v slept=%v, want nothing at all", mime, data, err, slept)
	}
}
