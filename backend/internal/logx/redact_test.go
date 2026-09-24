package logx

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"testing"
)

func TestRedactErr_stripsQueryAndUserinfoFromURLError(t *testing.T) {
	given := &url.Error{
		Op:  "Post",
		URL: "https://auth.example.com/token?code=SECRET123&state=abc",
		Err: errors.New("connection refused"),
	}
	got := RedactErr(given).Error()
	if strings.Contains(got, "SECRET123") {
		t.Fatalf("RedactErr() leaked the code: %s", got)
	}
	if !strings.Contains(got, "auth.example.com/token") {
		t.Fatalf("RedactErr() dropped the useful part: %s", got)
	}
}

func TestRedactErr_stripsUserinfo(t *testing.T) {
	given := &url.Error{Op: "Get", URL: "https://user:pw@example.com/x", Err: errors.New("boom")}
	if got := RedactErr(given).Error(); strings.Contains(got, "pw") {
		t.Fatalf("RedactErr() leaked userinfo: %s", got)
	}
}

func TestRedactErr_passesThroughPlainErrors(t *testing.T) {
	given := errors.New("plain failure")
	got := RedactErr(given)
	if got.Error() != "plain failure" {
		t.Fatalf("RedactErr() = %q, want %q", got.Error(), "plain failure")
	}
	if !errors.Is(got, given) {
		t.Fatal("RedactErr() replaced an error it did not redact")
	}
}

func TestRedactErr_nilIsNil(t *testing.T) {
	if RedactErr(nil) != nil {
		t.Fatal("RedactErr(nil) should be nil")
	}
}

func TestRedactErr_redactsThroughWrapping(t *testing.T) {
	// fmt.Errorf("...: %w", err) renders and freezes the wrapped error's
	// message at wrap time, so an implementation that only mutated the inner
	// *url.Error's fields (rather than the rendered string) would fail here.
	urlErr := &url.Error{
		Op:  "Post",
		URL: "https://auth.example.com/token?code=SECRET123&state=abc",
		Err: errors.New("connection refused"),
	}
	given := fmt.Errorf("exchange oidc code: %w", urlErr)
	got := RedactErr(given).Error()
	if strings.Contains(got, "SECRET123") {
		t.Fatalf("RedactErr() leaked the code through a wrapped error: %s", got)
	}
	if !strings.Contains(got, "auth.example.com/token") {
		t.Fatalf("RedactErr() dropped the useful part: %s", got)
	}
	if !strings.Contains(got, "exchange oidc code") {
		t.Fatalf("RedactErr() dropped the outer wrap context: %s", got)
	}
}

// The chain survives redaction, which is what lets RedactErr sit at a wrap
// site rather than only at a log site.
func TestRedactErr_keepsTheChain(t *testing.T) {
	sentinel := errors.New("refused")
	urlErr := &url.Error{Op: "Get", URL: "https://cdn.example.com/a.jpg?sig=SECRET", Err: sentinel}
	got := RedactErr(fmt.Errorf("fetch image: %w", urlErr))
	if !errors.Is(got, sentinel) {
		t.Fatal("errors.Is lost the sentinel through redaction")
	}
	var ue *url.Error
	if !errors.As(got, &ue) {
		t.Fatal("errors.As lost the *url.Error through redaction")
	}
	if strings.Contains(got.Error(), "SECRET") {
		t.Fatalf("message still carries the query: %s", got.Error())
	}
}

// TestRedactAttr_scrubsErrAttrsOnTheHandler: with RedactAttr installed on the
// handler, an unwrapped *url.Error logged under "err" never reaches the output
// with its query string, whatever the call site did.
func TestRedactAttr_scrubsErrAttrsOnTheHandler(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: RedactAttr}))
	urlErr := &url.Error{Op: "Get", URL: "https://i.ytimg.com/vi/x/hq.jpg?sqp=SECRET", Err: errors.New("dial tcp: refused")}
	logger.Warn("fetch failed", "video_id", "x", "err", urlErr)
	logger.Warn("other key untouched", "cause", "plain text")
	out := buf.String()
	if strings.Contains(out, "SECRET") {
		t.Fatalf("query string reached the log: %s", out)
	}
	if !strings.Contains(out, "i.ytimg.com/vi/x/hq.jpg") {
		t.Fatalf("path was lost: %s", out)
	}
	if !strings.Contains(out, "cause=\"plain text\"") {
		t.Fatalf("a non-err attr was altered: %s", out)
	}
}
