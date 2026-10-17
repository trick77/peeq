package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/ship/kit/httpx"
)

// compose probes /readyz from inside the container (curl 127.0.0.1): it must
// fail when the database does, and the probes stay hidden from anyone else.
func TestProbes(t *testing.T) {
	dbErr := errors.New("database is locked")
	deps, _ := shareMetaDeps(t)
	deps.Ready = []httpx.Check{{Name: "db", Run: func(context.Context) error { return dbErr }}}
	h := New(deps)
	probe := func(path, peer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = peer
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := probe("/healthz", "127.0.0.1:4000"); rec.Code != http.StatusOK {
		t.Errorf("/healthz = %d, want 200", rec.Code)
	}
	rec := probe("/readyz", "127.0.0.1:4000")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"db"`) {
		t.Errorf("/readyz with a failing db = %d %s, want 503 naming db", rec.Code, rec.Body.String())
	}
	dbErr = nil
	if rec := probe("/readyz", "127.0.0.1:4000"); rec.Code != http.StatusOK {
		t.Errorf("/readyz with a healthy db = %d, want 200", rec.Code)
	}
	if rec := probe("/readyz", "198.51.100.7:4000"); rec.Code != http.StatusNotFound {
		t.Errorf("/readyz from outside = %d, want 404", rec.Code)
	}
}
