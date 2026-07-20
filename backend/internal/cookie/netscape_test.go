package cookie_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/cookie"
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

// TestParse_extensionOutput locks the peeq Companion extension's JavaScript
// serializer to this Go parser. They are two implementations of one file
// format in two languages with nothing linking them at compile time, so the
// fixture is the contract. Regenerate it with:
//   cd extension && node testdata/generate_fixture.js > ../backend/internal/cookie/testdata/extension_output.txt
func TestParse_extensionOutput(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "extension_output.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	text := string(raw)

	// The whole point: extension output must satisfy the real validator.
	if err := cookie.Validate(text); err != nil {
		t.Fatalf("Validate rejected extension output: %v", err)
	}

	cookies, err := cookie.Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cookies) != 7 {
		t.Fatalf("parsed %d cookies, want 7", len(cookies))
	}

	byName := make(map[string]cookie.Cookie, len(cookies))
	for _, c := range cookies {
		byName[c.Name] = c
		if c.Domain == ".google.com" {
			t.Fatal("non-YouTube cookie leaked into the serialized jar")
		}
	}

	// PREF is secure:false httpOnly:false; SAPISID is secure:true
	// httpOnly:false. Together they prove column 4 carries `secure` and not
	// `httpOnly` — the exact TubeArchivist bug this guards against.
	if byName["PREF"].Secure {
		t.Error("PREF: Secure = true, want false (column 4 is not httpOnly)")
	}
	if !byName["SAPISID"].Secure {
		t.Error("SAPISID: Secure = false, want true")
	}
	// Float expiry must arrive truncated, not rounded or mangled.
	if got := byName["PREF"].Expiry; got != 1819099943 {
		t.Errorf("PREF: Expiry = %d, want 1819099943", got)
	}
	// A session cookie carries expiry 0.
	if got := byName["YSC"].Expiry; got != 0 {
		t.Errorf("YSC: Expiry = %d, want 0", got)
	}
	// The #HttpOnly_ prefix must be stripped, leaving a clean domain.
	if got := byName["SID"].Domain; got != ".youtube.com" {
		t.Errorf("SID: Domain = %q, want %q", got, ".youtube.com")
	}

	// The parsed Domain is identical whether or not #HttpOnly_ was emitted,
	// because Parse strips it. Assert on the raw text too, or a serializer
	// that stopped emitting the prefix would slip through this lock.
	if !strings.Contains(text, "#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t") {
		t.Error("fixture has no #HttpOnly_ prefixed line; the serializer stopped marking httpOnly cookies")
	}
}
