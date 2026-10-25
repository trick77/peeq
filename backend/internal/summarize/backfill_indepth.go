package summarize

// BACKFILL(in-depth): one-off fill of videos analysed before 0039. Remove
// (this file, backfill_indepth_test.go, videos/store_indepth_backfill*.go and
// the call in Worker.Run) once in_depth_retry has drained to 0.

// backfillInDepth queues an in-depth retry for every unwatched video that has
// a summary but no in-depth text; the retry path writes and indexes them one
// idle turn at a time. Runs at every boot: a queued video keeps its row.
func (w *Worker) backfillInDepth() {
	n, err := w.d.Videos.QueueMissingInDepth()
	if err != nil {
		w.d.Logger.Error("summarize worker: in-depth backfill failed", "err", err)
		return
	}
	if n > 0 {
		w.d.Logger.Info("summarize worker: in-depth backfill queued", "count", n)
	}
}
