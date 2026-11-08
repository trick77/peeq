package sharelink

// Link is a share link as stored: the raw token, the video it grants access to,
// its expiry (empty string = never), and when it was created.
//
// Table: share_links
type Link struct {
	Token     string
	VideoID   string
	ExpiresAt string
	CreatedAt string
}
