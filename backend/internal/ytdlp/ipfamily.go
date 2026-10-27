package ytdlp

// IP families settings.ytdlp_ip_family may hold. Empty leaves the choice to
// yt-dlp, which in practice means IPv4.
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
