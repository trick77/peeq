package ytdlp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// captionInfo is the part of yt-dlp's info dict that lists a video's caption
// tracks: uploaded ones under subtitles, YouTube's speech recognition (ASR)
// under automatic_captions.
//
// automatic_captions is mostly NOT tracks. yt-dlp lists the ASR original twice
// ("<lang>-orig" and "<lang>") and then every language YouTube can translate it
// into, each a URL with tlang= on the same original; translations of an
// uploaded track appear there too, as "<to>-<from>". Asking for "en" on a German
// video therefore asks for a machine translation, and YouTube answered that
// request with 429 on the same videos every time: they settled without a
// transcript while their German one was there all along.
type captionInfo struct {
	Language          string                     `json:"language"`
	Subtitles         map[string][]captionFormat `json:"subtitles"`
	AutomaticCaptions map[string][]captionFormat `json:"automatic_captions"`
}

type captionFormat struct {
	URL string `json:"url"`
}

func parseCaptionInfo(raw []byte) (captionInfo, error) {
	var info captionInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return captionInfo{}, fmt.Errorf("ytdlp: parse caption list: %w", err)
	}
	return info, nil
}

// CaptionTrack is the one track a caption fetch asks for. Key is yt-dlp's
// language key, Manual says which list it is in (it decides --write-subs vs
// --write-auto-subs), Lang is the track's primary language subtag. Speech is
// the language the video is spoken in, as far as the list tells: the ASR
// original's, else the uploader's declared one, else the track's. It is what
// audio_language stores (the Player shows it as "Audio language"); an uploaded
// English track on a German talk is Lang en, Speech de. The zero value means
// no usable track.
type CaptionTrack struct {
	Key    string
	Manual bool
	Lang   string
	Speech string
}

// liveChatKey is where yt-dlp puts a stream's chat replay, among the uploaded
// subtitles. It is not a transcript.
const liveChatKey = "live_chat"

// PickCaptionTrack chooses the track that best transcribes the video, never a
// translation. In order:
//
//  1. an uploaded track in prefer (an exact key before a regional one);
//  2. ASR in prefer: the video is spoken in it;
//  3. an uploaded track in the spoken language (the ASR original's);
//  4. the ASR original, whatever the language;
//  5. with anyManual, any uploaded track: the uploader's declared language,
//     else the first by key.
//
// Rank 5 waits for anyManual because ASR lags a fresh upload by minutes to
// hours while uploaded tracks are there at once: an English video with only a
// French upload so far would otherwise be read in French for good. The inbox
// allows it on its last attempt; a download, which gets one attempt, always.
func PickCaptionTrack(info captionInfo, prefer string, anyManual bool) CaptionTrack {
	track, _ := pickCaptionTrack(info, prefer, anyManual)
	return track
}

// pickCaptionTrack is PickCaptionTrack, also reporting whether an uploaded
// track was held back for want of anyManual — so a caller can log why a
// video with a track still reads as having none.
func pickCaptionTrack(info captionInfo, prefer string, anyManual bool) (CaptionTrack, bool) {
	prefer = strings.ToLower(strings.TrimSpace(prefer))
	if prefer == "" {
		prefer = "en"
	}

	var manual []string
	for k, fs := range info.Subtitles {
		if k != liveChatKey && len(fs) > 0 {
			manual = append(manual, k)
		}
	}
	sort.Strings(manual)

	// ASR originals: the -orig keys, or, for a language YouTube cannot
	// translate into, the bare key yt-dlp lists without the -orig twin.
	// Untranslated is not enough to be one: yt-dlp leaves tlang= off an
	// uploaded track's "translation" into its own language too ("fr-fr" for an
	// uploaded "fr"), and taking that for ASR would make a lone uploaded track
	// look like the spoken language's.
	var orig, bare []string
	for k, fs := range info.AutomaticCaptions {
		if len(fs) == 0 || hasQuery(fs, "tlang", "") {
			continue
		}
		switch {
		case strings.HasSuffix(k, "-orig"):
			orig = append(orig, k)
		case hasQuery(fs, "kind", "asr"):
			bare = append(bare, k)
		}
	}
	asr := orig
	if len(asr) == 0 {
		asr = bare
	}
	sort.Strings(asr)

	asrLang := func(k string) string { return langBase(strings.TrimSuffix(k, "-orig")) }
	// The spoken language, as far as the list tells (CaptionTrack.Speech).
	speech := langBase(info.Language)
	if len(asr) > 0 {
		speech = asrLang(asr[0])
	}
	manualTrack := func(k string) CaptionTrack {
		t := CaptionTrack{Key: k, Manual: true, Lang: langBase(k), Speech: speech}
		if t.Speech == "" {
			t.Speech = t.Lang
		}
		return t
	}
	asrTrack := func(k string) CaptionTrack {
		return CaptionTrack{Key: k, Lang: asrLang(k), Speech: asrLang(k)}
	}
	// firstIn finds lang's key among keys: the exact tag first (a configured
	// "pt-BR" before a sibling "pt-PT"), then any with the same primary subtag.
	firstIn := func(keys []string, lang string) string {
		for _, k := range keys {
			if strings.EqualFold(strings.TrimSuffix(k, "-orig"), lang) {
				return k
			}
		}
		base := langBase(lang)
		for _, k := range keys {
			if base != "" && asrLang(k) == base {
				return k
			}
		}
		return ""
	}

	if k := firstIn(manual, prefer); k != "" {
		return manualTrack(k), false
	}
	if k := firstIn(asr, prefer); k != "" {
		return asrTrack(k), false
	}
	for _, a := range asr {
		if k := firstIn(manual, asrLang(a)); k != "" {
			return manualTrack(k), false
		}
	}
	if len(asr) > 0 {
		return asrTrack(asr[0]), false
	}
	if len(manual) == 0 {
		return CaptionTrack{}, false
	}
	if !anyManual {
		return CaptionTrack{}, true
	}
	if k := firstIn(manual, info.Language); k != "" {
		return manualTrack(k), false
	}
	return manualTrack(manual[0]), false
}

// hasQuery reports whether any of a track's URLs carries the query key, with
// value when value is non-empty. yt-dlp marks a translation with tlang= and
// YouTube marks speech recognition with kind=asr.
func hasQuery(fs []captionFormat, key, value string) bool {
	for _, f := range fs {
		u, err := url.Parse(f.URL)
		if err != nil {
			continue
		}
		q := u.Query()
		if q.Has(key) && (value == "" || q.Get(key) == value) {
			return true
		}
	}
	return false
}

// langBase is a language tag's primary subtag, lowercased: "en-US" and "en"
// are both "en". yt-dlp keys and the uploader's declared language mix the two.
func langBase(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(tag, "-_"); i >= 0 {
		tag = tag[:i]
	}
	return tag
}
