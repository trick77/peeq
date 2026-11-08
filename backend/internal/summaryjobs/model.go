package summaryjobs

// Job is one video's summarization work item, with the retry bookkeeping the
// worker uses to decide between another attempt and a permanent failure.
//
// Table: summary_jobs
type Job struct {
	ID          int64
	VideoID     string
	State       string
	Attempts    int
	MaxAttempts int
	LastError   string
}
