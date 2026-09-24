package media

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFetchImageBytes_errorOmitsQueryString: a transport error renders the
// URL, and a CDN thumbnail URL carries a signed query string. The error the
// callers log must not.
func TestFetchImageBytes_errorOmitsQueryString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Hijack and drop the connection so the client sees a transport error.
		// Not t.Fatal: this runs on the server goroutine, where Fatal would
		// only stall the client until its timeout.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("recorder is not a hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	_, _, err := FetchImageBytes(context.Background(), srv.URL+"/vi/abc/hqdefault.jpg?sqp=SECRET&rs=ALSO")
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error carries the query string: %s", err)
	}
	if !strings.Contains(err.Error(), "/vi/abc/hqdefault.jpg") {
		t.Fatalf("error lost the path: %s", err)
	}
}
