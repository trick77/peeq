// Package cookie parses and validates Netscape-format cookie files, the
// format yt-dlp expects for authenticated YouTube requests (--cookies).
package cookie

import (
	"fmt"
	"strconv"
	"strings"
)

// Cookie is a single parsed Netscape cookie-file entry.
type Cookie struct {
	Domain      string
	IncludeSubs bool
	Path        string
	Secure      bool
	Expiry      int64
	Name        string
	Value       string
}

// Cookies is a parsed Netscape cookie file.
type Cookies []Cookie

// youtubeSessionCookieNames are the cookie names that indicate an
// authenticated YouTube session. yt-dlp needs at least one of these to use
// the cookie jar for anything beyond anonymous requests.
var youtubeSessionCookieNames = map[string]bool{
	"SID":            true,
	"__Secure-3PSID": true,
	"__Secure-1PSID": true,
}

// Parse parses a Netscape-format cookie file: tab-separated fields
// (domain, includeSubdomains, path, secure, expiry, name, value), one
// cookie per line. Comment lines (starting with '#') and blank lines are
// skipped. Returns an error if no data lines parse into valid cookie
// entries at all (i.e. the text isn't a Netscape cookie file).
func Parse(text string) (Cookies, error) {
	var cookies Cookies
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		line = strings.TrimRight(line, "\r")
		// Netscape cookie files mark HttpOnly cookies with a "#HttpOnly_"
		// prefix on the domain field rather than a real comment. YouTube's
		// session cookies (SID, __Secure-*SID) are HttpOnly, so real
		// browser/extension exports have this prefix on exactly the lines
		// Validate cares about most — strip it before the comment check so
		// those lines are still treated as data, not skipped.
		line = strings.TrimPrefix(line, "#HttpOnly_")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 7 {
			return nil, fmt.Errorf("netscape cookie parse: line %d: expected 7 tab-separated fields, got %d", i+1, len(fields))
		}
		expiry, err := strconv.ParseInt(fields[4], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("netscape cookie parse: line %d: invalid expiry %q: %w", i+1, fields[4], err)
		}
		cookies = append(cookies, Cookie{
			Domain:      fields[0],
			IncludeSubs: strings.EqualFold(fields[1], "TRUE"),
			Path:        fields[2],
			Secure:      strings.EqualFold(fields[3], "TRUE"),
			Expiry:      expiry,
			Name:        fields[5],
			Value:       fields[6],
		})
	}
	if len(cookies) == 0 {
		return nil, fmt.Errorf("netscape cookie parse: no cookie entries found")
	}
	return cookies, nil
}

// Validate parses text as a Netscape cookie file and confirms it contains a
// usable YouTube session: at least one ".youtube.com" (or "youtube.com")
// line, and at least one recognized session cookie name among them.
func Validate(text string) error {
	cookies, err := Parse(text)
	if err != nil {
		return err
	}

	var sawYouTube bool
	var sawSessionCookie bool
	for _, c := range cookies {
		domain := strings.TrimPrefix(c.Domain, ".")
		if domain != "youtube.com" && !strings.HasSuffix(domain, ".youtube.com") {
			continue
		}
		sawYouTube = true
		if youtubeSessionCookieNames[c.Name] {
			sawSessionCookie = true
		}
	}

	if !sawYouTube {
		return fmt.Errorf("cookie file has no .youtube.com entries")
	}
	if !sawSessionCookie {
		return fmt.Errorf("cookie file has no YouTube session cookie (need one of SID, __Secure-3PSID, __Secure-1PSID)")
	}
	return nil
}
