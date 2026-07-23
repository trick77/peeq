// Package activity is peeq's background-work log: the durable record of what the
// scan/download/summary/retention/ytdlp workers already did (activity_events),
// plus the pure projection of what they will do next (see upcoming.go). Only
// AUTOMATIC work is recorded here — a user's own clicks stay out — and a no-op
// pass (a sweep that reclaimed nothing, a self-update that changed nothing)
// records nothing at all: the "silence rule" that keeps the agenda from filling
// with "nothing happened".
package activity

import (
	"database/sql"
	"log/slog"
	"time"
)

// maxRows caps the retained log. The past half of the agenda is bounded by row
// count, never by date — a fixed ceiling the trim enforces on every Record.
const maxRows = 2000

// Kinds and outcomes — mirror the migration's CHECK constraints.
const (
	KindScan        = "scan"
	KindChannelMeta = "channel_meta"
	KindDownload    = "download"
	KindSummary     = "summary"
	KindRetention   = "retention"
	KindYtdlp       = "ytdlp"
	KindAccess      = "access"

	OutcomeOK   = "ok"
	OutcomeWarn = "warn"
	OutcomeFail = "fail"
)

// Event is one recorded piece of automatic work. Subject is the display name
// frozen at write time (this log outlives the video/channel it names), so it is
// never re-joined.
type Event struct {
	ID        int64  `json:"id"`
	At        string `json:"at"`
	Kind      string `json:"kind"`
	Outcome   string `json:"outcome"`
	SubjectID string `json:"subject_id,omitempty"`
	Subject   string `json:"subject,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// Store persists activity_events.
type Store struct {
	db *sql.DB
	// OnRecord, when set, fires with the fully-populated event after a successful
	// insert, so main.go can fan it out over the SSE hub. Optional.
	OnRecord func(Event)
}

func New(db *sql.DB) *Store { return &Store{db: db} }

// Record inserts one terminal event, trims the log back to maxRows, and fires
// OnRecord. It must NEVER fail its caller: a worker whose scan actually
// succeeded must not report failure because the audit write broke, so every
// error here is logged at ERROR and swallowed. The caller passes no id/at — the
// row's id is assigned by AUTOINCREMENT and its timestamp by the column default.
func (s *Store) Record(e Event) {
	res, err := s.db.Exec(
		`INSERT INTO activity_events (kind, outcome, subject_id, subject, summary, detail)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		e.Kind, e.Outcome, e.SubjectID, e.Subject, e.Summary, e.Detail)
	if err != nil {
		slog.Error("activity record failed", "err", err, "kind", e.Kind)
		return
	}
	id, _ := res.LastInsertId()

	// Trim by EXACT row count via OFFSET, not `id <= max_id - maxRows`: id gaps
	// (there are none today, but a future delete could make some) would let the
	// arithmetic silently under-retain. OFFSET counts real rows. Index-only walk
	// on the primary key.
	if _, err := s.db.Exec(
		`DELETE FROM activity_events
		 WHERE id <= (SELECT id FROM activity_events ORDER BY id DESC LIMIT 1 OFFSET ?)`,
		maxRows); err != nil {
		slog.Error("activity trim failed", "err", err)
	}

	if s.OnRecord != nil {
		e.ID = id
		// datetime('now') is UTC to the second; match its format so the SSE-fanned
		// event carries the same timestamp the row got, without a re-read.
		e.At = time.Now().UTC().Format("2006-01-02 15:04:05")
		s.OnRecord(e)
	}
}

// Page is one keyset page of the log, newest first.
type Page struct {
	Events  []Event
	HasMore bool
}

// Recent returns up to limit events with id < beforeID (beforeID == 0 means the
// newest page), newest first. Pagination keys on id, never `at`: timestamps
// collide at the 1-second datetime('now') resolution, so `at` cannot order rows
// written in the same second. HasMore reports whether an older row exists beyond
// this page (fetched via a limit+1 probe), which is what the page's bottom edge
// renders against.
func (s *Store) Recent(beforeID int64, limit int) (Page, error) {
	if limit <= 0 {
		limit = 40
	}
	q := `SELECT id, at, kind, outcome, subject_id, subject, summary, detail
	      FROM activity_events`
	args := []any{}
	if beforeID > 0 {
		q += ` WHERE id < ?`
		args = append(args, beforeID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1) // +1 probes for a further page

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.At, &e.Kind, &e.Outcome,
			&e.SubjectID, &e.Subject, &e.Summary, &e.Detail); err != nil {
			return Page{}, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}

	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return Page{Events: out, HasMore: hasMore}, nil
}

// RetainedMax is the fixed row ceiling, surfaced so the UI can label the log's
// oldest edge ("the most recent N of up to 2000").
func (s *Store) RetainedMax() int { return maxRows }
