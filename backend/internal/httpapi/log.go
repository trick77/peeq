package httpapi

import (
	"errors"
	"net/url"
)

// redactErr strips credentials from *url.Error values before they reach the
// logs. This matters most on the OIDC path: the callback URL carries a live
// auth code and state, and transport errors embed the URL verbatim. Errors
// that aren't *url.Error pass through untouched.
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return err
	}
	if u, parseErr := url.Parse(urlErr.URL); parseErr == nil && u.Host != "" {
		u.RawQuery = ""
		u.User = nil
		urlErr.URL = u.String()
	}
	return err
}
