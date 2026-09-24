package videos

import (
	"context"
	"errors"
	"fmt"

	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/store"
)

// ErrNotFound is returned by EnqueueDownload for an id with no videos row.
// Every production caller looks the row up first, so reaching it means the
// row vanished in between (a channel delete cascading under a click): a
// benign race that is answered as a logged 500 rather than given its own
// status.
var ErrNotFound = errors.New("videos: not found")

// EnqueueDownload marks the video 'queued' and inserts its download job in
// ONE transaction. "This video is in the download queue" is a single fact
// spread over two tables, and the three handlers that used to write it as
// two statements could leave a 'queued' row with no job behind it when the
// insert failed — a video nothing would ever pick up.
//
// A pending Inbox row for the same video moves to 'queued' in the same
// transaction. The add-by-URL and extension paths never touched the ledger,
// so a video the scan had put on the Inbox stayed 'pending' there after it
// was queued another way: the Inbox kept offering it, approving it enqueued
// a second job, and the caption fetcher kept reading it. An 'ignored' row is
// a user decision and is left alone.
func (s *Store) EnqueueDownload(id string, priority int) (int64, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("enqueue download %s: begin: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	jobID, err := enqueueTx(ctx, tx, id, priority)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("enqueue download %s: commit: %w", id, err)
	}
	return jobID, nil
}

// UpsertAndEnqueueDownload is EnqueueDownload for a row that may not exist
// yet: the upsert (same rules as Upsert — never touches status, keeps known
// metadata) and the enqueue commit or roll back together, so a failed
// enqueue leaves no half-made video row.
func (s *Store) UpsertAndEnqueueDownload(v Video, priority int) (int64, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("enqueue download %s: begin: %w", v.ID, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := upsertTx(ctx, tx, v); err != nil {
		return 0, err
	}
	jobID, err := enqueueTx(ctx, tx, v.ID, priority)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("enqueue download %s: commit: %w", v.ID, err)
	}
	return jobID, nil
}

func enqueueTx(ctx context.Context, tx store.DBTX, id string, priority int) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`UPDATE videos SET status = ?, error_message = '' WHERE id = ?`, StatusQueued, id)
	if err != nil {
		return 0, fmt.Errorf("enqueue download %s: set status: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return 0, fmt.Errorf("enqueue download %s: rows affected: %w", id, err)
	} else if n == 0 {
		return 0, fmt.Errorf("enqueue download %s: %w", id, ErrNotFound)
	}
	// The same transition channelvideos.Store.SetState(id, StateQueued) makes,
	// narrowed to a 'pending' row. It is written out here because channelvideos
	// imports this package (its caption candidate query reads video status),
	// so the statement cannot be shared without a cycle; SetState's test and
	// TestEnqueueDownload_movesPendingLedgerRowToQueued are what keep the two
	// in step.
	if _, err := tx.ExecContext(ctx, `
UPDATE channel_videos
   SET state = 'queued', unavailable_reason = '', unavailable_at = NULL, decided_at = datetime('now')
 WHERE video_id = ? AND state = 'pending'`, id); err != nil {
		return 0, fmt.Errorf("enqueue download %s: settle pending row: %w", id, err)
	}
	var jobID int64
	if err := tx.QueryRowContext(ctx, jobs.EnqueueSQL, id, priority).Scan(&jobID); err != nil {
		return 0, fmt.Errorf("enqueue download %s: insert job: %w", id, err)
	}
	return jobID, nil
}
