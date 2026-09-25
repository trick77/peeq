package store

import "time"

// TimeLayout is the text form SQLite's datetime('now') writes and every
// stamp column in peeq is compared against: UTC, second precision, no zone.
// Go code goes through FormatTime and ParseTime rather than the layout
// directly, so a stamp can never be written in a local zone that a
// datetime('now') comparison would misjudge by the offset.
const TimeLayout = "2006-01-02 15:04:05"

// FormatTime renders t as a stamp column value, in UTC.
func FormatTime(t time.Time) string {
	return t.UTC().Format(TimeLayout)
}

// ParseTime reads a stamp column value back as a UTC time.
func ParseTime(s string) (time.Time, error) {
	return time.ParseInLocation(TimeLayout, s, time.UTC)
}
