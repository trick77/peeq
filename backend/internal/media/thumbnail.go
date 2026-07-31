package media

import (
	"path/filepath"
	"strings"
)

// ThumbnailExts are the extensions yt-dlp's --write-thumbnail may produce,
// depending on what format the source thumbnail was served in.
//
// This list has one home. Both the downloader (which finds the file yt-dlp just
// wrote) and the import worker (which goes looking for files written by earlier
// downloads) walk it, and a copy in either package would be a list two packages
// must remember to keep in lockstep.
var ThumbnailExts = []string{".jpg", ".jpeg", ".png", ".webp"}

// ThumbnailMime maps a thumbnail file's extension to the Content-Type to serve
// it as. Unknown extensions get "application/octet-stream" — no sniffing: the
// only files that reach here are ones this package's own extension list matched.
func ThumbnailMime(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

// ThumbnailExtForMime is ThumbnailMime's inverse, for naming the served file.
// http.ServeContent infers Content-Type from the name it is given, so handing it
// a name with the right extension is what makes the response carry the right
// type without setting the header by hand.
func ThumbnailExtForMime(mime string) string {
	switch mime {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	default:
		return ".bin"
	}
}
