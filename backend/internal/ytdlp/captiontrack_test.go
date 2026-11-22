package ytdlp

import "testing"

// uploaded is an uploaded track as yt-dlp lists it: formats with the
// timedtext URL. translatedTo adds tlang= the way yt-dlp builds a translation.
func uploaded(lang, translatedTo string) []captionFormat {
	u := "https://www.youtube.com/api/timedtext?v=x&lang=" + lang
	if translatedTo != "" {
		u += "&tlang=" + translatedTo
	}
	return []captionFormat{{URL: u + "&fmt=json3"}, {URL: u + "&fmt=vtt"}}
}

// speech is a speech-recognition track: YouTube's URL carries kind=asr.
func speech(lang, translatedTo string) []captionFormat {
	u := "https://www.youtube.com/api/timedtext?v=x&kind=asr&lang=" + lang
	if translatedTo != "" {
		u += "&tlang=" + translatedTo
	}
	return []captionFormat{{URL: u + "&fmt=vtt"}}
}

// asrInfo is a video whose speech YouTube transcribed as lang: the original
// under "<lang>-orig" and "<lang>", and every other language as a translation
// of it, exactly as yt-dlp's extractor builds automatic_captions.
func asrInfo(lang string, translations ...string) map[string][]captionFormat {
	m := map[string][]captionFormat{
		lang + "-orig": speech(lang, ""),
		lang:           speech(lang, ""),
	}
	for _, t := range translations {
		m[t] = speech(lang, t)
	}
	return m
}

func TestPickCaptionTrack(t *testing.T) {
	for _, tc := range []struct {
		name      string
		info      captionInfo
		anyManual bool
		want      CaptionTrack
	}{
		{
			name: "english speech",
			info: captionInfo{AutomaticCaptions: asrInfo("en", "de", "fr")},
			want: CaptionTrack{Key: "en-orig", Lang: "en", Speech: "en"},
		},
		{
			// The case that left three videos without a transcript: "en" exists,
			// but only as YouTube's machine translation of the German track, and
			// that request answered 429 every time.
			name: "german speech never takes the english translation",
			info: captionInfo{Language: "en-US", AutomaticCaptions: asrInfo("de", "en", "fr")},
			want: CaptionTrack{Key: "de-orig", Lang: "de", Speech: "de"},
		},
		{
			// The track is English, the talk is German: audio_language says German.
			name: "manual english beats german speech",
			info: captionInfo{
				Subtitles:         map[string][]captionFormat{"en": uploaded("en", "")},
				AutomaticCaptions: asrInfo("de", "en"),
			},
			want: CaptionTrack{Key: "en", Manual: true, Lang: "en", Speech: "de"},
		},
		{
			name: "exact preferred key beats a regional one",
			info: captionInfo{Subtitles: map[string][]captionFormat{
				"en-GB": uploaded("en-GB", ""),
				"en":    uploaded("en", ""),
			}},
			want: CaptionTrack{Key: "en", Manual: true, Lang: "en", Speech: "en"},
		},
		{
			name: "regional manual english",
			info: captionInfo{Subtitles: map[string][]captionFormat{"en-GB": uploaded("en-GB", "")}},
			want: CaptionTrack{Key: "en-GB", Manual: true, Lang: "en", Speech: "en"},
		},
		{
			name: "manual track in the spoken language beats its asr",
			info: captionInfo{
				Subtitles:         map[string][]captionFormat{"de": uploaded("de", ""), "fr": uploaded("fr", "")},
				AutomaticCaptions: asrInfo("de", "en"),
			},
			want: CaptionTrack{Key: "de", Manual: true, Lang: "de", Speech: "de"},
		},
		{
			name: "asr in the spoken language beats a manual track in another",
			info: captionInfo{
				Subtitles:         map[string][]captionFormat{"fr": uploaded("fr", "")},
				AutomaticCaptions: asrInfo("de", "en"),
			},
			anyManual: true,
			want:      CaptionTrack{Key: "de-orig", Lang: "de", Speech: "de"},
		},
		{
			name:      "only a manual french track",
			info:      captionInfo{Subtitles: map[string][]captionFormat{"fr": uploaded("fr", "")}},
			anyManual: true,
			want:      CaptionTrack{Key: "fr", Manual: true, Lang: "fr", Speech: "fr"},
		},
		{
			// The track is French; nothing says the speech is, and the uploader
			// declared English.
			name:      "a lone french track on a declared english video",
			info:      captionInfo{Language: "en", Subtitles: map[string][]captionFormat{"fr": uploaded("fr", "")}},
			anyManual: true,
			want:      CaptionTrack{Key: "fr", Manual: true, Lang: "fr", Speech: "en"},
		},
		{
			// Before the last rung a lone foreign manual track waits: the
			// speech may be English and its automatic captions still to come.
			name: "a lone foreign manual track waits until allowed",
			info: captionInfo{Subtitles: map[string][]captionFormat{"fr": uploaded("fr", "")}},
			want: CaptionTrack{},
		},
		{
			// yt-dlp lists an uploaded track's "translation" into its own
			// language without tlang= ("Setting tlang=lang returns damaged
			// subtitles"). It is not speech recognition, so the lone upload
			// still waits.
			name: "an uploaded track's self-translation is not asr",
			info: captionInfo{
				Subtitles: map[string][]captionFormat{"fr": uploaded("fr", "")},
				AutomaticCaptions: map[string][]captionFormat{
					"fr-fr": uploaded("fr", ""),
					"en-fr": uploaded("fr", "en"),
				},
			},
			want: CaptionTrack{},
		},
		{
			name: "several foreign manual tracks: the uploader's language",
			info: captionInfo{Language: "es-419", Subtitles: map[string][]captionFormat{
				"fr": uploaded("fr", ""),
				"es": uploaded("es", ""),
			}},
			anyManual: true,
			want:      CaptionTrack{Key: "es", Manual: true, Lang: "es", Speech: "es"},
		},
		{
			name: "several foreign manual tracks, no hint: first by key",
			info: captionInfo{Subtitles: map[string][]captionFormat{
				"it": uploaded("it", ""),
				"fr": uploaded("fr", ""),
			}},
			anyManual: true,
			want:      CaptionTrack{Key: "fr", Manual: true, Lang: "fr", Speech: "fr"},
		},
		{
			// Translations of a manual track land in automatic_captions as
			// "<to>-<from>". None of them is a source.
			name: "manual translations are not tracks",
			info: captionInfo{
				Subtitles:         map[string][]captionFormat{"fr": uploaded("fr", "")},
				AutomaticCaptions: map[string][]captionFormat{"en-fr": uploaded("fr", "en"), "de-fr": uploaded("fr", "de")},
			},
			anyManual: true,
			want:      CaptionTrack{Key: "fr", Manual: true, Lang: "fr", Speech: "fr"},
		},
		{
			name:      "live chat replay is not a caption track",
			info:      captionInfo{Subtitles: map[string][]captionFormat{"live_chat": {{URL: "https://www.youtube.com/live_chat_replay"}}}},
			anyManual: true,
			want:      CaptionTrack{},
		},
		{
			// yt-dlp lists a language YouTube cannot translate into (Cantonese)
			// without the -orig twin.
			name: "asr without an -orig key",
			info: captionInfo{AutomaticCaptions: map[string][]captionFormat{"yue": speech("yue", "")}},
			want: CaptionTrack{Key: "yue", Lang: "yue", Speech: "yue"},
		},
		{
			name:      "nothing",
			info:      captionInfo{},
			anyManual: true,
			want:      CaptionTrack{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := PickCaptionTrack(tc.info, "en", tc.anyManual); got != tc.want {
				t.Fatalf("PickCaptionTrack = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The preferred language is configurable (BACKEND_DEFAULT_SUB_LANG); English
// is only the default. A regional preference is matched exactly before its
// siblings.
func TestPickCaptionTrack_honoursThePreferredLanguage(t *testing.T) {
	info := captionInfo{
		Subtitles:         map[string][]captionFormat{"en": uploaded("en", "")},
		AutomaticCaptions: asrInfo("de", "en"),
	}
	if got, want := PickCaptionTrack(info, "de", false), (CaptionTrack{Key: "de-orig", Lang: "de", Speech: "de"}); got != want {
		t.Fatalf("prefer de: %+v, want %+v", got, want)
	}
	if got, want := PickCaptionTrack(info, "", false), (CaptionTrack{Key: "en", Manual: true, Lang: "en", Speech: "de"}); got != want {
		t.Fatalf("prefer empty: %+v, want %+v", got, want)
	}
	regional := captionInfo{Subtitles: map[string][]captionFormat{
		"zh-Hans": uploaded("zh-Hans", ""),
		"zh-Hant": uploaded("zh-Hant", ""),
	}}
	if got := PickCaptionTrack(regional, "zh-Hant", false); got.Key != "zh-Hant" {
		t.Fatalf("prefer zh-Hant: %+v, want the zh-Hant track", got)
	}
}

// The held-back flag tells "nothing listed" from "an uploaded track waits for
// the last attempt", which the caller logs.
func TestPickCaptionTrack_reportsAHeldBackTrack(t *testing.T) {
	lone := captionInfo{Subtitles: map[string][]captionFormat{"fr": uploaded("fr", "")}}
	if _, held := pickCaptionTrack(lone, "en", false); !held {
		t.Fatal("a lone foreign upload before the last attempt is not reported held back")
	}
	if _, held := pickCaptionTrack(lone, "en", true); held {
		t.Fatal("a track taken is reported held back")
	}
	if _, held := pickCaptionTrack(captionInfo{}, "en", false); held {
		t.Fatal("an empty list is reported held back")
	}
}

// The shape yt-dlp -J prints (trimmed from a real German video): the track
// list decodes, and the pick is the original, not the translation.
func TestParseCaptionInfo(t *testing.T) {
	raw := []byte(`{"id":"hDW2qdawQm0","language":"en-US","subtitles":{},
"automatic_captions":{
 "de-orig":[{"ext":"vtt","url":"https://www.youtube.com/api/timedtext?v=hDW2qdawQm0&lang=de&kind=asr&fmt=vtt","name":"German (Original)"}],
 "de":[{"ext":"vtt","url":"https://www.youtube.com/api/timedtext?v=hDW2qdawQm0&lang=de&kind=asr&fmt=vtt","name":"German"}],
 "en":[{"ext":"vtt","url":"https://www.youtube.com/api/timedtext?v=hDW2qdawQm0&lang=de&kind=asr&tlang=en&fmt=vtt","name":"English"}]}}`)
	info, err := parseCaptionInfo(raw)
	if err != nil {
		t.Fatalf("parseCaptionInfo: %v", err)
	}
	if got, want := PickCaptionTrack(info, "en", false), (CaptionTrack{Key: "de-orig", Lang: "de", Speech: "de"}); got != want {
		t.Fatalf("pick = %+v, want %+v", got, want)
	}
	if _, err := parseCaptionInfo([]byte("not json")); err == nil {
		t.Fatal("garbage decoded without an error")
	}
}
