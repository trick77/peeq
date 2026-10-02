package httpapi

import (
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/videos"
)

// The shape of a library that made the Library page slow in production: every
// video carries a stored poster and a few KB of description and analysis text.
const (
	benchVideos   = 500
	benchPending  = 200
	benchChannels = 40
	benchJobs     = 25
	benchPoster   = 80 << 10
	benchBanner   = 150 << 10
)

// seedLibraryBench builds a file-backed database through the real driver and
// returns a server over it plus a signed-in session cookie.
func seedLibraryBench(b *testing.B) (http.Handler, *http.Cookie) {
	b.Helper()
	db, err := store.Open(b.TempDir() + "/bench.db")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		b.Fatal(err)
	}

	poster := make([]byte, benchPoster)
	banner := make([]byte, benchBanner)
	_, _ = rand.Read(poster)
	_, _ = rand.Read(banner)
	description := strings.Repeat("A line of the video's description as YouTube publishes it. ", 50)
	summary := strings.Repeat("One sentence of the stored summary. ", 40)
	chapters := `[` + strings.TrimSuffix(strings.Repeat(`{"title":"A chapter","start":120,"summary":"What the chapter covers, at some length."},`, 12), ",") + `]`
	keyPoints := `[` + strings.TrimSuffix(strings.Repeat(`"A key point drawn from the transcript.",`, 8), ",") + `]`

	vs := videos.New(db)
	cs := channels.New(db)
	ledger := channelvideos.New(db)
	js := jobs.New(db)

	for c := range benchChannels {
		id := fmt.Sprintf("UC%022d", c)
		if err := cs.Upsert(channels.Channel{ID: id, Name: fmt.Sprintf("Channel %d", c)}); err != nil {
			b.Fatal(err)
		}
		if err := cs.Subscribe(id, "2030-01-01 00:00:00"); err != nil {
			b.Fatal(err)
		}
		for _, kind := range []string{"avatar", "banner"} {
			if err := cs.SetImage(id, kind, "image/webp", banner); err != nil {
				b.Fatal(err)
			}
		}
	}
	for i := range benchVideos {
		id := fmt.Sprintf("vid%08d", i)
		if err := vs.Upsert(videos.Video{
			ID: id, URL: "https://www.youtube.com/watch?v=" + id,
			Title:           fmt.Sprintf("Video number %d", i),
			ChannelID:       fmt.Sprintf("UC%022d", i%benchChannels),
			DurationSeconds: 900, PublishedAt: "2026-01-01", Description: description,
		}); err != nil {
			b.Fatal(err)
		}
		if err := vs.SetDownloaded(id, videos.DownloadedResult{MediaPath: id + ".mp4", FilesizeBytes: 1 << 28, FormatUsed: "mp4"}); err != nil {
			b.Fatal(err)
		}
		if err := vs.SetSummary(id, summary, chapters, keyPoints); err != nil {
			b.Fatal(err)
		}
		if err := vs.SetThumbnail(id, "image/webp", poster); err != nil {
			b.Fatal(err)
		}
		if i < benchJobs {
			if _, err := js.Enqueue(id, 0); err != nil {
				b.Fatal(err)
			}
		}
	}
	for i := range benchPending {
		id := fmt.Sprintf("pen%08d", i)
		if err := ledger.Insert(channelvideos.Entry{
			VideoID: id, ChannelID: fmt.Sprintf("UC%022d", i%benchChannels),
			Title: fmt.Sprintf("Pending %d", i), URL: "https://www.youtube.com/watch?v=" + id,
			State: channelvideos.StatePending,
		}); err != nil {
			b.Fatal(err)
		}
		if err := ledger.SetThumbnail(id, "image/webp", poster); err != nil {
			b.Fatal(err)
		}
	}

	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	h := New(Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		Settings:       settings.New(db),
		Videos:         vs,
		Channels:       cs,
		Ledger:         ledger,
		Jobs:           js,
		DevAuthClaims:  auth.Claims{Subject: "dev-tester", PreferredUsername: "dev", Email: "dev@example.local", Name: "Dev Tester"},
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			return h, c
		}
	}
	b.Fatal("no session cookie")
	return nil, nil
}

func benchGet(b *testing.B, h http.Handler, cookie *http.Cookie, path string) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		b.Errorf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.Len()
}

// libraryLoadPaths is what the SPA requests when the Library opens.
var libraryLoadPaths = []string{
	"/api/videos?sort=added_newest",
	"/api/videos/counts",
	"/api/downloads",
	"/api/pending",
	"/api/channels?filter=all",
}

// BenchmarkLibraryLoad measures the Library page's API calls one by one, and
// then fired together the way a page load fires them.
//
//	go test ./internal/httpapi -run '^$' -bench LibraryLoad -benchtime 20x
func BenchmarkLibraryLoad(b *testing.B) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.Cleanup(func() { slog.SetDefault(prev) })
	h, cookie := seedLibraryBench(b)

	for _, path := range libraryLoadPaths {
		b.Run(path, func(b *testing.B) {
			var size int
			for b.Loop() {
				size = benchGet(b, h, cookie, path)
			}
			b.ReportMetric(float64(size), "bytes/resp")
		})
	}
	b.Run("page-load-parallel", func(b *testing.B) {
		for b.Loop() {
			var wg sync.WaitGroup
			for _, path := range libraryLoadPaths {
				wg.Go(func() { benchGet(b, h, cookie, path) })
			}
			for i := range 10 {
				wg.Go(func() { benchGet(b, h, cookie, fmt.Sprintf("/api/videos/vid%08d/thumbnail", i)) })
			}
			wg.Wait()
		}
	})
}
