package ytdlp

import (
	"context"
	"net"
	"net/http"
	"time"
)

// IP families settings.ytdlp_ip_family may hold. Empty leaves the choice to
// yt-dlp (and the host's resolver order).
//
// The setting exists because YouTube blocks by address, and per family: a
// server's IPv4 address can answer every call, anonymous or not, with
// "Video unavailable. This content isn't available." while its IPv6 one,
// with the same cookie, still plays. No cookie and no yt-dlp update gets past
// a blocked address; pinning the other family does.
const (
	IPFamilyAuto = ""
	IPFamilyV4   = "4"
	IPFamilyV6   = "6"
)

// IsIPFamily reports whether v is a value settings.ytdlp_ip_family accepts.
func IsIPFamily(v string) bool {
	switch v {
	case IPFamilyAuto, IPFamilyV4, IPFamilyV6:
		return true
	}
	return false
}

// imageClients are the HTTP clients FetchImage uses, one per family. Built
// once: each owns a Transport and its idle connections.
var imageClients = map[string]*http.Client{
	IPFamilyV4: familyClient("tcp4"),
	IPFamilyV6: familyClient("tcp6"),
}

// imageClient is the client that dials only family, or http.DefaultClient for
// automatic (and, as with ipFamilyArgs, for an unknown value).
func imageClient(family string) *http.Client {
	if c, ok := imageClients[family]; ok {
		return c
	}
	return http.DefaultClient
}

func familyClient(network string) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	tr.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
		return d.DialContext(ctx, network, addr)
	}
	return &http.Client{Transport: tr}
}

// ipFamilyArgs is the yt-dlp flag that pins family, or nothing. An unknown
// value pins nothing: yt-dlp's own choice is safer than a guessed flag.
func ipFamilyArgs(family string) []string {
	switch family {
	case IPFamilyV4:
		return []string{"--force-ipv4"}
	case IPFamilyV6:
		return []string{"--force-ipv6"}
	}
	return nil
}
