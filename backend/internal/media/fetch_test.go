package media

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchImageBytes_returnsBodyAndMime asserts the caller gets the bytes and
// the content type to store them under, and that nothing is written to disk on
// the way — since migration 0023 every image peeq caches lives in a row.
func TestFetchImageBytes_returnsBodyAndMime(t *testing.T) {
	body := []byte("\xff\xd8\xff fake jpeg bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	mime, got, err := FetchImageBytes(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mime)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

// TestFetchImageBytes_toleratesCharsetParameter asserts the mime is taken from
// the type alone: a server that appends "; charset=..." must not make a
// perfectly good image look like an unknown type.
func TestFetchImageBytes_toleratesCharsetParameter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/webp; charset=binary")
		_, _ = w.Write([]byte("RIFFfake"))
	}))
	defer srv.Close()

	mime, _, err := FetchImageBytes(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if mime != "image/webp" {
		t.Fatalf("mime = %q, want image/webp", mime)
	}
}

// TestFetchImageBytes_rejectsNonImage asserts an HTML error page served with a
// 200 is not handed back as if it were an avatar.
func TestFetchImageBytes_rejectsNonImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>nope</html>"))
	}))
	defer srv.Close()

	_, _, err := FetchImageBytes(context.Background(), srv.URL)
	if !errors.Is(err, ErrUnsupportedContentType) {
		t.Fatalf("err = %v, want ErrUnsupportedContentType", err)
	}
}

// TestFetchImageBytes_rejectsOversizeBody asserts a hostile or broken server
// cannot bloat the database through this path.
func TestFetchImageBytes_rejectsOversizeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(make([]byte, maxImageBytes+1024))
	}))
	defer srv.Close()

	if _, _, err := FetchImageBytes(context.Background(), srv.URL); err == nil {
		t.Fatal("expected an error for an oversize body")
	}
}

// TestFetchImageBytes_emptyURL asserts a channel with no banner is a no-op
// rather than an error the caller has to special-case.
func TestFetchImageBytes_emptyURL(t *testing.T) {
	mime, data, err := FetchImageBytes(context.Background(), "")
	if err != nil {
		t.Fatalf("empty url should not error: %v", err)
	}
	if mime != "" || data != nil {
		t.Fatalf("empty url returned %q/%v, want nothing", mime, data)
	}
}

// TestFetchImageBytes_rejectsMalformedURL covers the request-construction
// failure branch.
func TestFetchImageBytes_rejectsMalformedURL(t *testing.T) {
	if _, _, err := FetchImageBytes(context.Background(), "http://\x7f/bad"); err == nil {
		t.Fatal("expected an error for a malformed url")
	}
}

// TestFetchImageBytes_rejectsUnreachableHost covers the transport failure
// branch — a network error, which the pending-thumbnail retry treats as
// transient rather than permanent.
func TestFetchImageBytes_rejectsUnreachableHost(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing is listening now

	if _, _, err := FetchImageBytes(context.Background(), "http://"+addr+"/x.jpg"); err == nil {
		t.Fatal("expected an error for an unreachable host")
	}
}

// TestFetchImageBytes_rejectsNon200Status asserts the status is carried in a
// typed error, which is what lets a caller tell a permanent 404 (try the next
// candidate) from a 5xx (worth retrying).
func TestFetchImageBytes_rejectsNon200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, _, err := FetchImageBytes(context.Background(), srv.URL)
	var se *FetchStatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusNotFound {
		t.Fatalf("err = %v, want a FetchStatusError carrying 404", err)
	}
}

// TestFetchImageBytes_rejectsTruncatedBody asserts a connection that dies
// mid-body is an error rather than a half image the caller would store.
func TestFetchImageBytes_rejectsTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Length", "1024")
		_, _ = w.Write([]byte("short"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Hijack and close without writing the promised remainder.
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, herr := hj.Hijack()
			if herr == nil {
				conn.Close()
			}
		}
	}))
	defer srv.Close()

	if _, _, err := FetchImageBytes(context.Background(), srv.URL); err == nil {
		t.Fatal("expected an error for a truncated body")
	}
}
