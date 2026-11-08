package jobs

// Job mirrors one row of the download_jobs table. StartedAt and FinishedAt
// are empty strings when the underlying column is NULL (job not yet
// started / not yet finished).
//
// Table: download_jobs
type Job struct {
	ID          int64
	VideoID     string
	State       string
	Priority    int
	Attempts    int
	MaxAttempts int
	LastError   string
	LogTail     string
	EnqueuedAt  string
	StartedAt   string
	FinishedAt  string
	// NextAttemptAt is the wall-clock floor before which a requeued job is
	// not claimable (see BumpAfter); empty means claimable now.
	NextAttemptAt string
}
