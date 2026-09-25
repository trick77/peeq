package ytgate

import (
	"context"
	"testing"

	"github.com/trick77/peeq/internal/settings"
)

func TestCookieAllows(t *testing.T) {
	cases := []struct {
		status    string
		anonymous bool
		want      bool
	}{
		{settings.CookieValid, false, true},
		{settings.CookieValid, true, true},
		{settings.CookieAbsent, false, false},
		{settings.CookieAbsent, true, true},
		// A rejected cookie is a signal, not an absence: anonymous mode does
		// not relax it, exactly like ytdlp.Runner's cookieGate.
		{settings.CookieStale, true, false},
		{settings.CookieBlocked, true, false},
		{settings.CookieStale, false, false},
	}
	for _, c := range cases {
		if got := CookieAllows(c.status, c.anonymous); got != c.want {
			t.Errorf("CookieAllows(%q, anonymous=%v) = %v, want %v", c.status, c.anonymous, got, c.want)
		}
	}
}

func TestGate_Open(t *testing.T) {
	ctx := context.Background()
	status := func(s string) func(context.Context) string { return func(context.Context) string { return s } }
	paused := func(p bool) func(context.Context) bool { return func(context.Context) bool { return p } }
	cases := []struct {
		name string
		gate Gate
		want bool
	}{
		{"valid cookie, no switch", Gate{CookieStatus: status(settings.CookieValid)}, true},
		{"absent cookie", Gate{CookieStatus: status(settings.CookieAbsent)}, false},
		{"absent cookie, anonymous allowed", Gate{CookieStatus: status(settings.CookieAbsent), AllowAnonymous: true}, true},
		{"stale cookie, anonymous allowed", Gate{CookieStatus: status(settings.CookieStale), AllowAnonymous: true}, false},
		{"valid cookie, paused", Gate{CookieStatus: status(settings.CookieValid), Paused: paused(true)}, false},
		{"valid cookie, not paused", Gate{CookieStatus: status(settings.CookieValid), Paused: paused(false)}, true},
		{"anonymous but paused", Gate{CookieStatus: status(settings.CookieAbsent), AllowAnonymous: true, Paused: paused(true)}, false},
	}
	for _, c := range cases {
		if got := c.gate.Open(ctx); got != c.want {
			t.Errorf("%s: Open = %v, want %v", c.name, got, c.want)
		}
	}
}

// A gate without a cookie source must fail loud, not open.
func TestGate_nilCookieStatusPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a nil CookieStatus must not fail open")
		}
	}()
	Gate{}.Open(context.Background())
}
