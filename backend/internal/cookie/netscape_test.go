package cookie_test

import (
	"testing"

	"github.com/trick77/vark/internal/cookie"
)

func TestValidate_rejectsGarbage(t *testing.T) {
	if cookie.Validate("hello") == nil {
		t.Fatal("garbage must fail")
	}
}

func TestValidate_rejectsNoYoutube(t *testing.T) {
	if cookie.Validate("# Netscape HTTP Cookie File\n.example.com\tTRUE\t/\tTRUE\t0\tX\ty") == nil {
		t.Fatal("cookie file with no .youtube.com lines must fail")
	}
}

func TestValidate_acceptsMinimalYoutube(t *testing.T) {
	ok := "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1789000000\tSID\tabc\n.youtube.com\tTRUE\t/\tTRUE\t1789000000\t__Secure-3PSID\tdef\n"
	if err := cookie.Validate(ok); err != nil {
		t.Fatalf("valid cookie rejected: %v", err)
	}
}

// TestValidate_acceptsHttpOnlyPrefixedYoutube covers real browser/extension
// exports: YouTube's session cookies (SID, __Secure-*SID) are HttpOnly, so
// genuine exports mark those lines with a "#HttpOnly_" domain prefix rather
// than a bare domain. Those lines must still count as data, not be skipped
// as comments.
func TestValidate_acceptsHttpOnlyPrefixedYoutube(t *testing.T) {
	ok := "# Netscape HTTP Cookie File\n" +
		"#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t1789000000\tSID\tabc\n" +
		"#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t1789000000\t__Secure-3PSID\tdef\n"
	if err := cookie.Validate(ok); err != nil {
		t.Fatalf("valid HttpOnly-prefixed cookie rejected: %v", err)
	}
}
