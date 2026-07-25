package ytdlp

import "testing"

// The four date fields yt-dlp reports do not agree, and which one is right
// depends on how the video came to exist. These cases pin the precedence.
func TestAirDate_precedence(t *testing.T) {
	// 2021-06-15T12:00:00Z and 2019-01-02T03:04:05Z as unix seconds.
	const releaseTS int64 = 1623758400
	const uploadTS int64 = 1546398245

	cases := []struct {
		name string
		in   rawDates
		want string
	}{
		{
			// The case that motivated all of this. A premiere is uploaded days
			// or years before it airs; upload_date is when the file was staged,
			// release_timestamp is when viewers could watch it. Sorting by the
			// former files it under the wrong year.
			name: "premiere prefers release_timestamp over upload_date",
			in: rawDates{
				ReleaseTimestamp: releaseTS,
				Timestamp:        uploadTS,
				ReleaseDate:      "20210615",
				UploadDate:       "20190102",
			},
			want: "2021-06-15",
		},
		{
			name: "release_date wins when only the string form is present",
			in:   rawDates{ReleaseDate: "20210615", UploadDate: "20190102"},
			want: "2021-06-15",
		},
		{
			// timestamp is upload time to the second; upload_date is the same
			// instant truncated. Identical result here, but timestamp is the
			// more precise source, so it is consulted first.
			name: "timestamp beats upload_date",
			in:   rawDates{Timestamp: uploadTS, UploadDate: "20190102"},
			want: "2019-01-02",
		},
		{
			name: "plain upload falls all the way through",
			in:   rawDates{UploadDate: "20190102"},
			want: "2019-01-02",
		},
		{
			name: "nothing reported yields empty, never a guess",
			in:   rawDates{},
			want: "",
		},
		{
			// A live stream that never aired: yt-dlp reports the scheduled
			// release as 0 rather than omitting the key.
			name: "zero timestamps are ignored, not treated as 1970",
			in:   rawDates{Timestamp: 0, UploadDate: "20190102"},
			want: "2019-01-02",
		},
		{
			// Garbage in one field must not shadow a good value further down.
			name: "an unparseable release_date falls through",
			in:   rawDates{ReleaseDate: "notadate", UploadDate: "20190102"},
			want: "2019-01-02",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := airDate(tc.in)
			if got != tc.want {
				t.Fatalf("airDate = %q, want %q", got, tc.want)
			}
		})
	}
}

// normalizeUploadDate used to check only len(raw) == 8, so any eight
// characters became a "date": "abcdefgh" produced "abcd-ef-gh", which then
// sorted as a real value and displayed as one.
func TestNormalizeUploadDate_rejectsNonDates(t *testing.T) {
	cases := []struct{ in, want string }{
		{"20190102", "2019-01-02"},
		{"abcdefgh", ""},
		{"20191301", ""}, // month 13
		{"20190230", ""}, // 30 February
		{"2019010", ""},  // too short
		{"201901022", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeUploadDate(tc.in); got != tc.want {
			t.Fatalf("normalizeUploadDate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
