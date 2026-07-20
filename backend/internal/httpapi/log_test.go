package httpapi

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestRedactErr_stripsQueryAndUserinfoFromURLError(t *testing.T) {
	// Given: a url.Error whose URL carries an OIDC code in the query string.
	given := &url.Error{
		Op:  "Post",
		URL: "https://auth.example.com/token?code=SECRET123&state=abc",
		Err: errors.New("connection refused"),
	}

	// When
	got := redactErr(given).Error()

	// Then
	if strings.Contains(got, "SECRET123") {
		t.Fatalf("redactErr() leaked the code: %s", got)
	}
	if !strings.Contains(got, "auth.example.com/token") {
		t.Fatalf("redactErr() dropped the useful part: %s", got)
	}
}

func TestRedactErr_stripsUserinfo(t *testing.T) {
	// Given
	given := &url.Error{Op: "Get", URL: "https://user:pw@example.com/x", Err: errors.New("boom")}

	// When
	got := redactErr(given).Error()

	// Then
	if strings.Contains(got, "pw") {
		t.Fatalf("redactErr() leaked userinfo: %s", got)
	}
}

func TestRedactErr_passesThroughPlainErrors(t *testing.T) {
	// Given
	given := errors.New("plain failure")

	// When
	got := redactErr(given)

	// Then
	if got.Error() != "plain failure" {
		t.Fatalf("redactErr() = %q, want %q", got.Error(), "plain failure")
	}
}

func TestRedactErr_nilIsNil(t *testing.T) {
	if redactErr(nil) != nil {
		t.Fatal("redactErr(nil) should be nil")
	}
}
