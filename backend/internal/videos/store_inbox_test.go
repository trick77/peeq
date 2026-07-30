package videos

import "testing"

// TestUpsertPreservesAnalysis is the load-bearing test for inbox summaries.
//
// The feature's whole promise is that reading a video from the Inbox and then
// downloading it does not pay for the model twice. That works because the
// approve-from-inbox path re-upserts the row from ledger metadata, and Upsert's
// ON CONFLICT clause touches only metadata columns — it never writes summary,
// subtitle_path, summary_status, chapters or status.
//
// Today that is true by construction rather than by intent: nobody wrote
// Upsert with a summary in mind. Which is exactly why it needs pinning. Adding
// one more column to that UPDATE SET is an ordinary-looking change that would
// break the feature silently — the summary would simply be gone by the time
// anyone looked, with no error anywhere.
func TestUpsertPreservesAnalysis(t *testing.T) {
	s := newTestStore(t)

	if err := s.Upsert(Video{ID: "v1", URL: "https://youtu.be/v1", Title: "Original"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetSubtitle("v1", ".summaries/v1/v1.en.vtt", "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}
	if err := s.SetSummaryText("v1", "The summary that must survive."); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	if err := s.SetSummaryStatus("v1", SummaryDone, ""); err != nil {
		t.Fatalf("set summary status: %v", err)
	}
	if err := s.SetStatus("v1", StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}

	// Exactly what handlePendingDownload does when the user presses Download:
	// re-upsert from the ledger's metadata, which carries no analysis at all.
	if err := s.Upsert(Video{
		ID: "v1", URL: "https://youtu.be/v1", Title: "Refreshed from the ledger",
		ChannelID: "UC1", DurationSeconds: 1122,
	}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}

	v, err := s.Get("v1")
	if err != nil || v == nil {
		t.Fatalf("get: %v", err)
	}
	if v.Summary != "The summary that must survive." {
		t.Fatalf("summary = %q; the download path must not clear it", v.Summary)
	}
	if v.SubtitlePath != ".summaries/v1/v1.en.vtt" {
		t.Fatalf("subtitle_path = %q; the download path must not clear it", v.SubtitlePath)
	}
	if v.SummaryStatus != SummaryDone {
		t.Fatalf("summary_status = %q, want it left at done", v.SummaryStatus)
	}
	if v.Status != StatusNew {
		t.Fatalf("status = %q; Upsert must not own the lifecycle column", v.Status)
	}
	// The metadata half is what Upsert IS for, so prove it still happened.
	if v.Title != "Refreshed from the ledger" {
		t.Fatalf("title = %q, want the refreshed one", v.Title)
	}
}
