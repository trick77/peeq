package summarize

import "encoding/json"

// decodeChapters parses the videos.chapters JSON column into []Chapter,
// tolerating empty/malformed input by returning nil (no chapters known yet).
func decodeChapters(s string) []Chapter {
	if s == "" {
		return nil
	}
	var out []Chapter
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// encodeChapters renders chapters as the JSON array text stored in
// videos.chapters, always a valid JSON array ("[]" when empty or on a
// marshal error).
func encodeChapters(chapters []Chapter) string {
	if len(chapters) == 0 {
		return "[]"
	}
	b, err := json.Marshal(chapters)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// encodeKeyPoints renders key points as the JSON array text stored in
// videos.key_points, always a valid JSON array ("[]" when empty or on a
// marshal error).
func encodeKeyPoints(keyPoints []KeyPoint) string {
	if len(keyPoints) == 0 {
		return "[]"
	}
	b, err := json.Marshal(keyPoints)
	if err != nil {
		return "[]"
	}
	return string(b)
}
